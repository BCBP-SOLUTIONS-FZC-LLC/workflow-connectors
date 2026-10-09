package msgraph

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func graphAgainst(t *testing.T, status int, delay time.Duration) (*graphClient, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return &graphClient{api: &realGraphAPI{httpClient: srv.Client(), baseURL: srv.URL}}, &hits
}

var msg = sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"}

func TestGraph_Outcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		delay  time.Duration
		want   error
	}{
		{"202 accepted", http.StatusAccepted, 0, nil},
		{"400 rejected", http.StatusBadRequest, 0, shared.ErrNotDelivered},
		{"503 unknown", http.StatusServiceUnavailable, 0, shared.ErrDeliveryUnknown},
		{"timeout unknown", http.StatusAccepted, 2 * time.Second, shared.ErrDeliveryUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, hits := graphAgainst(t, tc.status, tc.delay)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()

			_, err := c.Send(ctx, msg)
			if tc.want == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tc.want)
				assert.NotErrorIs(t, err, shared.ErrUpstream)
			}
			assert.Equal(t, int32(1), hits.Load(), "sendMail is attempted exactly once")
		})
	}
}

func TestGraph_RefusedToken_IsNotDelivered(t *testing.T) {
	t.Parallel()
	err := classifyError(&oauth2.RetrieveError{Response: &http.Response{StatusCode: 401}}, nil)
	assert.ErrorIs(t, err, shared.ErrNotDelivered, "sendMail was never called")
}

func TestGraph_TenantIDCannotRedirectTheTokenRequest(t *testing.T) {
	t.Parallel()
	for _, tenant := range []string{"evil.example/x?", "a/../../b", "x@evil.example", "x#y", "-bad", ""} {
		_, err := NewProvider(context.Background(), map[string]any{"tenantId": tenant, "clientId": "c", "clientSecret": "s"})
		assert.ErrorIs(t, err, shared.ErrValidation, tenant)
	}
	for _, tenant := range []string{"0b2f1e0c-7a43-4c4e-9f1e-2d6a8c0b9e11", "contoso.onmicrosoft.com"} {
		_, err := NewProvider(context.Background(), map[string]any{"tenantId": tenant, "clientId": "c", "clientSecret": "s"})
		assert.NoError(t, err, tenant)
	}
}

// A failed token request means sendMail was never made: not delivered, and
// classed by the token endpoint's answer.
func TestGraph_TokenFailure_Classes(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]shared.Class{401: shared.ClassPermanent, 400: shared.ClassPermanent, 429: shared.ClassTransient, 503: shared.ClassTransient} {
		err := classifyError(&oauth2.RetrieveError{Response: &http.Response{StatusCode: status}}, nil)
		assert.ErrorIs(t, err, shared.ErrNotDelivered)
		class, _ := shared.ClassOf(err)
		assert.Equal(t, want, class, status)
	}
	flattened := errors.New(`oauth2: cannot fetch token: Post "https://login.microsoftonline.com/x/oauth2/v2.0/token": dial tcp: lookup login.microsoftonline.com: no such host`)
	err := classifyError(flattened, nil)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsTransient(err), "a token request that could not be made is transient")
}
