package connectors_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// failingBucket is a storage provider whose every call fails with err.
type failingBucket struct {
	*storage.MockStorageClient
	err error
}

func (f failingBucket) Fetch(context.Context, string, string, int64) ([]byte, string, error) {
	return nil, "", f.err
}

// fetchError runs a storage fetch against a provider that fails with err.
func fetchError(t *testing.T, providerErr error) error {
	t.Helper()
	cfg := validConfig()
	cfg.StorageProviders = map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			return failingBucket{storage.NewMockStorageClient(), providerErr}, nil
		},
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	_, err = all[registry.TypeStorage].Execute(context.Background(), map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "k",
	})
	require.Error(t, err)
	return err
}

// apiErr has the shape of the AWS SDK's generic API error (smithy), which is
// what S3 returns for codes without a typed error, such as SlowDown.
type apiErr struct{ code string }

func (e apiErr) Error() string     { return "api error " + e.code }
func (e apiErr) ErrorCode() string { return e.code }

// 1. S3 NoSuchKey => permanent: never retried.
func TestRetry_S3NoSuchKey_IsPermanent(t *testing.T) {
	t.Parallel()
	err := fetchError(t, &types.NoSuchKey{Message: aws.String("The specified key does not exist.")})

	d := connectors.DecideRetry(registry.TypeStorage, err, "")
	assert.Equal(t, connectors.ClassPermanent, d.Class)
	assert.Equal(t, "api NoSuchKey", d.Reason)
	assert.False(t, d.Retry)
	assert.Equal(t, "permanent", d.Rule)
}

// 2. S3 SlowDown => transient: retried (storage is safe).
func TestRetry_S3SlowDown_IsTransient(t *testing.T) {
	t.Parallel()
	err := fetchError(t, apiErr{"SlowDown"})

	d := connectors.DecideRetry(registry.TypeStorage, err, "")
	assert.Equal(t, connectors.ClassTransient, d.Class)
	assert.True(t, d.Retry)
	assert.True(t, connectors.IsTransient(err))
}

// 3. Email connection timeout => transient, and — since the connection never
// opened, nothing was delivered — retried without risking a duplicate.
func TestRetry_EmailConnectTimeout_IsTransientAndRetried(t *testing.T) {
	t.Parallel()
	_, dialErr := (&net.Dialer{Timeout: time.Nanosecond}).Dial("tcp", "10.255.255.1:443")
	require.Error(t, dialErr)
	err := sendemail.ClassifyTransport("sendgrid", dialErr)

	d := connectors.DecideRetry(registry.TypeSendEmail, err, "")
	assert.Equal(t, connectors.ClassTransient, d.Class, d.Reason)
	assert.True(t, errors.Is(err, connectors.ErrNotDelivered))
	assert.True(t, d.Retry)
	assert.Equal(t, "transient, not delivered", d.Rule)

	dns := sendemail.ClassifyTransport("sendgrid", &net.DNSError{Err: "server misbehaving", Name: "api.sendgrid.com"})
	assert.True(t, connectors.AutoRetryAllowed(registry.TypeSendEmail, dns, ""), "DNS failure: transient, not delivered")
	throttled := sendemail.ClassifyStatus("sendgrid", http.StatusTooManyRequests, errors.New("rate limited"))
	assert.True(t, connectors.AutoRetryAllowed(registry.TypeSendEmail, throttled, ""), "429: transient, not accepted")
}

// 4. Email invalid recipient => permanent.
func TestRetry_EmailInvalidRecipient_IsPermanent(t *testing.T) {
	t.Parallel()
	err := sendemail.ClassifyStatus("aws-ses", http.StatusBadRequest, errors.New("MessageRejected: Email address is not verified"))

	d := connectors.DecideRetry(registry.TypeSendEmail, err, "")
	assert.Equal(t, connectors.ClassPermanent, d.Class)
	assert.False(t, d.Retry)
	// A provider error code beats the bare status: SES throttling with a 400
	// is still transient, and nothing was accepted.
	throttled := sendemail.ClassifyStatus("aws-ses", http.StatusBadRequest, apiErr{"Throttling"})
	assert.True(t, connectors.AutoRetryAllowed(registry.TypeSendEmail, throttled, ""))
}

// A send that may have reached the provider is never retried, even though a
// timeout or 503 would be transient anywhere else.
func TestRetry_EmailWithUnknownOutcome_IsNeverRetried(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"503":                   sendemail.ClassifyStatus("sendgrid", http.StatusServiceUnavailable, errors.New("unavailable")),
		"timeout after sending": sendemail.ClassifyTransport("sendgrid", fmt.Errorf("read: %w", context.DeadlineExceeded)),
	} {
		d := connectors.DecideRetry(registry.TypeSendEmail, err, "")
		assert.Equalf(t, connectors.ClassUnknown, d.Class, name)
		assert.Falsef(t, d.Retry, name)
		assert.True(t, errors.Is(err, connectors.ErrDeliveryUnknown))
	}
}

func restCallStatus(t *testing.T, status int, method string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	cfg := validConfig()
	cfg.Aliases = aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{Alias: "svc", Method: method, BaseURL: srv.URL, PathTemplate: "/x"}}}
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	_, err = all[registry.TypeRestCall].Execute(connectors.WithDepartments(context.Background(), []string{"d:r"}), map[string]any{"endpointAlias": "svc"})
	require.Error(t, err)
	return err
}

// 5. HTTP 503 => transient: retried for an idempotent method only.
func TestRetry_HTTP503_IsTransient(t *testing.T) {
	t.Parallel()
	get := restCallStatus(t, http.StatusServiceUnavailable, "GET")
	d := connectors.DecideRetry(registry.TypeRestCall, get, "GET")
	assert.Equal(t, connectors.ClassTransient, d.Class)
	assert.Equal(t, "http 503", d.Reason)
	assert.True(t, d.Retry)

	post := restCallStatus(t, http.StatusServiceUnavailable, "POST")
	d = connectors.DecideRetry(registry.TypeRestCall, post, "POST")
	assert.Equal(t, connectors.ClassTransient, d.Class)
	assert.False(t, d.Retry, "a POST may have taken effect")
	assert.Equal(t, "non-idempotent method", d.Rule)
}

// 6. HTTP 404 => permanent.
func TestRetry_HTTP404_IsPermanent(t *testing.T) {
	t.Parallel()
	err := restCallStatus(t, http.StatusNotFound, "GET")
	d := connectors.DecideRetry(registry.TypeRestCall, err, "GET")
	assert.Equal(t, connectors.ClassPermanent, d.Class)
	assert.False(t, d.Retry)
}

func TestRetry_HTTPStatusTable_ThroughRestCall(t *testing.T) {
	t.Parallel()
	for _, status := range []int{408, 429, 500, 502, 503, 504, 400, 401, 403, 404} {
		err := restCallStatus(t, status, "GET")
		want := connectors.ClassPermanent
		if status >= 500 || status == 408 || status == 429 {
			want = connectors.ClassTransient
		}
		class, reason := connectors.ClassOf(err)
		assert.Equal(t, want, class, strconv.Itoa(status))
		assert.Equal(t, "http "+strconv.Itoa(status), reason)
	}
}

// 7. Unknown error path: an error the library cannot classify is reported as
// unknown and is not retried, whatever the connector's policy.
func TestRetry_UnclassifiedError_IsUnknownAndNotRetried(t *testing.T) {
	t.Parallel()
	err := fetchError(t, errors.New("provider returned something new"))

	d := connectors.DecideRetry(registry.TypeStorage, err, "")
	assert.Equal(t, connectors.ClassUnknown, d.Class)
	assert.Equal(t, "unclassified", d.Reason)
	assert.False(t, d.Retry, "unknown is treated conservatively: no automatic retry")
	assert.Equal(t, "unknown", d.Rule)
	assert.True(t, errors.Is(err, connectors.ErrUpstream), "still an upstream failure — just not a retryable one")
}

// slowContent is a document content store whose object read times out.
type slowContent struct{ *docref.MemoryContent }

type timingOut struct{}

func (timingOut) Read([]byte) (int, error) { return 0, &net.OpError{Op: "read", Err: timeoutErr{}} }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func (s slowContent) Open(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	_, size, err := s.MemoryContent.Open(ctx, bucket, key)
	return io.NopCloser(timingOut{}), size, err
}

// Document resolution: missing ref, missing object and checksum mismatch are
// permanent; a network timeout reading the object is transient.
func TestRetry_DocumentResolution(t *testing.T) {
	t.Parallel()
	ctx := connectors.WithTenant(context.Background(), "tenant-1")

	upload := func(docRefs *docref.Service, ref string) error {
		cfg := validConfig()
		cfg.DocRefs = docRefs
		cfg.StorageProviders = map[string]storage.ProviderConstructor{
			"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
				return storage.NewMockStorageClient(), nil
			},
		}
		all, err := connectors.New(cfg)
		require.NoError(t, err)
		_, err = all[registry.TypeStorage].Execute(ctx, map[string]any{
			"operation": "upload", "provider": "aws-s3", "bucket": "b", "key": "out", "content": ref,
		})
		require.Error(t, err)
		return err
	}

	content := docref.NewMemoryContent("docs")
	svc := docref.NewService(docref.NewMemoryStore(), content)
	missingObject, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("x"))
	require.NoError(t, err)
	require.NoError(t, content.Delete(ctx, missingObject.Bucket, missingObject.ObjectKey))
	altered, err := svc.Create(ctx, "tenant-1", "text/plain", []byte("abc"))
	require.NoError(t, err)
	require.NoError(t, content.Put(ctx, altered.ObjectKey, []byte("xyz"), "text/plain"))

	for name, err := range map[string]error{
		"missing reference": upload(svc, docref.NewID()),
		"missing object":    upload(svc, missingObject.ID),
		"checksum mismatch": upload(svc, altered.ID),
	} {
		d := connectors.DecideRetry(registry.TypeStorage, err, "")
		assert.Equalf(t, connectors.ClassPermanent, d.Class, name)
		assert.Falsef(t, d.Retry, name)
	}

	slow := docref.NewService(docref.NewMemoryStore(), slowContent{docref.NewMemoryContent("docs")})
	ref, err := slow.Create(ctx, "tenant-1", "text/plain", []byte("x"))
	require.NoError(t, err)
	d := connectors.DecideRetry(registry.TypeStorage, upload(slow, ref.ID), "")
	assert.Equal(t, connectors.ClassTransient, d.Class, d.Reason)
	assert.Equal(t, "timeout", d.Reason)
	assert.True(t, d.Retry)
}

func TestRetry_PolicyRules(t *testing.T) {
	t.Parallel()
	transient := fmt.Errorf("%w: %w", connectors.ErrUpstream, shared.HTTPStatusError(503, errors.New("provider 503")))

	assert.True(t, connectors.AutoRetryAllowed(registry.TypeStorage, &documents.InProgressError{DocumentID: "d"}, ""), "an in-progress Drive document is retried later")
	assert.False(t, connectors.AutoRetryAllowed(registry.TypeStorage, connectors.ErrValidation, ""))
	d := connectors.DecideRetry(registry.TypeChatNotify, transient, "")
	assert.False(t, d.Retry)
	assert.Equal(t, "policy unsafe", d.Rule)
	assert.False(t, connectors.AutoRetryAllowed("unknown-type", transient, ""))
	assert.False(t, connectors.AutoRetryAllowed(registry.TypeStorage, nil, ""), "nothing to retry")
}

func TestRetryDecision_LogAttrs(t *testing.T) {
	t.Parallel()
	d := connectors.DecideRetry(registry.TypeStorage, fetchError(t, apiErr{"SlowDown"}), "")
	got := map[string]string{}
	for _, a := range d.LogAttrs() {
		got[a.Key] = a.Value.String()
	}
	assert.Equal(t, map[string]string{
		"retry": "true", "error_class": "transient", "error_reason": "api SlowDown",
		"retry_policy": "safe", "retry_rule": "transient",
	}, got)
}

// A Valkey that is full (noeviction) or cannot confirm a write in its AOF is
// temporarily unavailable: storage retries, and the retry mints a new ref.
func TestRetry_DocRefStoreUnavailable_IsTransient(t *testing.T) {
	t.Parallel()
	refs := docref.NewMemoryStore()
	refs.FailPut(fmt.Errorf("%w: OOM command not allowed when used memory > 'maxmemory'", docref.ErrUnavailable))
	cfg := validConfig()
	cfg.DocRefs = docref.NewService(refs, docref.NewMemoryContent("docs"))
	cfg.StorageProviders = map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			c := storage.NewMockStorageClient()
			require.NoError(t, c.Upload(context.Background(), "b", "k", []byte("x"), "text/plain"))
			return c, nil
		},
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	_, err = all[registry.TypeStorage].Execute(connectors.WithTenant(context.Background(), "t"), map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "k", "createDocument": true,
	})
	require.Error(t, err)
	d := connectors.DecideRetry(registry.TypeStorage, err, "")
	assert.Equal(t, connectors.ClassTransient, d.Class)
	assert.True(t, d.Retry)
}
