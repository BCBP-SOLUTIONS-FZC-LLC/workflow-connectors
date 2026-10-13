package shared_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// swapDefaultTransport replaces http.DefaultTransport for one test, as a
// worker does when it wraps it (otelhttp, httpmock). Not parallel: it changes
// a process global, and Go runs non-parallel tests before parallel ones.
func swapDefaultTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	saved := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = saved })
}

// A wrapped http.DefaultTransport used to panic on an unchecked type
// assertion in every client constructor (connectors.New included).
func TestBaseTransport_WrappedDefaultTransport_NoPanic(t *testing.T) {
	swapDefaultTransport(t, wrapper{next: http.DefaultTransport})

	base := shared.BaseTransport()
	require.NotNil(t, base)
	assert.True(t, base.ForceAttemptHTTP2, "Go's default settings")
	assert.Equal(t, 100, base.MaxIdleConns)
	assert.NotNil(t, base.Proxy)
	assert.NotNil(t, base.DialContext)

	assert.NotPanics(t, func() {
		c := shared.InternalHTTPClient(nil)
		tr, ok := c.Transport.(*http.Transport)
		require.True(t, ok)
		assert.Nil(t, tr.Proxy)
	})
	assert.NotPanics(t, func() {
		c := shared.ProviderHTTPClient()
		_, ok := c.Transport.(*http.Transport)
		assert.True(t, ok)
	})
}

func TestBaseTransport_ClonesDefaultTransport(t *testing.T) {
	custom := &http.Transport{MaxIdleConns: 7}
	swapDefaultTransport(t, custom)

	base := shared.BaseTransport()
	assert.Equal(t, 7, base.MaxIdleConns)
	assert.NotSame(t, custom, base, "a clone, never the shared transport")
}

// The internal default transport never uses an egress proxy, which would see
// x-internal-token; a caller's own *http.Transport keeps its proxy.
func TestInternalHTTPClient_DefaultTransportHasNoProxy(t *testing.T) {
	t.Parallel()
	c := shared.InternalHTTPClient(nil)
	assert.Nil(t, c.Transport.(*http.Transport).Proxy)

	c = shared.InternalHTTPClient(&http.Client{})
	assert.Nil(t, c.Transport.(*http.Transport).Proxy)

	proxyURL, _ := url.Parse("http://proxy.internal:3128")
	caller := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	c = shared.InternalHTTPClient(caller)
	tr := c.Transport.(*http.Transport)
	require.NotNil(t, tr.Proxy, "the caller's proxy choice is kept")
	got, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "http", Host: "svc"}})
	require.NoError(t, err)
	assert.Equal(t, proxyURL, got)

	// Provider calls go to external APIs and keep the default proxy setting.
	assert.NotNil(t, shared.ProviderHTTPClient().Transport.(*http.Transport).Proxy)
}
