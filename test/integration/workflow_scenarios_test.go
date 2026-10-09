//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

const maxAttempts = 5

// IT-01: the invoice workflow. Replica A fetches the invoice into a document
// ref; replica B emails it. The email provider throttles once, so the worker
// retries and the invoice is delivered exactly once, with its exact bytes.
// Valkey holds only the reference; the bytes are in S3.
func TestIT01_InvoiceWorkflow_AcrossReplicas_RetriesThrottledSend(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	tenant, invoice := "tenant-"+uuid.NewString(), []byte("%PDF-1.7 invoice 42\x00\xff")
	email := &scriptedEmail{steps: []emailStep{
		{err: sendemail.ClassifyStatus("sendgrid", http.StatusTooManyRequests, errors.New("rate limited"))},
		{},
	}}
	bucket := tenantBucketWith(t, "invoice-42.pdf", invoice)
	a, b := p.startReplica(t, bucket, email), p.startReplica(t, bucket, email)

	fetched := runTask(tenantCtx(tenant), a.byType[registry.TypeStorage], "", fetchInput("invoice-42.pdf"), maxAttempts)
	require.NoError(t, last(fetched).err)
	ref := last(fetched).out["contentRef"].(string)

	sent := runTask(tenantCtx(tenant), b.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{ref}, "messageKey", "invoice-42"), maxAttempts)
	require.Len(t, sent, 2, "throttled once, then accepted")
	assert.Equal(t, "transient, not delivered", sent[0].decision.Rule)
	require.NoError(t, last(sent).err)

	msgs := email.sent()
	require.Len(t, msgs, 1, "delivered exactly once")
	assert.Equal(t, invoice, msgs[0].Attachments[0].Content)
	in := intent(t, p, tenant, "invoice-42")
	assert.Equal(t, sendintent.StatusAccepted, in.Status)
	assert.Equal(t, 2, in.Attempts)

	vk := redis.NewClient(&redis.Options{Addr: p.valkeyAddr})
	defer func() { _ = vk.Close() }()
	fields, err := vk.HKeys(context.Background(), valkeystore.Key(ref)).Result()
	require.NoError(t, err)
	assert.ElementsMatch(t, valkeystore.Fields, fields, "Valkey holds the reference only, never the bytes")
}

// IT-02: the document's S3 object is deleted between the two tasks — the
// send fails permanently (source missing), once, and nothing is sent.
func TestIT02_SourceDeletedBetweenTasks_PermanentNothingSent(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	tenant := "tenant-" + uuid.NewString()
	email := &scriptedEmail{steps: []emailStep{{}}}
	r := p.startReplica(t, tenantBucketWith(t, "contract.pdf", []byte("contract")), email)

	ref := last(runTask(tenantCtx(tenant), r.byType[registry.TypeStorage], "", fetchInput("contract.pdf"), maxAttempts)).out["contentRef"].(string)
	meta, err := r.docRefs.Lookup(context.Background(), tenant, ref)
	require.NoError(t, err)
	deleteObject(t, meta.Bucket, meta.ObjectKey)

	sent := runTask(tenantCtx(tenant), r.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{ref}), maxAttempts)
	require.Len(t, sent, 1)
	assert.ErrorIs(t, last(sent).err, docref.ErrSourceMissing)
	assert.Equal(t, connectors.ClassPermanent, last(sent).decision.Class)
	assert.Empty(t, email.sent())
}

// IT-03: the S3 object is overwritten after the ref was created — integrity
// violation, permanent, nothing sent: altered content is never delivered.
func TestIT03_ObjectOverwritten_IntegrityViolation(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	tenant := "tenant-" + uuid.NewString()
	email := &scriptedEmail{steps: []emailStep{{}}}
	r := p.startReplica(t, tenantBucketWith(t, "terms.pdf", []byte("approved terms")), email)

	ref := last(runTask(tenantCtx(tenant), r.byType[registry.TypeStorage], "", fetchInput("terms.pdf"), maxAttempts)).out["contentRef"].(string)
	meta, err := r.docRefs.Lookup(context.Background(), tenant, ref)
	require.NoError(t, err)
	overwriteObject(t, meta.Bucket, meta.ObjectKey, []byte("altered terms!"))

	sent := runTask(tenantCtx(tenant), r.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{ref}), maxAttempts)
	require.Len(t, sent, 1)
	assert.ErrorIs(t, last(sent).err, docref.ErrIntegrityViolation)
	assert.Empty(t, email.sent())
}

// IT-04: the provider accepts the email but the response is lost — unknown,
// not retried; the workflow re-running the task is refused as a duplicate
// (the intent is in PostgreSQL, so any replica refuses it).
func TestIT04_LostResponse_NotRetried_DuplicateRefusedOnAnotherReplica(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	tenant := "tenant-" + uuid.NewString()
	email := &scriptedEmail{steps: []emailStep{
		{err: sendemail.ClassifyTransport("sendgrid", fmt.Errorf("read: %w", context.DeadlineExceeded)), delivered: true},
	}}
	bucket := tenantBucketWith(t, "x.pdf", []byte("x"))
	a, b := p.startReplica(t, bucket, email), p.startReplica(t, bucket, email)

	first := runTask(tenantCtx(tenant), a.byType[registry.TypeSendEmail], "", emailInput("messageKey", "welcome-7"), maxAttempts)
	require.Len(t, first, 1)
	assert.Equal(t, connectors.ClassUnknown, last(first).decision.Class)
	assert.Equal(t, sendintent.StatusUnknown, intent(t, p, tenant, "welcome-7").Status)

	again := runTask(tenantCtx(tenant), b.byType[registry.TypeSendEmail], "", emailInput("messageKey", "welcome-7"), maxAttempts)
	require.Len(t, again, 1)
	assert.ErrorIs(t, last(again).err, sendintent.ErrDuplicateRequest)
	assert.Len(t, email.sent(), 1, "the recipient got it once")
}

// IT-05: a reference past its TTL — Valkey expired it; not found, permanent.
func TestIT05_ExpiredReference_NotFoundPermanent(t *testing.T) {
	t.Parallel()
	p := newPlatform(t)
	p.refTTL = 300 * time.Millisecond
	tenant := "tenant-" + uuid.NewString()
	email := &scriptedEmail{steps: []emailStep{{}}}
	r := p.startReplica(t, tenantBucketWith(t, "q.pdf", []byte("quote")), email)

	ref := last(runTask(tenantCtx(tenant), r.byType[registry.TypeStorage], "", fetchInput("q.pdf"), maxAttempts)).out["contentRef"].(string)
	time.Sleep(600 * time.Millisecond)

	sent := runTask(tenantCtx(tenant), r.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{ref}), maxAttempts)
	require.Len(t, sent, 1)
	assert.ErrorIs(t, last(sent).err, docref.ErrNotFound)
	assert.Equal(t, connectors.ClassPermanent, last(sent).decision.Class)
}

// IT-06: an internal service is briefly unavailable — a GET recovers on the
// third attempt; a POST is never retried, since it may have taken effect.
func TestIT06_RestCallRecoversForGET_NotForPOST(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	newConn := func(method string) connectors.Connector {
		byType, err := connectors.New(connectors.Config{
			InternalToken: "test-token",
			Aliases:       aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "svc", Method: method, BaseURL: srv.URL, PathTemplate: "/x"}}},
		})
		require.NoError(t, err)
		return byType[registry.TypeRestCall]
	}
	ctx := connectors.WithDepartments(context.Background(), []string{"dept:reviewer"})

	get := runTask(ctx, newConn(http.MethodGet), http.MethodGet, map[string]any{"endpointAlias": "svc"}, maxAttempts)
	require.Len(t, get, 3)
	assert.NoError(t, last(get).err)

	calls.Store(0)
	post := runTask(ctx, newConn(http.MethodPost), http.MethodPost, map[string]any{"endpointAlias": "svc"}, maxAttempts)
	require.Len(t, post, 1)
	assert.Equal(t, "non-idempotent method", last(post).decision.Rule)
	assert.Equal(t, int32(1), calls.Load())
}
