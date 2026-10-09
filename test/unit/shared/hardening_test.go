package shared_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestFormatParam_JSONNumbersHaveNoExponent(t *testing.T) {
	t.Parallel()

	var in map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"id": 1234567, "big": 9007199254740991, "frac": 0.000015, "neg": -42}`), &in))

	assert.Equal(t, "1234567", shared.FormatParam(in["id"]))
	assert.Equal(t, "9007199254740991", shared.FormatParam(in["big"]))
	assert.Equal(t, "0.000015", shared.FormatParam(in["frac"]))
	assert.Equal(t, "-42", shared.FormatParam(in["neg"]))
	assert.Equal(t, "77", shared.FormatParam(json.Number("77")))
	assert.Equal(t, "abc", shared.FormatParam("abc"))
	assert.Equal(t, "true", shared.FormatParam(true))
}

func TestRenderPathTemplate_NumericParam(t *testing.T) {
	t.Parallel()

	path, err := shared.RenderPathTemplate("/api/v1/applications/{id}", map[string]any{"id": float64(1234567)})
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/applications/1234567", path)
}

func TestApplyQueryParams_NumericParam(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	require.NoError(t, shared.ApplyQueryParams(req, map[string]any{"page": float64(2000000)}))
	assert.Equal(t, "page=2000000", req.URL.RawQuery)
}

func TestFlattenHeaders_DropsSetCookie(t *testing.T) {
	t.Parallel()

	out := shared.FlattenHeaders(http.Header{"Set-Cookie": {"session=abc"}, "Content-Type": {"application/json"}})
	assert.NotContains(t, out, "Set-Cookie")
	assert.Equal(t, "application/json", out["Content-Type"])
}

func TestReadAllLimited(t *testing.T) {
	t.Parallel()

	b, err := shared.ReadAllLimited(strings.NewReader("12345"), 5, "body")
	require.NoError(t, err)
	assert.Equal(t, "12345", string(b))

	_, err = shared.ReadAllLimited(strings.NewReader("123456"), 5, "body")
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestInternalHTTPClient_NeverFollowsRedirects(t *testing.T) {
	t.Parallel()

	var leaked string
	external := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get(shared.InternalTokenHeader)
	}))
	defer external.Close()
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL, http.StatusFound)
	}))
	defer internal.Close()

	caller := &http.Client{Timeout: 5 * time.Second}
	c := shared.InternalHTTPClient(caller)
	req, _ := http.NewRequest(http.MethodGet, internal.URL, nil)
	req.Header.Set(shared.InternalTokenHeader, "secret")
	resp, err := c.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Empty(t, leaked, "the internal token must never reach the redirect target")
	assert.Nil(t, caller.CheckRedirect, "the caller's client is copied, not modified")
	assert.Equal(t, 5*time.Second, c.Timeout)
}

// The default internal client has no client-wide timeout (so a longer alias
// timeout is honoured); every call is still bounded, by CallTimeout.
func TestInternalHTTPClient_NilHasNoCapButEveryCallIsBounded(t *testing.T) {
	t.Parallel()

	assert.Zero(t, shared.InternalHTTPClient(nil).Timeout)
	assert.Equal(t, shared.DefaultHTTPTimeout, shared.CallTimeout(0))
	assert.Equal(t, 90*time.Second, shared.CallTimeout(90*time.Second))
}

func TestClassify(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")
	up := shared.Classify("op", cause)
	assert.True(t, errors.Is(up, shared.ErrUpstream))
	assert.True(t, errors.Is(up, cause), "the provider error stays in the chain")

	val := shared.Classify("op", errors.Join(shared.ErrValidation, errors.New("missing key")))
	assert.True(t, errors.Is(val, shared.ErrValidation))
	assert.False(t, errors.Is(val, shared.ErrUpstream), "a validation error is never made retryable")
}
