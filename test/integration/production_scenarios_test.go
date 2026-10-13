//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// Scenarios for the production-readiness fixes that depend on the real
// stack: the send intent is in PostgreSQL (through PgBouncer), the document
// ref in Valkey and its bytes in floci S3.

// sendAccepted fetches key into a ref on r and sends it under messageKey,
// returning the ref and the accepted send's output.
func sendAccepted(t *testing.T, r *replica, tenant, key, messageKey string) (string, map[string]any) {
	t.Helper()
	fetched := runTask(tenantCtx(tenant), r.byType[registry.TypeStorage], "", fetchInput(key), maxAttempts)
	require.NoError(t, last(fetched).err)
	ref := last(fetched).out["contentRef"].(string)

	sent := runTask(tenantCtx(tenant), r.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{ref}, "messageKey", messageKey), maxAttempts)
	require.Len(t, sent, 1)
	require.NoError(t, last(sent).err)
	require.Equal(t, true, last(sent).out["sent"])
	return ref, last(sent).out
}

// assertAcceptedDuplicate: a redelivery of the accepted send succeeds as a
// duplicate carrying the accepted attempt's message ID, and sends nothing.
func assertAcceptedDuplicate(t *testing.T, p *platform, r *replica, email *scriptedEmail, tenant, ref, messageKey string, first map[string]any) {
	t.Helper()
	again := runTask(tenantCtx(tenant), r.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{ref}, "messageKey", messageKey), maxAttempts)
	require.Len(t, again, 1)
	require.NoError(t, last(again).err, "a duplicate of an accepted email succeeds without resolving its attachments")
	out := last(again).out
	assert.Equal(t, true, out["duplicate"])
	assert.Equal(t, false, out["sent"])
	assert.Equal(t, string(sendintent.StatusAccepted), out["status"])
	assert.Equal(t, first["messageId"], out["messageId"], "the accepted attempt's message ID")
	sentAt, ok := out["sentAt"].(time.Time)
	require.True(t, ok, "sentAt is the accepted attempt's time")
	assert.False(t, sentAt.IsZero())
	assert.Len(t, email.sent(), 1, "the recipient got it once")

	in := intent(t, p, tenant, messageKey)
	assert.Equal(t, sendintent.StatusAccepted, in.Status)
	assert.Equal(t, 1, in.Attempts, "the duplicate did not re-reserve")
}

// IT-07: the email was accepted, then its attachment ref was deleted (the
// Valkey record and the S3 object) before the job was redelivered. The
// intent is reserved before attachments are resolved, so the redelivery is
// an accepted duplicate (success) rather than a permanent "ref not found".
func TestIT07_AcceptedDuplicate_AfterAttachmentRefDeleted_Succeeds(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	tenant := "tenant-" + uuid.NewString()
	email := &scriptedEmail{steps: []emailStep{{}}}
	a := p.startReplica(t, tenantBucketWith(t, "receipt.pdf", []byte("%PDF receipt")), email)
	b := p.startReplica(t, storage.NewMockStorageClient(), email)

	ref, first := sendAccepted(t, a, tenant, "receipt.pdf", "receipt-9")
	meta, err := a.docRefs.Lookup(context.Background(), tenant, ref)
	require.NoError(t, err)
	require.NoError(t, a.docRefs.Delete(context.Background(), tenant, ref))
	_, err = b.docRefs.Lookup(context.Background(), tenant, ref)
	require.ErrorIs(t, err, docref.ErrNotFound, "the ref is gone for every replica")
	_, err = s3Client(t).HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(meta.Bucket), Key: aws.String(meta.ObjectKey)})
	require.Error(t, err, "the S3 object is gone too")

	assertAcceptedDuplicate(t, p, b, email, tenant, ref, "receipt-9", first)
}

// IT-08: the same, with the ref expired by its Valkey TTL instead of deleted.
func TestIT08_AcceptedDuplicate_AfterAttachmentRefExpired_Succeeds(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	p.refTTL = 2 * time.Second
	tenant := "tenant-" + uuid.NewString()
	email := &scriptedEmail{steps: []emailStep{{}}}
	r := p.startReplica(t, tenantBucketWith(t, "statement.pdf", []byte("%PDF statement")), email)

	ref, first := sendAccepted(t, r, tenant, "statement.pdf", "statement-3")
	require.Eventually(t, func() bool {
		_, err := r.docRefs.Lookup(context.Background(), tenant, ref)
		return errors.Is(err, docref.ErrNotFound)
	}, 10*time.Second, 100*time.Millisecond, "Valkey expires the ref")

	assertAcceptedDuplicate(t, p, r, email, tenant, ref, "statement-3", first)
}

// IT-09: an attachment that no longer resolves fails the send after the
// intent was reserved: the intent is recorded not_delivered in PostgreSQL
// (not left pending), so once the workflow fetches the document again the
// same messageKey is re-reserved and sent — on another replica.
func TestIT09_AttachmentMissingAfterReserve_NotDelivered_ThenSendsOnce(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	tenant := "tenant-" + uuid.NewString()
	email := &scriptedEmail{steps: []emailStep{{}}}
	bucket := tenantBucketWith(t, "notice.pdf", []byte("%PDF notice"))
	a, b := p.startReplica(t, bucket, email), p.startReplica(t, bucket, email)

	stale := last(runTask(tenantCtx(tenant), a.byType[registry.TypeStorage], "", fetchInput("notice.pdf"), maxAttempts)).out["contentRef"].(string)
	require.NoError(t, a.docRefs.Delete(context.Background(), tenant, stale))

	failed := runTask(tenantCtx(tenant), a.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{stale}, "messageKey", "notice-1"), maxAttempts)
	require.Len(t, failed, 1, "permanent: not retried")
	assert.ErrorIs(t, last(failed).err, docref.ErrNotFound)
	assert.Equal(t, connectors.ClassPermanent, last(failed).decision.Class)
	in := intent(t, p, tenant, "notice-1")
	assert.Equal(t, sendintent.StatusNotDelivered, in.Status, "recorded, not left pending")
	assert.Equal(t, 1, in.Attempts)
	assert.Empty(t, email.sent())

	fresh := last(runTask(tenantCtx(tenant), b.byType[registry.TypeStorage], "", fetchInput("notice.pdf"), maxAttempts)).out["contentRef"].(string)
	sent := runTask(tenantCtx(tenant), b.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{fresh}, "messageKey", "notice-1"), maxAttempts)
	require.Len(t, sent, 1)
	require.NoError(t, last(sent).err, "a not_delivered intent is re-reserved")
	assert.Equal(t, true, last(sent).out["sent"])
	msgs := email.sent()
	require.Len(t, msgs, 1)
	assert.Equal(t, []byte("%PDF notice"), msgs[0].Attachments[0].Content)
	in = intent(t, p, tenant, "notice-1")
	assert.Equal(t, sendintent.StatusAccepted, in.Status)
	assert.Equal(t, 2, in.Attempts)
}

// IT-10: Valkey refuses the ref's metadata write (an ACL user without the
// script commands: NOPERM). storage's createDocument fails, is not retried,
// and the content already written to S3 is deleted again: nothing is left
// under the tenant's prefix and no ref resolves.
func TestIT10_DocRefCreate_ValkeyRefusesWrite_S3ObjectCleanedUp(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	ctx := context.Background()
	tenant := "tenant-" + uuid.NewString()

	admin := redis.NewClient(&redis.Options{Addr: p.valkeyAddr})
	t.Cleanup(func() { _ = admin.Close() })
	// The runbook's worker ACL without +eval / +evalsha: every read and
	// delete works, the create script is refused.
	user, password := "docref-noscript-"+uuid.NewString()[:8], uuid.NewString()
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", user, "on", ">"+password, "resetkeys", "~docref:*", "resetchannels",
		"-@all", "+exists", "+hmget", "+hset", "+time", "+pexpireat", "+hgetall", "+hget", "+del", "+waitaof", "+asking",
		"+config|get", "+ping").Err())
	t.Cleanup(func() { _ = admin.Do(context.Background(), "ACL", "DELUSER", user).Err() })
	vk := redis.NewClient(&redis.Options{Addr: p.valkeyAddr, Username: user, Password: password})
	t.Cleanup(func() { _ = vk.Close() })
	require.NoError(t, valkeystore.CheckDurability(ctx, vk))

	refs := docref.NewService(valkeystore.New(vk, valkeystore.Options{TTL: time.Hour}), s3content.New(s3Client(t), p.bucket), docref.WithKeyPrefix("docrefs/"))
	byType, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		StorageProviders: map[string]storage.ProviderConstructor{
			"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
				return tenantBucketWith(t, "report.pdf", []byte("%PDF report")), nil
			},
		},
		DocRefs: refs,
	})
	require.NoError(t, err)

	got := runTask(tenantCtx(tenant), byType[registry.TypeStorage], "", fetchInput("report.pdf"), maxAttempts)
	require.Len(t, got, 1, "a refused metadata write is not retried")
	require.Error(t, last(got).err)
	assert.True(t, redis.IsPermissionError(last(got).err) || strings.Contains(last(got).err.Error(), "NOPERM"), "%v", last(got).err)
	assert.False(t, last(got).decision.Retry)
	assert.NotEqual(t, connectors.ClassTransient, last(got).decision.Class, "a missing grant is not retried as an outage")

	listed, err := s3Client(t).ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(p.bucket), Prefix: aws.String("docrefs/" + tenant + "/")})
	require.NoError(t, err)
	assert.Empty(t, listed.Contents, "the content written before the refused metadata write was deleted")
	keys, err := admin.Keys(ctx, "docref:*").Result()
	require.NoError(t, err)
	for _, k := range keys {
		tn, err := admin.HGet(ctx, k, "tenant_id").Result()
		if err == nil {
			assert.NotEqual(t, tenant, tn, "no ref record for the tenant")
		}
	}
}
