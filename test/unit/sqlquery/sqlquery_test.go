package sqlquery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sqlquery"
)

const testInternalToken = "test-token"

func sqlQueryConnector(srv *httptest.Server, q aliasconfig.Query) sqlquery.Connector {
	q.BaseURL = srv.URL
	aliases := aliasconfig.Config{SQLQuery: []aliasconfig.Query{q}}
	return sqlquery.New(aliases, nil, testInternalToken)
}

func TestSQLQuery_AttachesMandatoryHeadersAndBindsParams(t *testing.T) {
	t.Parallel()

	var gotToken, gotDepartments string
	var gotBody struct {
		QueryID string `json:"queryId"`
		Params  []any  `json:"params"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("x-internal-token")
		gotDepartments = r.Header.Get("x-departments")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resultSet": []map[string]any{{"id": "1"}}})
	}))
	defer srv.Close()

	conn := sqlQueryConnector(srv, aliasconfig.Query{Alias: "get-tenants", Path: "/q", QueryID: "get-tenants", ParamCount: 1})

	ctx := shared.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	out, err := conn.Execute(ctx, map[string]any{
		"queryAlias": "get-tenants",
		"params":     []any{"active"},
	})
	require.NoError(t, err)

	assert.Equal(t, testInternalToken, gotToken)
	assert.Equal(t, "dept-1:reviewer", gotDepartments)
	assert.Equal(t, "get-tenants", gotBody.QueryID)
	assert.Equal(t, []any{"active"}, gotBody.Params)
	resultSet, ok := out["resultSet"].([]map[string]any)
	require.True(t, ok)
	assert.Len(t, resultSet, 1)
}

func TestSQLQuery_ParamCountMismatch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	conn := sqlQueryConnector(srv, aliasconfig.Query{Alias: "get-tenants", Path: "/q", QueryID: "get-tenants", ParamCount: 2})

	ctx := shared.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	_, err := conn.Execute(ctx, map[string]any{
		"queryAlias": "get-tenants",
		"params":     []any{"only-one"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestSQLQuery_MissingDepartments_FailsClosed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	conn := sqlQueryConnector(srv, aliasconfig.Query{Alias: "get-tenants", Path: "/q", QueryID: "get-tenants"})

	_, err := conn.Execute(context.Background(), map[string]any{"queryAlias": "get-tenants"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrMissingInternalAuth))
}

// paramCount 0 means exactly zero params; params that are not a list are
// invalid input, never silently dropped.
func TestSQLQuery_ParamsMustMatchExactly(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resultSet":[]}`))
	}))
	defer srv.Close()
	ctx := shared.WithDepartments(context.Background(), []string{"dept-1:reviewer"})

	none := sqlQueryConnector(srv, aliasconfig.Query{Alias: "q", Path: "/q", QueryID: "q", ParamCount: 0})
	_, err := none.Execute(ctx, map[string]any{"queryAlias": "q", "params": []any{"x"}})
	require.ErrorIs(t, err, shared.ErrValidation, "paramCount 0 takes no params")

	for _, ok := range []map[string]any{
		{"queryAlias": "q"},
		{"queryAlias": "q", "params": nil},
		{"queryAlias": "q", "params": []any{}},
	} {
		_, err = none.Execute(ctx, ok)
		require.NoError(t, err, "%v", ok)
	}

	one := sqlQueryConnector(srv, aliasconfig.Query{Alias: "q", Path: "/q", QueryID: "q", ParamCount: 1})
	for _, bad := range []any{"active", map[string]any{"a": 1}, []string{"active"}, json.Number("1")} {
		_, err = one.Execute(ctx, map[string]any{"queryAlias": "q", "params": bad})
		require.ErrorIs(t, err, shared.ErrValidation, "%T", bad)
		assert.Contains(t, err.Error(), "params must be a list")
	}
	assert.Equal(t, int32(3), calls.Load(), "no rejected call reached the service")
}
