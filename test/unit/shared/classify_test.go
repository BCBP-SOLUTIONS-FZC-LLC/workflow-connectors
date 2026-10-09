package shared_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestClassifyHTTPStatus_Table(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]shared.Class{
		408: shared.ClassTransient, 425: shared.ClassTransient, 429: shared.ClassTransient,
		500: shared.ClassTransient, 502: shared.ClassTransient, 503: shared.ClassTransient, 504: shared.ClassTransient,
		400: shared.ClassPermanent, 401: shared.ClassPermanent, 403: shared.ClassPermanent, 404: shared.ClassPermanent,
		409: shared.ClassPermanent, 413: shared.ClassPermanent, 422: shared.ClassPermanent,
		301: shared.ClassPermanent, 302: shared.ClassPermanent,
		501: shared.ClassUnknown, 505: shared.ClassUnknown, 507: shared.ClassUnknown,
	} {
		assert.Equalf(t, want, shared.ClassifyHTTPStatus(status), "status %d", status)
	}
}

// codedErr has the shape of an AWS SDK API error (smithy.APIError).
type codedErr struct{ code string }

func (e codedErr) Error() string     { return "api error " + e.code }
func (e codedErr) ErrorCode() string { return e.code }

func TestClassifyCause_S3ErrorCodes(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]shared.Class{
		"NoSuchKey":          shared.ClassPermanent,
		"AccessDenied":       shared.ClassPermanent,
		"InvalidBucketName":  shared.ClassPermanent,
		"RequestTimeout":     shared.ClassTransient,
		"SlowDown":           shared.ClassTransient,
		"InternalError":      shared.ClassTransient,
		"ServiceUnavailable": shared.ClassTransient,
		"SomethingNew":       shared.ClassUnknown,
	} {
		err := fmt.Errorf("wrapped: %w", codedErr{code})
		class, reason := shared.ClassifyCause(err)
		assert.Equalf(t, want, class, "code %s", code)
		if want != shared.ClassUnknown {
			assert.Equal(t, "api "+code, reason)
		}
	}
}

func TestClassifyCause_RealNetworkFailures(t *testing.T) {
	t.Parallel()

	// Connection refused: a port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	_, err = net.Dial("tcp", addr)
	require.Error(t, err)
	class, reason := shared.ClassifyCause(err)
	assert.Equal(t, shared.ClassTransient, class, "refused: %v", err)
	assert.Equal(t, "connection refused", reason)

	// Connection timeout: a dial that cannot complete in time.
	_, err = (&net.Dialer{Timeout: time.Nanosecond}).Dial("tcp", "10.255.255.1:25")
	require.Error(t, err)
	class, _ = shared.ClassifyCause(err)
	assert.Equal(t, shared.ClassTransient, class, "dial timeout: %v", err)

	// DNS failure.
	class, reason = shared.ClassifyCause(&net.DNSError{Err: "server misbehaving", Name: "api.example", IsTemporary: true})
	assert.Equal(t, shared.ClassTransient, class)
	assert.Equal(t, "dns", reason)

	for name, err := range map[string]error{
		"deadline":   fmt.Errorf("get: %w", context.DeadlineExceeded),
		"reset":      &net.OpError{Op: "read", Err: syscall.ECONNRESET},
		"short read": io.ErrUnexpectedEOF,
	} {
		class, _ := shared.ClassifyCause(err)
		assert.Equalf(t, shared.ClassTransient, class, name)
	}
}

// 7. The unknown path, as documented: unrecognised errors and cancellation
// are unknown, and unknown is never retryable.
func TestClassOf_UnknownIsNeverRetryable(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"plain error":      errors.New("something odd"),
		"upstream only":    fmt.Errorf("%w: provider said no", shared.ErrUpstream),
		"cancelled":        shared.Classify("op", context.Canceled),
		"unrecognised 5xx": shared.HTTPStatusError(507, errors.New("insufficient storage")),
	} {
		class, _ := shared.ClassOf(err)
		assert.Equalf(t, shared.ClassUnknown, class, name)
		assert.Falsef(t, shared.IsRetryable(err), name)
		assert.Falsef(t, shared.IsPermanent(err), name)
	}
}

func TestClassOf_InputErrorsArePermanent(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		fmt.Errorf("%w: bucket is required", shared.ErrValidation),
		shared.ErrMissingTenant,
		shared.ErrMissingInternalAuth,
		// Even if something transient is underneath, invalid input stays permanent.
		fmt.Errorf("%w: %w", shared.ErrValidation, shared.Transient("timeout", context.DeadlineExceeded)),
	} {
		assert.True(t, shared.IsPermanent(err), "%v", err)
	}
}

func TestClassify_KeepsAttachedClassAndDerivesTheRest(t *testing.T) {
	t.Parallel()

	permanent := shared.Classify("op", shared.Permanent("drive file not found", errors.New("no file")))
	assert.True(t, errors.Is(permanent, shared.ErrUpstream))
	assert.True(t, shared.IsPermanent(permanent))
	_, reason := shared.ClassOf(permanent)
	assert.Equal(t, "drive file not found", reason)

	derived := shared.Classify("op", fmt.Errorf("get: %w", codedErr{"SlowDown"}))
	assert.True(t, shared.IsTransient(derived))
	assert.True(t, shared.IsRetryable(derived))

	validation := shared.Classify("op", fmt.Errorf("%w: missing key", shared.ErrValidation))
	assert.False(t, errors.Is(validation, shared.ErrUpstream))
	assert.True(t, shared.IsPermanent(validation))
}

func TestClassOf_IsDeterministic(t *testing.T) {
	t.Parallel()
	err := shared.Classify("op", fmt.Errorf("x: %w", codedErr{"SlowDown"}))
	first, firstReason := shared.ClassOf(err)
	for range 100 {
		class, reason := shared.ClassOf(err)
		require.Equal(t, first, class)
		require.Equal(t, firstReason, reason)
	}
}

// Provider clients never negotiate HTTP/2, whose client may replay a request
// body after a PROTOCOL_ERROR reset (a duplicate email).
func TestProviderHTTPClient_IsHTTP1Only(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Proto))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := shared.ProviderHTTPClient()
	client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, "HTTP/1.1", resp.Proto)
}
