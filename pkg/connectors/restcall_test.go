package connectors_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/aliasconfig"
)

func restCallConfig(t *testing.T, srv *httptest.Server, ep aliasconfig.Endpoint) connectors.Config {
	t.Helper()
	ep.BaseURL = srv.URL
	cfg := validConfig()
	cfg.Aliases = aliasconfig.Config{RestCall: []aliasconfig.Endpoint{ep}}
	return cfg
}

func TestRestCall_AttachesMandatoryHeaders(t *testing.T) {
	t.Parallel()

	var gotToken, gotDepartments string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("x-internal-token")
		gotDepartments = r.Header.Get("x-departments")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cfg := restCallConfig(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps/{id}"})
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	ctx := connectors.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	out, err := all["rest-call"].Execute(ctx, map[string]any{
		"endpointAlias": "get-app",
		"pathParams":    map[string]any{"id": "42"},
	})
	require.NoError(t, err)

	assert.Equal(t, cfg.InternalToken, gotToken)
	assert.Equal(t, "dept-1:reviewer", gotDepartments)
	assert.EqualValues(t, http.StatusOK, out["status"])
}

func TestRestCall_MissingDepartments_FailsClosedBeforeAnyRequest(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	cfg := restCallConfig(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps/{id}"})
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	_, err = all["rest-call"].Execute(context.Background(), map[string]any{
		"endpointAlias": "get-app",
		"pathParams":    map[string]any{"id": "42"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrMissingInternalAuth))
	assert.False(t, called)
}

func TestRestCall_UnknownAlias_NoNetworkCall(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	cfg := restCallConfig(t, srv, aliasconfig.Endpoint{Alias: "known", Method: "GET", PathTemplate: "/x"})
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	ctx := connectors.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	_, err = all["rest-call"].Execute(ctx, map[string]any{"endpointAlias": "unknown"})
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

	cfg := restCallConfig(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps"})
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	ctx := connectors.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	_, err = all["rest-call"].Execute(ctx, map[string]any{"endpointAlias": "get-app"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrUpstream))
}

func TestRestCall_AppliesQueryParams(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("status")
	}))
	defer srv.Close()

	cfg := restCallConfig(t, srv, aliasconfig.Endpoint{Alias: "list-apps", Method: "GET", PathTemplate: "/apps"})
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	ctx := connectors.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	_, err = all["rest-call"].Execute(ctx, map[string]any{
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

	cfg := restCallConfig(t, srv, aliasconfig.Endpoint{Alias: "get-app", Method: "GET", PathTemplate: "/apps/{id}"})
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	ctx := connectors.WithDepartments(context.Background(), []string{"dept-1:reviewer"})
	_, err = all["rest-call"].Execute(ctx, map[string]any{"endpointAlias": "get-app"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}
