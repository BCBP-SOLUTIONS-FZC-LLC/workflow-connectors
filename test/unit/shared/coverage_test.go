package shared_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestClass_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "transient", shared.ClassTransient.String())
	assert.Equal(t, "permanent", shared.ClassPermanent.String())
	assert.Equal(t, "unknown", shared.ClassUnknown.String())
	assert.Equal(t, "unknown", shared.Class(42).String())
}

func TestWithClass_NilError(t *testing.T) {
	t.Parallel()
	assert.NoError(t, shared.WithClass(shared.ClassTransient, "x", nil))
	assert.NoError(t, shared.ClassifyByCause(nil))
	class, reason := shared.ClassifyCause(nil)
	assert.Equal(t, shared.ClassUnknown, class)
	assert.Empty(t, reason)
}

// statusErr has the shape of an SDK response error exposing HTTPStatusCode.
type statusErr struct{ status int }

func (e statusErr) Error() string       { return "response error" }
func (e statusErr) HTTPStatusCode() int { return e.status }

func TestClassifyCause_HTTPStatusCode(t *testing.T) {
	t.Parallel()
	class, reason := shared.ClassifyCause(statusErr{status: 503})
	assert.Equal(t, shared.ClassTransient, class)
	assert.Equal(t, "http 503", reason)

	class, reason = shared.ClassifyCause(statusErr{status: 404})
	assert.Equal(t, shared.ClassPermanent, class)
	assert.Equal(t, "http 404", reason)

	// A 2xx status says nothing about the failure.
	class, _ = shared.ClassifyCause(statusErr{status: 200})
	assert.Equal(t, shared.ClassUnknown, class)
}

func TestClassifyCause_DialFailure(t *testing.T) {
	t.Parallel()
	err := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("no route")}
	class, reason := shared.ClassifyCause(err)
	assert.Equal(t, shared.ClassTransient, class)
	assert.Equal(t, "connection failed", reason)

	class, _ = shared.ClassifyCause(&net.OpError{Op: "read", Net: "tcp", Err: errors.New("weird")})
	assert.Equal(t, shared.ClassUnknown, class)
}

func TestClassifyByCause_KeepsExplicitlyUnknown(t *testing.T) {
	t.Parallel()
	explicit := shared.WithClass(shared.ClassUnknown, "outcome unknown", io.EOF)
	got := shared.ClassifyByCause(explicit)
	assert.Same(t, explicit, got, "an explicitly unknown error is not reclassified by its cause")
	class, reason := shared.ClassOf(got)
	assert.Equal(t, shared.ClassUnknown, class)
	assert.Equal(t, "outcome unknown", reason)
}

func TestClientCache_ZeroLimit_EvictsNothingWhenEmpty(t *testing.T) {
	t.Parallel()
	cache := shared.NewClientCache[string](0)
	h, err := cache.Acquire("k", func() (string, error) { return "client", nil })
	require.NoError(t, err)
	defer h.Release()
	assert.Equal(t, "client", h.Client())
	assert.Equal(t, 1, cache.Len())
}

func TestValidateGoogleServiceAccountKey_Valid(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"service_account"}`,
		`{"type":"service_account","token_uri":"https://oauth2.googleapis.com/token","universe_domain":"googleapis.com"}`,
	} {
		assert.NoError(t, shared.ValidateGoogleServiceAccountKey([]byte(raw), "key"), raw)
	}
}

func TestRenderPathTemplate_Unterminated(t *testing.T) {
	t.Parallel()
	_, err := shared.RenderPathTemplate("/items/{id", map[string]any{"id": 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unterminated")
}

func TestDecodeResponseBody_InvalidJSONFallsBackToString(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader("{not json")),
	}
	v, err := shared.DecodeResponseBody(resp)
	require.NoError(t, err)
	assert.Equal(t, "{not json", v)
}

func TestFormatParam_Float32(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "3", shared.FormatParam(float32(3)))
	assert.Equal(t, "1.5", shared.FormatParam(float32(1.5)))
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadAllLimited_ReadError(t *testing.T) {
	t.Parallel()
	boom := errors.New("read failed")
	_, err := shared.ReadAllLimited(failingReader{err: boom}, 10, "body")
	assert.ErrorIs(t, err, boom)
}
