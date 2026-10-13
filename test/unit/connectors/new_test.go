package connectors_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// The worker drops cached provider clients after a credential rotation by
// asserting this interface on what New returns: it must hold for storage and
// send-email (pointer receivers, so New must keep returning pointers).
func TestNew_StorageAndSendEmailCanResetClients(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(validConfig())
	require.NoError(t, err)
	for _, typ := range []string{registry.TypeStorage, registry.TypeSendEmail} {
		resetter, ok := all[typ].(interface{ ResetClients() })
		require.Truef(t, ok, "%s must implement ResetClients", typ)
		assert.NotPanics(t, resetter.ResetClients)
	}
}

// Aliases built in code are validated by New, not only by aliasconfig.Load.
func TestNew_InvalidAliasesFail(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Aliases = aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "a", Method: "GET", BaseURL: "http://svc.internal?x=1", PathTemplate: "/a"},
	}}
	_, err := connectors.New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connectors: Aliases")

	cfg.Aliases = aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "a", Method: "GET", BaseURL: "http://svc.internal", PathTemplate: "/a"},
	}}
	_, err = connectors.New(cfg)
	require.NoError(t, err)
}

type wrappedTransport struct{ next http.RoundTripper }

func (w wrappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return w.next.RoundTrip(r)
}

// A worker that wraps http.DefaultTransport (otelhttp, httpmock) must not
// make New panic. Not parallel: it swaps a process global.
func TestNew_WrappedDefaultTransport_NoPanic(t *testing.T) {
	saved := http.DefaultTransport
	http.DefaultTransport = wrappedTransport{next: saved}
	t.Cleanup(func() { http.DefaultTransport = saved })

	assert.NotPanics(t, func() {
		_, err := connectors.New(validConfig())
		require.NoError(t, err)
	})
}

// End to end: an alias whose "#" would hide its fixed query ("role=user"
// becomes a fragment, never sent) is refused by New, so no Execute can let a
// caller's queryParams set role. With the "#" removed the fixed key is
// enforced and the server sees only the alias's value.
func TestNew_FragmentAliasRefused_FixedQueryKeyEnforced(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	var gotQuery atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotQuery.Store(r.URL.RawQuery)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	cfg := validConfig()
	cfg.Aliases = aliasconfig.Config{RestCall: []aliasconfig.Endpoint{
		{Alias: "users", Method: "GET", BaseURL: srv.URL, PathTemplate: "/users#?role=user"},
	}}
	_, err := connectors.New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `must not contain "#"`)

	cfg.Aliases.RestCall[0].PathTemplate = "/users?role=user"
	all, err := connectors.New(cfg)
	require.NoError(t, err)
	ctx := connectors.WithDepartments(context.Background(), []string{"d:approver"})

	_, err = all[registry.TypeRestCall].Execute(ctx, map[string]any{
		"endpointAlias": "users",
		"queryParams":   map[string]any{"role": "admin"},
	})
	require.ErrorIs(t, err, connectors.ErrValidation)
	assert.Zero(t, hits.Load(), "a refused call never reaches the service")

	_, err = all[registry.TypeRestCall].Execute(ctx, map[string]any{
		"endpointAlias": "users",
		"queryParams":   map[string]any{"page": "2"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load())
	assert.Equal(t, "role=user&page=2", gotQuery.Load())
}
