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

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestNewProvider_BuildsClosableClient(t *testing.T) {
	t.Parallel()

	client, err := NewProvider(context.Background(), map[string]any{"tenantId": "contoso.onmicrosoft.com", "clientId": "id", "clientSecret": "secret"})
	require.NoError(t, err)
	gc, ok := client.(*graphClient)
	require.True(t, ok)
	api, ok := gc.api.(*realGraphAPI)
	require.True(t, ok)
	assert.Equal(t, graphAPIBaseURL, api.baseURL)
	assert.NotNil(t, api.idle)
	assert.NoError(t, gc.Close())
}

func TestGraphClient_Close_Variants(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (&graphClient{api: &fakeGraphAPI{}}).Close(), "an API without idle connections")
	assert.NoError(t, (&graphClient{api: &realGraphAPI{}}).Close(), "a real API without its own transport")
}

func TestRealGraphAPI_InvalidBaseURL_IsNotDelivered(t *testing.T) {
	t.Parallel()

	client := &graphClient{api: &realGraphAPI{httpClient: http.DefaultClient, baseURL: "://no-scheme"}}
	_, err := client.Send(context.Background(), msg)
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrNotDelivered, "the request was never written")
}

// A token endpoint that cannot answer (it hangs up): Graph is never called,
// and the failure is transient and not delivered.
func TestGraph_TokenEndpointUnreachable_TransientNotDelivered(t *testing.T) {
	t.Parallel()

	var graphHits atomic.Int32
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		graphHits.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(graph.Close)
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(token.Close)

	cc := &clientcredentials.Config{ClientID: "id", ClientSecret: "secret", TokenURL: token.URL}
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, token.Client())
	httpClient := &http.Client{Transport: &oauth2.Transport{Source: cc.TokenSource(tokenCtx), Base: graph.Client().Transport}}
	client := &graphClient{api: &realGraphAPI{httpClient: httpClient, baseURL: graph.URL}}

	_, err := client.Send(context.Background(), msg)
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsTransient(err))
	assert.Zero(t, graphHits.Load(), "sendMail is never made without a token")
}

func TestGraph_ClassifyTokenError_RetrieveErrorWithoutResponse(t *testing.T) {
	t.Parallel()

	_, notWritten := sendemail.TraceWrites(context.Background())
	err := classifyError(&oauth2.RetrieveError{}, notWritten)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsTransient(err), "no token endpoint status: nothing was sent, so it is transient")
}
