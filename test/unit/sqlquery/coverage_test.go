package sqlquery_test

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sqlquery"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

var deptCtx = shared.WithDepartments(context.Background(), []string{"dept-1:reviewer"})

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func aliasesFor(baseURL string) aliasconfig.Config {
	return aliasconfig.Config{SQLQuery: []aliasconfig.Query{{Alias: "q", BaseURL: baseURL, Path: "/q", QueryID: "q"}}}
}

func TestSQLQuery_Type(t *testing.T) {
	t.Parallel()
	assert.Equal(t, registry.TypeSQLQuery, sqlquery.New(aliasconfig.Config{}, nil, testInternalToken).Type())
}

func TestSQLQuery_MissingAlias_IsValidationError(t *testing.T) {
	t.Parallel()
	_, err := sqlquery.New(aliasconfig.Config{}, nil, testInternalToken).Execute(deptCtx, map[string]any{})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
}

func TestSQLQuery_UnencodableParams_IsValidationError(t *testing.T) {
	t.Parallel()
	conn := sqlquery.New(aliasesFor("http://127.0.0.1:1"), nil, testInternalToken)
	_, err := conn.Execute(deptCtx, map[string]any{"queryAlias": "q", "params": []any{math.NaN()}})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "encoding request")
}

func TestSQLQuery_UnbuildableRequest_IsValidationError(t *testing.T) {
	t.Parallel()
	// Not validated here: a control character makes the URL unparseable.
	conn := sqlquery.New(aliasesFor("http://svc.internal\x7f"), nil, testInternalToken)
	_, err := conn.Execute(deptCtx, map[string]any{"queryAlias": "q"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrValidation)
	assert.Contains(t, err.Error(), "baseURL")
}

func TestSQLQuery_TransportError_IsUpstream(t *testing.T) {
	t.Parallel()
	boom := errors.New("transport down")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, boom })}
	_, err := sqlquery.New(aliasesFor("http://svc.internal"), client, testInternalToken).Execute(deptCtx, map[string]any{"queryAlias": "q"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, boom)
}

func TestSQLQuery_ResponseReadError_IsUpstream(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection dropped mid-body")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(errReader{err: boom}), Request: r}, nil
	})}
	_, err := sqlquery.New(aliasesFor("http://svc.internal"), client, testInternalToken).Execute(deptCtx, map[string]any{"queryAlias": "q"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, boom)
}

func TestSQLQuery_MalformedResponse_IsPermanentUpstream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.Copy(w, strings.NewReader("not json"))
	}))
	defer srv.Close()

	_, err := sqlquery.New(aliasesFor(srv.URL), nil, testInternalToken).Execute(deptCtx, map[string]any{"queryAlias": "q"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.True(t, shared.IsPermanent(err))
	assert.Contains(t, err.Error(), "decoding response")
}
