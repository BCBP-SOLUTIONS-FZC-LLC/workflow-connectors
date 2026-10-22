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
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
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

// WS-12 (a google-drive fetch after a crashed upload whose lease expired)
// needs the Drive adapter's unexported fake and lives in
// pkg/connectors/storage/googledrive/facade_test.go.

// runRestCallTask is runTask for rest-call as the worker runs it: the method
// passed to DecideRetry is the "method" field of Execute's output (set once
// the request was sent), not re-resolved from the alias.
func runRestCallTask(ctx context.Context, conn connectors.Connector, input map[string]any, maxAttempts int) []attempt {
	var attempts []attempt
	for range maxAttempts {
		out, err := conn.Execute(ctx, input)
		a := attempt{out: out, err: err}
		if err != nil {
			method, _ := out["method"].(string)
			a.decision = connectors.DecideRetry(conn.Type(), err, method)
		}
		attempts = append(attempts, a)
		if err == nil || !a.decision.Retry {
			break
		}
	}
	return attempts
}

// flakyService answers 503 to its first `failures` requests, then 200.
func flakyService(t *testing.T, failures int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) <= failures {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// WS-10: the worker decides a rest-call retry from the output's "method": a
// 503 on a GET alias is retried until it succeeds; the same 503 on a POST
// alias is not (the service may have acted on it).
func TestWorker_RestCall503_DecidedByOutputMethod(t *testing.T) {
	t.Parallel()
	ctx := connectors.WithActor(connectors.WithTenant(context.Background(), "tenant-1"), "principal-sub")
	for method, want := range map[string]struct {
		attempts int
		ok       bool
		rule     string
	}{
		http.MethodGet:  {attempts: 3, ok: true, rule: "transient"},
		http.MethodPost: {attempts: 1, ok: false, rule: "non-idempotent method"},
	} {
		srv, hits := flakyService(t, 2)
		all, err := connectors.New(connectors.Config{
			InternalToken: "test-token",
			Aliases:       aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "svc", Method: method, BaseURL: srv.URL, PathTemplate: "/x"}}},
		})
		require.NoError(t, err)

		attempts := runRestCallTask(ctx, all[registry.TypeRestCall], map[string]any{"endpointAlias": "svc"}, maxAttempts)

		require.Len(t, attempts, want.attempts, method)
		assert.Equal(t, method, attempts[0].out["method"], method)
		assert.Equal(t, http.StatusServiceUnavailable, attempts[0].out["status"], method)
		assert.Equal(t, want.rule, attempts[0].decision.Rule, method)
		assert.Equal(t, want.ok, last(attempts).err == nil, method)
		assert.Equal(t, int32(want.attempts), hits.Load(), method)
	}
}

// lostReplyIntents loses Reserve's reply on its first call without
// reserving anything, so the read-back finds nothing and the release
// releases nothing; later calls work.
type lostReplyIntents struct {
	*sendintent.MemoryStore
	failed atomic.Bool
}

func (s *lostReplyIntents) Reserve(ctx context.Context, tenant, key string, resendFrom int, token string) (sendintent.Intent, bool, error) {
	if s.failed.CompareAndSwap(false, true) {
		return sendintent.Intent{}, false, fmt.Errorf("reserve: %w", dialTimeoutErr)
	}
	return s.MemoryStore.Reserve(ctx, tenant, key, resendFrom, token)
}

var dialTimeoutErr = &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}

// WS-11: a Reserve whose reply was lost cannot be proven unreserved: the
// reservation may surface pending later. The worker does not retry it, even
// though the error itself looks transient, and nothing is sent; the
// operator's re-run (after checking) sends exactly once.
func TestWorker_EmailAmbiguousReservation_NeverAutoRetried(t *testing.T) {
	t.Parallel()
	email := &scriptedEmail{steps: []emailStep{{}}}
	intents := &lostReplyIntents{MemoryStore: sendintent.NewMemoryStore()}
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return email, nil },
		},
		SendIntents: intents,
	})
	require.NoError(t, err)
	conn := all[registry.TypeSendEmail]

	attempts := runTask(tenantCtx(), conn, "", emailInput("messageKey", "invoice-42"), maxAttempts)
	require.Len(t, attempts, 1, "never retried automatically")
	d := last(attempts).decision
	assert.False(t, d.Retry)
	assert.Equal(t, connectors.ClassUnknown, d.Class)
	assert.Equal(t, "unknown", d.Rule)
	assert.Contains(t, d.Reason, "send intent not recorded")
	assert.Equal(t, "not_delivered", last(attempts).out["deliveryOutcome"])
	assert.Zero(t, email.deliveredCount())

	rerun := runTask(tenantCtx(), conn, "", emailInput("messageKey", "invoice-42"), maxAttempts)
	require.Len(t, rerun, 1)
	require.NoError(t, last(rerun).err)
	assert.Equal(t, 1, email.deliveredCount())
}
