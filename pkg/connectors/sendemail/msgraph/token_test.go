package msgraph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// The token client never follows a redirect: the client secret must not be
// sent to another host.
func TestTokenHTTPClient_DoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"stolen","token_type":"Bearer"}`))
	}))
	t.Cleanup(other.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)

	client := tokenHTTPClient()
	assert.Equal(t, shared.DefaultHTTPTimeout, client.Timeout)
	cc := &clientcredentials.Config{ClientID: "id", ClientSecret: "secret", TokenURL: redirecting.URL}
	_, err := cc.Token(context.WithValue(context.Background(), oauth2.HTTPClient, client))
	require.Error(t, err)
	assert.Zero(t, elsewhere.Load(), "the redirect was not followed")

	built, err := NewProvider(context.Background(), map[string]any{"tenantId": "contoso.onmicrosoft.com", "clientId": "id", "clientSecret": "secret"})
	require.NoError(t, err)
	api := built.(*graphClient).api.(*realGraphAPI)
	require.NotNil(t, api.tokenIdle)
	assert.NotNil(t, api.tokenIdle.CheckRedirect)
	assert.NoError(t, built.(*graphClient).Close())
}
