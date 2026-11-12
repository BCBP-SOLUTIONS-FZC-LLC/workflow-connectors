package restcall_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/restcall"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

const testInternalToken = "test-token"

func restCallConnector(t *testing.T, srv *httptest.Server, ep aliasconfig.Endpoint) restcall.Connector {
	t.Helper()
	ep.BaseURL = srv.URL
	aliases := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{ep}}
	return restcall.New(aliases, nil, testInternalToken)
}

func TestRestCall_SendsTheCallerIdentityAndNoDepartmentGrant(t *testing.T) {
	t.Parallel()

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	conn := restCallConnector(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps/{id}"})

	ctx := shared.WithLaneDepartment(callerCtx(), "lane-dept")
	ctx = shared.WithDepartments(ctx, []string{"dept-1:reviewer"})
	out, err := conn.Execute(ctx, map[string]any{
		"endpointAlias": "get-app",
		"pathParams":    map[string]any{"id": "42"},
	})
	require.NoError(t, err)

	assert.Equal(t, testInternalToken, got.Get("x-internal-token"))
	assert.Equal(t, "tenant-1", got.Get("x-tenant-id"))
	assert.Equal(t, "principal-sub", got.Get("x-user-id"))
	assert.Equal(t, "lane-dept", got.Get("x-workflow-department-id"))
	assert.Empty(t, got.Values("x-departments"), "a connector call carries no department grant")
	for _, h := range []string{"x-tenant-roles", "x-plan", "x-feature-flags", "x-residency-mode"} {
		assert.Empty(t, got.Values(h), h)
	}
	assert.EqualValues(t, http.StatusOK, out["status"])
}

func TestRestCall_MissingCallerIdentity_FailsClosedBeforeAnyRequest(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	conn := restCallConnector(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps/{id}"})

	for name, ctx := range map[string]context.Context{
		"nothing":          context.Background(),
		"departments only": shared.WithDepartments(context.Background(), []string{"dept-1:reviewer"}),
		"tenant only":      shared.WithTenant(context.Background(), "tenant-1"),
	} {
		_, err := conn.Execute(ctx, map[string]any{
			"endpointAlias": "get-app",
			"pathParams":    map[string]any{"id": "42"},
		})
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, shared.ErrMissingInternalAuth), name)
	}
	assert.False(t, called)
}

func TestRestCall_NoInternalToken_SendsNoTokenHeaderButTheCallerIdentity(t *testing.T) {
	t.Parallel()

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	aliases := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "get-app", Method: "GET", BaseURL: srv.URL, PathTemplate: "/apps"},
	}}
	conn := restcall.New(aliases, nil, "")

	_, err := conn.Execute(callerCtx(), map[string]any{"endpointAlias": "get-app"})
	require.NoError(t, err)

	assert.Empty(t, got.Values("x-internal-token"), "no token configured: no header")
	assert.Equal(t, "tenant-1", got.Get("x-tenant-id"))
	assert.Equal(t, "principal-sub", got.Get("x-user-id"))
}

func TestRestCall_NoInternalToken_MissingTenantOrActor_FailsClosedBeforeAnyRequest(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	aliases := aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "get-app", Method: "GET", BaseURL: srv.URL, PathTemplate: "/apps"},
	}}
	conn := restcall.New(aliases, nil, "")

	for name, ctx := range map[string]context.Context{
		"nothing":     context.Background(),
		"tenant only": shared.WithTenant(context.Background(), "tenant-1"),
		"actor only":  shared.WithActor(context.Background(), "principal-sub"),
	} {
		_, err := conn.Execute(ctx, map[string]any{"endpointAlias": "get-app"})
		require.ErrorIs(t, err, shared.ErrMissingInternalAuth, name)
	}
	assert.False(t, called)
}

func TestRestCall_UnknownAlias_NoNetworkCall(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	conn := restCallConnector(t, srv, aliasconfig.Endpoint{Alias: "known", Method: "GET", PathTemplate: "/x"})

	ctx := shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")
	_, err := conn.Execute(ctx, map[string]any{"endpointAlias": "unknown"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, aliasconfig.ErrUnknownAlias))
	assert.False(t, called)
}

func TestRestCall_NonSuccessStatus_ReturnsUpstreamError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	conn := restCallConnector(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps"})

	ctx := shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")
	_, err := conn.Execute(ctx, map[string]any{"endpointAlias": "get-app"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
}

func TestRestCall_AppliesQueryParams(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("status")
	}))
	defer srv.Close()

	conn := restCallConnector(t, srv, aliasconfig.Endpoint{Alias: "list-apps", Method: "GET", PathTemplate: "/apps"})

	ctx := shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")
	_, err := conn.Execute(ctx, map[string]any{
		"endpointAlias": "list-apps",
		"queryParams":   map[string]any{"status": "active"},
	})
	require.NoError(t, err)
	assert.Equal(t, "active", gotQuery)
}

func TestRestCall_MissingPathParam(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	conn := restCallConnector(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps/{id}"})

	ctx := shared.WithActor(shared.WithTenant(context.Background(), "tenant-1"), "principal-sub")
	_, err := conn.Execute(ctx, map[string]any{"endpointAlias": "get-app"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}
