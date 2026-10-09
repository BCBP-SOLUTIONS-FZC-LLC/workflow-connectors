package ses

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// countingServer answers every request with status after delay, counting hits.
func countingServer(t *testing.T, status int, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"test"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func clientFor(url string) *sesClient {
	return newSESClient("AKID", "SECRET", "us-east-1", func(o *sesv2.Options) { o.BaseEndpoint = aws.String(url) })
}

var msg = sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"}

func TestSES_ServerError_SentExactlyOnce_OutcomeUnknown(t *testing.T) {
	t.Parallel()
	srv, hits := countingServer(t, http.StatusInternalServerError, 0)

	_, err := clientFor(srv.URL).Send(context.Background(), msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrDeliveryUnknown, "a 5xx may follow an acceptance")
	assert.Equal(t, int32(1), hits.Load(), "the SDK must not retry SendEmail: a retry could deliver a duplicate")
}

func TestSES_Throttled_SentExactlyOnce_NotDelivered(t *testing.T) {
	t.Parallel()
	srv, hits := countingServer(t, http.StatusTooManyRequests, 0)

	_, err := clientFor(srv.URL).Send(context.Background(), msg)

	assert.ErrorIs(t, err, shared.ErrNotDelivered, "a 4xx is a definitive rejection")
	assert.Equal(t, int32(1), hits.Load(), "no automatic retry even for throttling")
}

func TestSES_Timeout_SentExactlyOnce_OutcomeUnknown(t *testing.T) {
	t.Parallel()
	srv, hits := countingServer(t, http.StatusOK, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := clientFor(srv.URL).Send(ctx, msg)

	assert.ErrorIs(t, err, shared.ErrDeliveryUnknown, "a timeout after the request was sent is ambiguous")
	assert.Equal(t, int32(1), hits.Load())
}

func TestSES_ClientHasNoRetryer(t *testing.T) {
	t.Parallel()
	c := clientFor("http://127.0.0.1:1")
	api, ok := c.api.(*realSESAPI)
	require.True(t, ok)
	assert.Equal(t, 1, api.client.Options().Retryer.MaxAttempts(), "SendEmail is attempted once")
}
