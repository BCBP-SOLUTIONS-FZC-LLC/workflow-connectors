package sendgrid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestSendGrid_DeliveryOutcomes_ByStatus(t *testing.T) {
	t.Parallel()

	msg := sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"}
	for status, want := range map[int]error{
		400: shared.ErrNotDelivered,
		429: shared.ErrNotDelivered,
		500: shared.ErrDeliveryUnknown,
	} {
		_, err := (&sendGridClient{api: &fakeSendGridAPI{statusCode: status}}).Send(context.Background(), msg)
		assert.ErrorIs(t, err, want, status)
		assert.NotErrorIs(t, err, shared.ErrUpstream, status)
	}
}

// Over real HTTP: whether the request was written decides the outcome.
func TestSendGrid_DeliveryOutcomes_ByWhetherTheRequestWasWritten(t *testing.T) {
	t.Parallel()
	msg := sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"}

	// The provider reads the whole request, then the connection dies before
	// any response: it may have accepted the email.
	lost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(lost.Close)
	_, err := (&sendGridClient{api: newRealSendGridAPI("key", lost.URL)}).Send(context.Background(), msg)
	assert.ErrorIs(t, err, shared.ErrDeliveryUnknown, "written, no response: may have been accepted")

	// Nothing listens: the request never left.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := "http://" + ln.Addr().String()
	require.NoError(t, ln.Close())
	_, err = (&sendGridClient{api: newRealSendGridAPI("key", closed)}).Send(context.Background(), msg)
	assert.ErrorIs(t, err, shared.ErrNotDelivered, "connection refused")

	// The deadline expires while connecting (a non-routable address): Go's
	// transport reports the context error, not a dial error — still not
	// delivered, and transient.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = (&sendGridClient{api: newRealSendGridAPI("key", "http://10.255.255.1")}).Send(ctx, msg)
	assert.ErrorIs(t, err, shared.ErrNotDelivered, "connect timeout")
	assert.True(t, shared.IsTransient(err), "%v", err)
}

// One cached client is shared by concurrent sends: every recipient must get
// exactly their own message, and every caller its own message ID.
func TestSendGrid_ConcurrentSendsOnOneClient_NeverMixBodies(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	received := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Personalizations []struct {
				To []struct{ Email string } `json:"to"`
			} `json:"personalizations"`
			Content []struct{ Value string } `json:"content"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		to, content := body.Personalizations[0].To[0].Email, body.Content[0].Value
		assert.Equal(t, "body for "+to, content, "the body sent to a recipient is that recipient's")
		mu.Lock()
		received[to]++
		mu.Unlock()
		w.Header().Set("X-Message-Id", "id-"+to)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	client := &sendGridClient{api: newRealSendGridAPI("key", srv.URL)}
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			to := fmt.Sprintf("r%d@example.com", i)
			id, err := client.Send(context.Background(), sendemail.EmailMessage{
				SenderEmail: "a@example.com", ReceiverEmail: to, Body: "body for " + to,
			})
			assert.NoError(t, err)
			assert.Equal(t, "id-"+to, id)
		})
	}
	wg.Wait()
	assert.Len(t, received, n)
	for to, count := range received {
		assert.Equal(t, 1, count, to)
	}
}

// Redirects are never followed: the API key would go to the new host.
func TestSendGrid_RedirectNotFollowed(t *testing.T) {
	t.Parallel()
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	t.Cleanup(target.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)

	_, err := (&sendGridClient{api: newRealSendGridAPI("key", srv.URL)}).Send(context.Background(),
		sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"})
	require.Error(t, err)
	assert.False(t, leaked)
}
