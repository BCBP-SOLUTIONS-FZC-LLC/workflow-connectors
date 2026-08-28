package sqlquery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/sqlquery"
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
