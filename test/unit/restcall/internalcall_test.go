package restcall_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// countingServer counts the requests it receives.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// An unvalidated alias whose path does not start with "/" could move the
// call to another host ("http://svc" + "@evil/x" has host evil): refused
// before anything is sent.
func TestRestCall_PathCannotChangeHost(t *testing.T) {
	t.Parallel()
	evil, hits := countingServer(t)
	evilHost := strings.TrimPrefix(evil.URL, "http://")

	for _, tmpl := range []string{"@" + evilHost + "/steal", "{p}/steal"} {
		conn := restcall.New(aliasFor("http://svc.internal", tmpl), nil, "secret")
		_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a", "pathParams": map[string]any{"p": "@" + evilHost}})
		require.ErrorIs(t, err, shared.ErrValidation, tmpl)
		assert.True(t, shared.IsPermanent(err))
	}
	assert.Zero(t, hits.Load(), "nothing may reach another host")
}

// A path parameter of "." or ".." would move the call to another resource
// once the server normalises the path.
func TestRestCall_DotSegmentPathParam_IsRejected(t *testing.T) {
	t.Parallel()
	srv, hits := countingServer(t)
	conn := restcall.New(aliasFor(srv.URL, "/tenants/t1/items/{id}"), nil, "t")
	for _, v := range []string{".", "..", ""} {
		_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a", "pathParams": map[string]any{"id": v}})
		require.ErrorIs(t, err, shared.ErrValidation, "%q", v)
	}
	assert.Zero(t, hits.Load())
}

// A query parameter the alias's template fixes cannot be overridden.
func TestRestCall_QueryParamCannotOverrideTemplate(t *testing.T) {
	t.Parallel()
	var gotQuery atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { gotQuery.Store(r.URL.RawQuery) }))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/items?scope=own"), nil, "t")
	_, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a", "queryParams": map[string]any{"scope": "all"}})
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.Nil(t, gotQuery.Load(), "nothing sent")

	_, err = conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a", "queryParams": map[string]any{"page": 2.0}})
	require.NoError(t, err)
	assert.Equal(t, "page=2&scope=own", gotQuery.Load())
}

// The status alone classifies an error response: an oversized error page
// must not turn a transient 503 into a permanent validation error.
func TestRestCall_ErrorStatus_OversizedBody_ClassifiedByStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.Repeat("x", int(shared.MaxResponseBytes)+10)))
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/x"), nil, "t")
	out, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a"})
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.NotErrorIs(t, err, shared.ErrValidation)
	assert.True(t, shared.IsTransient(err))
	assert.Equal(t, http.StatusServiceUnavailable, out["status"])
	assert.Equal(t, true, out["bodyTruncated"])
	assert.Len(t, out["body"], int(shared.MaxResponseBytes))
}

// A cut-off error body is returned as far as it arrived; the status decides.
func TestRestCall_ErrorStatus_BodyReadFailure_ClassifiedByStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/x"), nil, "t")
	out, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a"})
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.True(t, shared.IsTransient(err))
	assert.Equal(t, "slow down", out["body"])
	assert.Equal(t, true, out["bodyTruncated"])
}

func TestRestCall_ErrorStatus_SmallBody_IsDecodedAndNotTruncated(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404}`))
	}))
	defer srv.Close()

	out, err := restcall.New(aliasFor(srv.URL, "/x"), nil, "t").Execute(deptCtx(), map[string]any{"endpointAlias": "a"})
	require.ErrorIs(t, err, shared.ErrUpstream)
	assert.True(t, shared.IsPermanent(err))
	assert.Equal(t, map[string]any{"code": json.Number("404")}, out["body"])
	assert.NotContains(t, out, "bodyTruncated")
}

// Integers beyond 2^53 keep every digit, and feed the next call unchanged.
func TestRestCall_LargeIntegers_StayExact(t *testing.T) {
	t.Parallel()
	var gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 9007199254740993}`))
	}))
	defer srv.Close()

	conn := restcall.New(aliasFor(srv.URL, "/items/{id}"), nil, "t")
	out, err := conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a", "pathParams": map[string]any{"id": "1"}})
	require.NoError(t, err)
	id := out["body"].(map[string]any)["id"]
	assert.Equal(t, json.Number("9007199254740993"), id)

	_, err = conn.Execute(deptCtx(), map[string]any{"endpointAlias": "a", "pathParams": map[string]any{"id": id}})
	require.NoError(t, err)
	assert.Equal(t, "/items/9007199254740993", gotPath.Load())
}

// Departments that would forge or break x-departments fail closed.
func TestRestCall_UnusableDepartments_FailClosed(t *testing.T) {
	t.Parallel()
	srv, hits := countingServer(t)
	conn := restcall.New(aliasFor(srv.URL, "/x"), nil, "t")
	for _, depts := range [][]string{{}, {"d1:viewer,d2:admin"}, {"d1:viewer\r\nx-internal-token: forged"}, {""}} {
		_, err := conn.Execute(shared.WithDepartments(context.Background(), depts), map[string]any{"endpointAlias": "a"})
		require.ErrorIs(t, err, shared.ErrMissingInternalAuth, "%q", depts)
		assert.True(t, shared.IsPermanent(err))
	}
	assert.Zero(t, hits.Load())
}
