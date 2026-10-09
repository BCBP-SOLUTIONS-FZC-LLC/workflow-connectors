package ses

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sesAnswering(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSES_Accepted_ReturnsMessageID(t *testing.T) {
	t.Parallel()

	id, err := clientFor(sesAnswering(t, `{"MessageId":"ses-1"}`).URL).Send(context.Background(), msg)
	require.NoError(t, err)
	assert.Equal(t, "ses-1", id)
}

func TestSES_AcceptedWithoutMessageID_ReturnsEmptyID(t *testing.T) {
	t.Parallel()

	id, err := clientFor(sesAnswering(t, `{}`).URL).Send(context.Background(), msg)
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestSESClient_Close_Variants(t *testing.T) {
	t.Parallel()

	assert.NoError(t, clientFor("http://127.0.0.1:1").Close(), "drops the client's idle connections")
	assert.NoError(t, (&sesClient{}).Close(), "a client without its own transport")
}
