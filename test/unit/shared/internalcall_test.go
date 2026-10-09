package shared_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// errors.Is with a per-class sentinel matched any classified error in the
// chain, while ClassOf decides by precedence: an invalid-input error wrapping
// a transient cause is permanent. There is no such sentinel any more, so
// the two can never disagree.
func TestClassifiedError_HasNoClassSentinelMatching(t *testing.T) {
	t.Parallel()
	_, hasIs := any(&shared.ClassifiedError{}).(interface{ Is(error) bool })
	assert.False(t, hasIs, "a class is read with ClassOf only")

	err := fmt.Errorf("%w: %w", shared.ErrValidation, shared.Transient("timeout", context.DeadlineExceeded))
	assert.True(t, shared.IsPermanent(err))
	assert.False(t, shared.IsTransient(err))
}

func TestRenderPathTemplate_RejectsEmptyAndDotSegments(t *testing.T) {
	t.Parallel()
	for _, v := range []any{"", ".", "..", json.Number("")} {
		_, err := shared.RenderPathTemplate("/tenants/t1/items/{id}/view", map[string]any{"id": v})
		require.ErrorIs(t, err, shared.ErrValidation, "%q", v)
		assert.True(t, shared.IsPermanent(err))
	}
	for v, want := range map[string]string{"...": "/items/...", ".x": "/items/.x", "a/../b": "/items/a%2F..%2Fb", "%2e%2e": "/items/%252e%252e"} {
		got, err := shared.RenderPathTemplate("/items/{id}", map[string]any{"id": v})
		require.NoError(t, err, v)
		assert.Equal(t, want, got)
	}
}

func TestRenderPathTemplate_MissingParamIsValidation(t *testing.T) {
	t.Parallel()
	_, err := shared.RenderPathTemplate("/items/{id}", nil)
	assert.ErrorIs(t, err, shared.ErrValidation)
	_, err = shared.RenderPathTemplate("/items/{id", nil)
	assert.ErrorIs(t, err, shared.ErrValidation)
}

// A placeholder in the template's query string is query-escaped, so its
// value can never add a parameter; dot values are fine there.
func TestRenderPathTemplate_QueryPlaceholderIsQueryEscaped(t *testing.T) {
	t.Parallel()
	got, err := shared.RenderPathTemplate("/items/{id}?view={view}&n={n}", map[string]any{
		"id": "a b", "view": "x&admin=true", "n": "..",
	})
	require.NoError(t, err)
	assert.Equal(t, "/items/a%20b?view=x%26admin%3Dtrue&n=..", got)
}

func TestNewInternalRequest_StaysOnTheAliasHost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	req, err := shared.NewInternalRequest(ctx, http.MethodGet, "http://svc.internal:8080/api/", "/items/1?view=full", nil)
	require.NoError(t, err)
	assert.Equal(t, "http://svc.internal:8080/api/items/1?view=full", req.URL.String())

	for _, tc := range []struct{ base, path string }{
		{"http://svc", "@evil.example/x"},   // userinfo "svc", host evil.example
		{"http://svc", ".evil.example/x"},   // host svc.evil.example
		{"http://svc", ":1@evil.example/x"}, // userinfo, other host
		{"http://svc", "x"},                 // no leading slash
		{"http://user:pw@svc", "/x"},        // userinfo on the base
		{"http://svc", "/%zz"},              // unparsable result
		{"http://[::1", "/x"},               // unparsable base
		{"http://svc", ""},                  // nothing to append
	} {
		_, err := shared.NewInternalRequest(ctx, http.MethodGet, tc.base, tc.path, nil)
		require.ErrorIs(t, err, shared.ErrValidation, "%s + %s", tc.base, tc.path)
	}

	_, err = shared.NewInternalRequest(ctx, "BAD METHOD", "http://svc", "/x", nil)
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "building request")
}

func TestApplyQueryParams_CannotOverrideTemplateKey(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://svc/items?scope=own", nil)
	err := shared.ApplyQueryParams(req, map[string]any{"scope": "all"})
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.Equal(t, "scope=own", req.URL.RawQuery, "the request is left as the alias built it")

	require.NoError(t, shared.ApplyQueryParams(req, map[string]any{"page": json.Number("2")}))
	assert.Equal(t, "page=2&scope=own", req.URL.RawQuery)
	require.NoError(t, shared.ApplyQueryParams(req, nil))
}

func TestDecodeBody_KeepsLargeIntegersExact(t *testing.T) {
	t.Parallel()

	v := shared.DecodeBody([]byte(`{"id": 12345678901234567890, "amount": 9007199254740993}`), "application/json")
	m, ok := v.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, json.Number("12345678901234567890"), m["id"])
	assert.Equal(t, json.Number("9007199254740993"), m["amount"])
	assert.Equal(t, "9007199254740993", shared.FormatParam(m["amount"]))

	assert.Nil(t, shared.DecodeBody(nil, "application/json"))
	assert.Equal(t, `{"a":1} trailing`, shared.DecodeBody([]byte(`{"a":1} trailing`), "application/json"))
	assert.Equal(t, `{"a":1}`, shared.DecodeBody([]byte(`{"a":1}`), "text/plain"))
	assert.Equal(t, map[string]any{"a": json.Number("1")}, shared.DecodeBody([]byte("{\"a\":1}\n"), "application/json"))
}

func TestDecodeJSON_UsesNumbers(t *testing.T) {
	t.Parallel()
	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	require.NoError(t, shared.DecodeJSON([]byte(`{"rows":[{"id":18446744073709551615}]}`), &out))
	assert.Equal(t, json.Number("18446744073709551615"), out.Rows[0]["id"])
	require.Error(t, shared.DecodeJSON([]byte(`{} {}`), &out))
	require.Error(t, shared.DecodeJSON([]byte(`{`), &out))
}

func TestStringFields_AcceptJSONNumber(t *testing.T) {
	t.Parallel()
	m := map[string]any{"id": json.Number("12345678901234567890"), "ids": []any{"a", json.Number("7"), 3.0}, "n": 3.0}
	assert.Equal(t, "12345678901234567890", shared.StringField(m, "id"))
	assert.Equal(t, "", shared.StringField(m, "n"))
	assert.Equal(t, []string{"a", "7"}, shared.StringSliceField(m, "ids"))
}

type partialThenFail struct {
	data []byte
	done bool
}

func (r *partialThenFail) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("connection reset")
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestDecodeErrorResponseBody_NeverFails(t *testing.T) {
	t.Parallel()

	decode := func(ct string, body io.Reader) (any, bool) {
		resp := &http.Response{Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(body)}
		defer func() { _ = resp.Body.Close() }()
		return shared.DecodeErrorResponseBody(resp)
	}

	body, truncated := decode("application/json", strings.NewReader(`{"error":"busy"}`))
	assert.False(t, truncated)
	assert.Equal(t, map[string]any{"error": "busy"}, body)

	big := strings.Repeat("x", int(shared.MaxResponseBytes)) + "é tail"
	body, truncated = decode("text/plain", strings.NewReader(big))
	assert.True(t, truncated)
	assert.Len(t, body, int(shared.MaxResponseBytes))

	// Cut mid-rune: the incomplete UTF-8 sequence is dropped.
	body, truncated = decode("application/json", &partialThenFail{data: []byte(`{"error":"caf` + "\xc3")})
	assert.True(t, truncated)
	assert.Equal(t, `{"error":"caf`, body)
}

func TestDepartments_RequireAtLeastOneUsableValue(t *testing.T) {
	t.Parallel()

	bad := map[string][]string{
		"unset": nil,
		"empty": {},
		"blank": {"d1:approver", ""},
		"comma": {"d1:approver,d2:admin"},
		"cr":    {"d1:approver\r"},
		"lf":    {"d1:approver\nx-other: 1"},
	}
	for name, depts := range bad {
		ctx := context.Background()
		if depts != nil || name != "unset" {
			ctx = shared.WithDepartments(ctx, depts)
		}
		_, ok := shared.DepartmentsFromContext(ctx)
		assert.False(t, ok, name)
		_, err := shared.DepartmentsHeaderValue(ctx)
		require.ErrorIs(t, err, shared.ErrMissingInternalAuth, name)
		assert.True(t, shared.IsPermanent(err), name)
	}

	ctx := shared.WithDepartments(context.Background(), []string{"d1:approver", "d2:viewer"})
	got, ok := shared.DepartmentsFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, []string{"d1:approver", "d2:viewer"}, got)
	v, err := shared.DepartmentsHeaderValue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "d1:approver,d2:viewer", v)
}

func h2TLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Proto))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func protoOf(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.Proto
}

// Internal calls never negotiate HTTP/2, whose client may replay a request
// body after a stream reset (a repeated non-idempotent rest-call).
func TestInternalHTTPClient_IsHTTP1Only(t *testing.T) {
	t.Parallel()
	srv := h2TLSServer(t)
	tlsConfig := srv.Client().Transport.(*http.Transport).TLSClientConfig

	// nil client: a clone of the default transport.
	c := shared.InternalHTTPClient(nil)
	c.Transport.(*http.Transport).TLSClientConfig = tlsConfig.Clone()
	assert.Equal(t, "HTTP/1.1", protoOf(t, c, srv.URL))

	// A caller's *http.Transport that would negotiate HTTP/2 is cloned and
	// forced to HTTP/1.1; the caller's own transport is left untouched.
	caller := srv.Client()
	require.Equal(t, "HTTP/2.0", protoOf(t, caller, srv.URL), "precondition: the caller's client speaks HTTP/2")
	callerTransport := caller.Transport
	c = shared.InternalHTTPClient(caller)
	assert.Equal(t, "HTTP/1.1", protoOf(t, c, srv.URL))
	assert.Same(t, callerTransport, caller.Transport)
	assert.Equal(t, "HTTP/2.0", protoOf(t, caller, srv.URL), "the caller's TLS config still offers h2")
	assert.NotSame(t, callerTransport, c.Transport)

	// A caller's client with no transport gets the HTTP/1.1 default clone.
	c = shared.InternalHTTPClient(&http.Client{})
	_, isTransport := c.Transport.(*http.Transport)
	assert.True(t, isTransport)
}

type wrapper struct{ next http.RoundTripper }

func (w wrapper) RoundTrip(r *http.Request) (*http.Response, error) { return w.next.RoundTrip(r) }

// Any other RoundTripper is the caller's: it is used as-is.
func TestInternalHTTPClient_CustomRoundTripperUsedAsIs(t *testing.T) {
	t.Parallel()
	rt := wrapper{next: http.DefaultTransport}
	c := shared.InternalHTTPClient(&http.Client{Transport: rt})
	assert.Equal(t, rt, c.Transport)
}
