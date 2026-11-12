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

func TestSQLQuery_SendsTheCallerIdentityAndBindsParams(t *testing.T) {
	t.Parallel()

	var gotToken string
	var got http.Header
	var gotBody struct {
		QueryID string `json:"queryId"`
		Params  []any  `json:"params"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("x-internal-token")
		got = r.Header.Clone()
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resultSet": []map[string]any{{"id": "1"}}})
	}))
	defer srv.Close()

	conn := sqlQueryConnector(srv, aliasconfig.Query{Alias: "get-tenants", Path: "/q", QueryID: "get-tenants", ParamCount: 1})

	ctx := shared.WithLaneDepartment(shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub"), "lane-dept")
	ctx = shared.WithDepartments(ctx, []string{"dept-1:reviewer"})
	out, err := conn.Execute(ctx, map[string]any{
		"queryAlias": "get-tenants",
		"params":     []any{"active"},
	})
	require.NoError(t, err)

	assert.Equal(t, testInternalToken, gotToken)
	assert.Equal(t, "tenant-1", got.Get("x-tenant-id"))
	assert.Equal(t, "principal-sub", got.Get("x-user-id"))
	assert.Equal(t, "lane-dept", got.Get("x-workflow-department-id"))
	assert.Empty(t, got.Values("x-departments"), "a connector call carries no department grant")
	assert.Equal(t, "get-tenants", gotBody.QueryID)
	assert.Equal(t, []any{"active"}, gotBody.Params)
	resultSet, ok := out["resultSet"].([]map[string]any)
	require.True(t, ok)
	assert.Len(t, resultSet, 1)
}

func TestSQLQuery_NoInternalToken_SendsNoTokenHeaderButTheCallerIdentity(t *testing.T) {
	t.Parallel()

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resultSet":[]}`))
	}))
	defer srv.Close()

	aliases := aliasconfig.Config{SQLQuery: []aliasconfig.Query{
		{Alias: "q", BaseURL: srv.URL, Path: "/q", QueryID: "q"},
	}}
	ctx := shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")
	_, err := sqlquery.New(aliases, nil, "").Execute(ctx, map[string]any{"queryAlias": "q"})
	require.NoError(t, err)

	assert.Empty(t, got.Values("x-internal-token"), "no token configured: no header")
	assert.Equal(t, "tenant-1", got.Get("x-tenant-id"))
	assert.Equal(t, "principal-sub", got.Get("x-user-id"))
}

func TestSQLQuery_NoInternalToken_MissingTenantOrActor_FailsClosedBeforeAnyRequest(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	aliases := aliasconfig.Config{SQLQuery: []aliasconfig.Query{
		{Alias: "q", BaseURL: srv.URL, Path: "/q", QueryID: "q"},
	}}
	conn := sqlquery.New(aliases, nil, "")
	for name, ctx := range map[string]context.Context{
		"nothing":     context.Background(),
		"tenant only": shared.WithTenant(context.Background(), "tenant-1"),
		"actor only":  shared.WithActor(context.Background(), "principal-sub"),
	} {
		_, err := conn.Execute(ctx, map[string]any{"queryAlias": "q"})
		require.ErrorIs(t, err, shared.ErrMissingInternalAuth, name)
	}
	assert.Zero(t, hits.Load())
}

func TestSQLQuery_ParamCountMismatch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	conn := sqlQueryConnector(srv, aliasconfig.Query{Alias: "get-tenants", Path: "/q", QueryID: "get-tenants", ParamCount: 2})

	ctx := shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")
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
	ctx := shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")

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
