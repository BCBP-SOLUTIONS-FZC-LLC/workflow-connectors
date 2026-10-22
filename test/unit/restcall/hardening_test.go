package restcall_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func aliasFor(baseURL, pathTemplate string) aliasconfig.Config {
	return aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "a", Method: http.MethodGet, BaseURL: baseURL, PathTemplate: pathTemplate},
	}}
}

func callerCtx() context.Context {
	return shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")
}

func TestRestCall_Redirect_NotFollowed_TokenNeverLeaves(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var leakedToken, leakedDepts string
	external := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		leakedToken = r.Header.Get(shared.InternalTokenHeader)
		leakedDepts = r.Header.Get(shared.UserIDHeader)
	}))
	defer external.Close()
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/steal", http.StatusFound)
	}))
	defer internal.Close()

	conn := restcall.New(aliasFor(internal.URL, "/x"), &http.Client{}, "secret-token")
	out, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "a"})

	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
	assert.Equal(t, http.StatusFound, out["status"])
	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, leakedToken, "x-internal-token must never reach another host")
	assert.Empty(t, leakedDepts)
}

func TestRestCall_NumericPathAndQueryParams(t *testing.T) {
	t.Parallel()

	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/applications/{id}"), nil, "t")
	_, err := conn.Execute(callerCtx(), map[string]any{
		"endpointAlias": "a",
		"pathParams":    map[string]any{"id": float64(1234567)},
		"queryParams":   map[string]any{"page": float64(3)},
	})
	require.NoError(t, err)
	assert.Equal(t, "/applications/1234567", gotPath)
	assert.Equal(t, "page=3", gotQuery)
}

func TestRestCall_UnknownAlias_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := restcall.New(aliasconfig.Config{}, nil, "t")
	_, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "missing"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
	assert.True(t, errors.Is(err, aliasconfig.ErrUnknownAlias))
}

func TestRestCall_TransportError_IsUpstream(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	conn := restcall.New(aliasFor(url, "/x"), nil, "t")
	_, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "a"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
}

// A 2xx whose body exceeds the cap was processed by the service: the call
// fails permanently ("response too large"), never as invalid input, and the
// output still reports the status, headers and method.
func TestRestCall_OversizedResponse_IsPermanentNotValidation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "r-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(make([]byte, shared.MaxResponseBytes+1))
	}))
	defer srv.Close()

	aliases := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "a", Method: "post", BaseURL: srv.URL, PathTemplate: "/x"},
	}}
	conn := restcall.New(aliases, nil, "t")
	out, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "a"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, shared.ErrValidation), "the input was valid")
	assert.ErrorIs(t, err, shared.ErrTooLarge)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	class, reason := shared.ClassOf(err)
	assert.Equal(t, shared.ClassPermanent, class)
	assert.Equal(t, restcall.ReasonResponseTooLarge, reason)

	require.NotNil(t, out)
	assert.Equal(t, http.StatusCreated, out["status"])
	assert.Equal(t, http.MethodPost, out["method"])
	assert.Equal(t, "r-1", out["headers"].(map[string]any)["X-Request-Id"])
	assert.Equal(t, true, out["bodyTruncated"])
	assert.Len(t, out["body"], int(shared.MaxResponseBytes))
}

// A body whose read fails keeps the status and method in the output; the
// read error is classified by its cause.
func TestRestCall_ResponseReadError_KeepsStatusAndMethod(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/x"), nil, "t")
	out, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "a"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.Equal(t, http.StatusOK, out["status"])
	assert.Equal(t, http.MethodGet, out["method"])
	assert.Nil(t, out["body"])
}

// Every result once the request was sent carries the method used: success,
// an HTTP error status and a transport error.
func TestRestCall_OutputCarriesMethod(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	aliases := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "ok", Method: "put", BaseURL: srv.URL, PathTemplate: "/ok"},
		{Alias: "fail", Method: "PATCH", BaseURL: srv.URL, PathTemplate: "/fail"},
	}}
	conn := restcall.New(aliases, nil, "t")

	out, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "ok"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, out["method"])

	out, err = conn.Execute(callerCtx(), map[string]any{"endpointAlias": "fail"})
	require.Error(t, err)
	assert.Equal(t, http.MethodPatch, out["method"])

	srv.Close() // connection refused from now on
	out, err = conn.Execute(callerCtx(), map[string]any{"endpointAlias": "ok"})
	require.Error(t, err)
	assert.Equal(t, map[string]any{"method": http.MethodPut}, out)
	assert.True(t, shared.IsTransient(err))
}

func TestRestCall_SetCookieNotReturned(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/x"), nil, "t")
	out, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "a"})
	require.NoError(t, err)
	assert.NotContains(t, out["headers"], "Set-Cookie")
}

// An alias timeout longer than the 30-second default is honoured: the
// default client carries no client-wide timeout that would cut it short.
func TestRestCall_AliasTimeoutLongerThanDefault_IsHonoured(t *testing.T) {
	t.Parallel()
	client := shared.InternalHTTPClient(nil)
	assert.Zero(t, client.Timeout, "no client-wide cap")
	assert.Equal(t, 2*time.Minute, shared.CallTimeout(2*time.Minute))
	assert.Equal(t, shared.DefaultHTTPTimeout, shared.CallTimeout(0), "an alias without a timeout gets the default deadline")
}

// Defence in depth for a Config that skipped Validate (restcall.New takes it
// as is): a "#" in the alias path is refused at call time, before any
// request, so a fixed query hidden behind it cannot be bypassed.
func TestRestCall_UnvalidatedFragmentAlias_IsRefused(t *testing.T) {
	t.Parallel()

	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	conn := restcall.New(aliasFor(srv.URL, "/users#?role=user"), nil, "t")
	_, err := conn.Execute(callerCtx(), map[string]any{
		"endpointAlias": "a",
		"queryParams":   map[string]any{"role": "admin"},
	})
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "fragment")
	assert.False(t, called.Load())
}
