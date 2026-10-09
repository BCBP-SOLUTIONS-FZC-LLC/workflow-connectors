// Worker scenarios: what the connector worker's retry loop actually does with
// each kind of failure, end to end through connectors.New — how many times a
// task runs, what reaches the provider, and what the send intent records.
package unit_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

const maxAttempts = 5

// apiErr has the shape of the AWS SDK's generic API error.
type apiErr struct{ code string }

func (e apiErr) Error() string     { return "api error " + e.code }
func (e apiErr) ErrorCode() string { return e.code }

func dialTimeout(t *testing.T) error {
	t.Helper()
	_, err := (&net.Dialer{Timeout: time.Nanosecond}).Dial("tcp", "10.255.255.1:443")
	require.Error(t, err)
	return err
}

// WS-01: throttled once, then accepted — the worker retries and the email is
// delivered exactly once; the send intent records both attempts.
func TestWorker_EmailThrottledThenAccepted_RetriedOnce(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{
		{err: sendemail.ClassifyStatus("sendgrid", http.StatusTooManyRequests, errors.New("rate limited"))},
		{},
	}}
	w := newWorld(t, email)

	attempts := runTask(tenantCtx(), w.byType[registry.TypeSendEmail], "", emailInput("messageKey", "invoice-42"), maxAttempts)

	require.Len(t, attempts, 2)
	assert.True(t, attempts[0].decision.Retry)
	assert.Equal(t, "transient, not delivered", attempts[0].decision.Rule)
	assert.NoError(t, last(attempts).err)
	assert.Equal(t, 1, email.deliveredCount(), "delivered exactly once")
	intent, ok, _ := w.intents.Get(context.Background(), "tenant-1", "invoice-42")
	require.True(t, ok)
	assert.Equal(t, sendintent.StatusAccepted, intent.Status)
	assert.Equal(t, 2, intent.Attempts)
}

// WS-02: the provider is unreachable on every attempt — the worker stops at
// its attempt limit; nothing was ever delivered.
func TestWorker_EmailPersistentConnectFailure_StopsAtAttemptLimit(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{{err: sendemail.ClassifyTransport("sendgrid", dialTimeout(t))}}}
	w := newWorld(t, email)

	attempts := runTask(tenantCtx(), w.byType[registry.TypeSendEmail], "", emailInput(), maxAttempts)

	assert.Len(t, attempts, maxAttempts, "transient retries are bounded by the worker, not infinite")
	assert.Equal(t, connectors.ClassTransient, last(attempts).decision.Class)
	assert.Zero(t, email.deliveredCount())
}

// WS-03: the provider accepts the email but the response is lost — unknown,
// never retried; running the task again is refused as a duplicate request.
func TestWorker_EmailLostResponse_NotRetried_DuplicateRefused(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{
		{err: sendemail.ClassifyTransport("sendgrid", fmt.Errorf("read: %w", context.DeadlineExceeded)), delivered: true},
	}}
	w := newWorld(t, email)
	conn := w.byType[registry.TypeSendEmail]

	attempts := runTask(tenantCtx(), conn, "", emailInput("messageKey", "welcome-7"), maxAttempts)
	require.Len(t, attempts, 1)
	assert.Equal(t, connectors.ClassUnknown, last(attempts).decision.Class)
	assert.Equal(t, "unknown", last(attempts).out["deliveryOutcome"])

	again := runTask(tenantCtx(), conn, "", emailInput("messageKey", "welcome-7"), maxAttempts)
	require.Len(t, again, 1)
	assert.ErrorIs(t, last(again).err, sendintent.ErrDuplicateRequest)
	assert.Equal(t, connectors.ClassPermanent, last(again).decision.Class)
	assert.Equal(t, 1, email.deliveredCount(), "no duplicate reached the recipient")
}

// WS-04: an invalid recipient is rejected — permanent, one attempt.
func TestWorker_EmailInvalidRecipient_OneAttempt(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{
		{err: sendemail.ClassifyStatus("aws-ses", http.StatusBadRequest, errors.New("MessageRejected: address not verified"))},
	}}
	w := newWorld(t, email)

	attempts := runTask(tenantCtx(), w.byType[registry.TypeSendEmail], "", emailInput(), maxAttempts)
	require.Len(t, attempts, 1)
	assert.Equal(t, connectors.ClassPermanent, last(attempts).decision.Class)
}

// WS-05: after an unknown outcome an operator resends explicitly — the
// documented duplicate risk is real and recorded on the intent.
func TestWorker_ExplicitResendAfterUnknown_IsCounted(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{
		{err: sendemail.ClassifyStatus("sendgrid", http.StatusServiceUnavailable, errors.New("503")), delivered: true},
		{},
	}}
	w := newWorld(t, email)
	conn := w.byType[registry.TypeSendEmail]

	runTask(tenantCtx(), conn, "", emailInput("messageKey", "k"), maxAttempts)
	resent := runTask(tenantCtx(), conn, "", emailInput("messageKey", "k", "resend", true, "resendAttempt", 1), maxAttempts)

	assert.NoError(t, last(resent).err)
	intent, _, _ := w.intents.Get(context.Background(), "tenant-1", "k")
	assert.Equal(t, 2, intent.Attempts)
	assert.Equal(t, 2, email.deliveredCount(), "an explicit resend after an unknown outcome may duplicate")
}

// WS-06: S3 throttles twice, then serves the object — storage retries.
func TestWorker_StorageSlowDownThenSuccess_Retried(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil, apiErr{"SlowDown"}, apiErr{"SlowDown"})
	require.NoError(t, w.bucket.Upload(context.Background(), "b", "invoice.pdf", []byte("%PDF"), "application/pdf"))

	attempts := runTask(tenantCtx(), w.byType[registry.TypeStorage], "", map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "invoice.pdf",
	}, maxAttempts)

	require.Len(t, attempts, 3)
	assert.Equal(t, "api SlowDown", attempts[0].decision.Reason)
	assert.NoError(t, last(attempts).err)
	assert.Equal(t, "%PDF", last(attempts).out["content"])
}

// WS-07: S3 NoSuchKey — permanent, one attempt.
func TestWorker_StorageNoSuchKey_OneAttempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil, &types.NoSuchKey{Message: aws.String("missing")})

	attempts := runTask(tenantCtx(), w.byType[registry.TypeStorage], "", map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "missing.pdf",
	}, maxAttempts)

	require.Len(t, attempts, 1)
	assert.Equal(t, connectors.ClassPermanent, last(attempts).decision.Class)
}

// WS-08: a document ref whose S3 object was overwritten — the attachment
// fails integrity, permanently, before anything is sent.
func TestWorker_AttachmentIntegrityViolation_OneAttemptNothingSent(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	ref, err := w.docRefs.Create(tenantCtx(), "tenant-1", "application/pdf", []byte("approved contract"))
	require.NoError(t, err)
	require.NoError(t, w.content.Put(context.Background(), ref.ObjectKey, []byte("altered contract!"), "application/pdf"))

	attempts := runTask(tenantCtx(), w.byType[registry.TypeSendEmail], "", emailInput("attachments", []any{ref.ID}), maxAttempts)

	require.Len(t, attempts, 1)
	assert.ErrorIs(t, last(attempts).err, docref.ErrIntegrityViolation)
	assert.Equal(t, connectors.ClassPermanent, last(attempts).decision.Class)
	assert.Zero(t, w.email.deliveredCount())
}

type unrecordableIntents struct{ *sendintent.MemoryStore }

func (unrecordableIntents) Record(context.Context, string, int, sendintent.Status, string, string) error {
	return errors.New("database unavailable")
}

// WS-09: the send was throttled (nothing delivered) but its outcome could not
// be recorded, leaving the intent pending — an automatic retry would be
// refused as a duplicate, so none is attempted and the error says why.
func TestWorker_UnrecordedIntent_NotRetried(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{
		{err: sendemail.ClassifyStatus("sendgrid", http.StatusTooManyRequests, errors.New("rate limited"))},
	}}
	w := newWorld(t, email)
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return email, nil },
		},
		DocRefs:     w.docRefs,
		SendIntents: unrecordableIntents{sendintent.NewMemoryStore()},
	})
	require.NoError(t, err)

	attempts := runTask(tenantCtx(), all[registry.TypeSendEmail], "", emailInput("messageKey", "k"), maxAttempts)
	require.Len(t, attempts, 1)
	d := last(attempts).decision
	assert.False(t, d.Retry)
	assert.Equal(t, connectors.ClassUnknown, d.Class)
	assert.Contains(t, d.Reason, "send intent not recorded")
	assert.Contains(t, last(attempts).out["sendIntentWarning"], "outcome not recorded")
}
