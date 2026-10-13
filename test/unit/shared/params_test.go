package shared_test

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// The alias's fixed query is kept byte for byte — order, valueless flags and
// escaping — and the caller's params are appended, sorted by key.
func TestApplyQueryParams_KeepsAliasQueryVerbatim(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequest(http.MethodGet, "http://svc/items?z=1&verbose&a=x%20y", nil)
	require.NoError(t, err)
	require.NoError(t, shared.ApplyQueryParams(req, map[string]any{"page": json.Number("2"), "b": "c&d", "a2": true}))
	assert.Equal(t, "z=1&verbose&a=x%20y&a2=true&b=c%26d&page=2", req.URL.RawQuery)

	req, err = http.NewRequest(http.MethodGet, "http://svc/items?verbose", nil)
	require.NoError(t, err)
	err = shared.ApplyQueryParams(req, map[string]any{"verbose": "1"})
	require.ErrorIs(t, err, shared.ErrValidation, "a valueless flag is still a key the alias fixed")
	assert.Equal(t, "verbose", req.URL.RawQuery)
}

func TestApplyQueryParams_NonScalarIsValidation(t *testing.T) {
	t.Parallel()

	for name, v := range map[string]any{
		"list": []any{"a", "b"},
		"map":  map[string]any{"k": "v"},
		"nil":  nil,
	} {
		req, err := http.NewRequest(http.MethodGet, "http://svc/items", nil)
		require.NoError(t, err)
		err = shared.ApplyQueryParams(req, map[string]any{"q": v})
		require.ErrorIs(t, err, shared.ErrValidation, name)
		assert.Contains(t, err.Error(), `query param "q"`, name)
		assert.Empty(t, req.URL.RawQuery, "%s: the request is left as the alias built it", name)
	}
}

func TestRenderPathTemplate_NonScalarIsValidation(t *testing.T) {
	t.Parallel()

	for name, v := range map[string]any{
		"list": []any{"a", "b"},
		"map":  map[string]any{"k": "v"},
		"nil":  nil,
	} {
		_, err := shared.RenderPathTemplate("/items/{id}", map[string]any{"id": v})
		require.ErrorIs(t, err, shared.ErrValidation, name)
		assert.Contains(t, err.Error(), `path param "id"`, name)
		_, err = shared.RenderPathTemplate("/items?f={id}", map[string]any{"id": v})
		require.ErrorIs(t, err, shared.ErrValidation, name)
	}
}

func TestFormatScalarParam(t *testing.T) {
	t.Parallel()

	ok := map[any]string{
		"abc":             "abc",
		true:              "true",
		json.Number("77"): "77",
		float64(1234567):  "1234567",
		float32(1.5):      "1.5",
		int(-3):           "-3",
		int8(8):           "8",
		int16(16):         "16",
		int32(32):         "32",
		int64(1 << 40):    "1099511627776",
		uint(1):           "1",
		uint8(2):          "2",
		uint16(3):         "3",
		uint32(4):         "4",
		uint64(1<<63 + 1): "9223372036854775809",
	}
	for in, want := range ok {
		got, err := shared.FormatScalarParam(in)
		require.NoError(t, err, "%T", in)
		assert.Equal(t, want, got, "%T", in)
	}

	for _, in := range []any{nil, []any{"a"}, []string{"a"}, map[string]any{}, struct{}{}} {
		_, err := shared.FormatScalarParam(in)
		require.ErrorIs(t, err, shared.ErrValidation, "%T", in)
	}
}

func formatScalar(t *testing.T, v any) string {
	t.Helper()
	s, err := shared.FormatScalarParam(v)
	require.NoError(t, err)
	return s
}

// The deprecated FormatParam keeps its v2.0.0 behaviour for existing callers.
func TestFormatParam_DeprecatedRendersAnything(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "[a b]", shared.FormatParam([]any{"a", "b"}))    //nolint:staticcheck // pins the deprecated function
	assert.Equal(t, "<nil>", shared.FormatParam(nil))                //nolint:staticcheck // pins the deprecated function
	assert.Equal(t, "1234567", shared.FormatParam(float64(1234567))) //nolint:staticcheck // pins the deprecated function
}

// URL.Query drops a pair containing ";", which would hide a key the alias
// fixed after it; ApplyQueryParams parses strictly and refuses such a query
// rather than sending the key twice.
func TestApplyQueryParams_UnparsableAliasQueryIsValidation(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"http://svc/items?a=1;b=2&page=1", "http://svc/items?a=%zz"} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		require.NoError(t, err)
		before := req.URL.RawQuery
		err = shared.ApplyQueryParams(req, map[string]any{"page": "2"})
		require.ErrorIs(t, err, shared.ErrValidation, raw)
		assert.Contains(t, err.Error(), "cannot be parsed", raw)
		assert.Equal(t, before, req.URL.RawQuery, "%s: the request is left as the alias built it", raw)
	}
}

// Floating-point edge cases never print an exponent (a path segment or
// query value "1e+21" would be a different value to most services), and a
// type that is not exactly a scalar kind — a named type, a pointer — is
// refused rather than rendered with fmt.Sprint.
func TestFormatScalarParam_FloatEdgesAndNonScalarKinds(t *testing.T) {
	t.Parallel()

	for in, want := range map[float64]string{
		math.Copysign(0, -1): "0",
		0.1:                  "0.1",
		-2.5:                 "-2.5",
		1 << 53:              "9007199254740992",
		1e21:                 "1000000000000000000000",
		1e-7:                 "0.0000001",
	} {
		got, err := shared.FormatScalarParam(in)
		require.NoError(t, err, "%v", in)
		assert.Equal(t, want, got, "%v", in)
		assert.NotContains(t, got, "e", "%v", in)
	}

	type label string
	s := "a"
	for _, in := range []any{label("a"), &s, []byte("a"), func() {}, complex(1, 2)} {
		_, err := shared.FormatScalarParam(in)
		require.ErrorIs(t, err, shared.ErrValidation, "%T", in)
	}
}
