package connectors_test

import (
	"net/http"
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
