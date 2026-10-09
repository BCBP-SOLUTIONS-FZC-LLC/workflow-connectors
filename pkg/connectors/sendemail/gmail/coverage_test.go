package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// serviceAccountKey is a structurally valid key: the private key is only
// parsed when a token is requested, which these tests never do.
const serviceAccountKey = `{
	"type": "service_account",
	"client_email": "svc@example.iam.gserviceaccount.com",
	"private_key": "not-parsed-until-a-token-is-requested",
	"token_uri": "https://oauth2.googleapis.com/token"
}`

func TestNewProvider_RejectsInvalidServiceAccountKeys(t *testing.T) {
	t.Parallel()

	for name, key := range map[string]string{
		"not a service account":   `{"type":"authorized_user"}`,
		"unparseable by x/oauth2": `{"type":"service_account","private_key":123}`,
	} {
		_, err := NewProvider(context.Background(), map[string]any{"serviceAccountKey": key, "senderEmail": "a@example.com"})
		assert.ErrorIs(t, err, shared.ErrValidation, name)
	}
}

func TestNewProvider_ValidKey_BuildsClosableClient(t *testing.T) {
	t.Parallel()

	client, err := NewProvider(context.Background(), map[string]any{"serviceAccountKey": serviceAccountKey, "senderEmail": "a@example.com"})
	require.NoError(t, err)
	gc, ok := client.(*gmailClient)
	require.True(t, ok)
	assert.NotNil(t, gc.httpClient)
	assert.IsType(t, &realGmailAPI{}, gc.api)
	assert.NoError(t, gc.Close())
}

func TestGmailClient_Close_WithoutTransport(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (&gmailClient{api: &fakeGmailMessagesAPI{}}).Close())
}

// The service cannot be built when the environment asks for mTLS outside the
// default universe; the failure is reported, not a nil client.
func TestNewProvider_ServiceBuildFailure(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_UNIVERSE_DOMAIN", "example.com")
	t.Setenv("GOOGLE_API_USE_MTLS_ENDPOINT", "always")

	client, err := NewProvider(context.Background(), map[string]any{"serviceAccountKey": serviceAccountKey, "senderEmail": "a@example.com"})
	require.Error(t, err)
	assert.Nil(t, client)
}

// gmailAgainst points the real Gmail API at srv.
func gmailAgainst(t *testing.T, httpClient *http.Client, srv *httptest.Server) *gmailClient {
	t.Helper()
	svc, err := gmail.NewService(context.Background(), option.WithHTTPClient(httpClient), option.WithEndpoint(srv.URL+"/"))
	require.NoError(t, err)
	return &gmailClient{api: &realGmailAPI{messages: svc.Users.Messages}}
}

func TestRealGmailAPI_Send(t *testing.T) {
	t.Parallel()

	var gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			Raw string `json:"raw"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		gotRaw = m.Raw
		if strings.Contains(r.URL.Path, "/users/me/messages/send") && m.Raw != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"gmail-42"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	id, err := gmailAgainst(t, srv.Client(), srv).Send(context.Background(), sendemail.EmailMessage{
		SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Subject: "hi", Body: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "gmail-42", id)
	decoded, err := base64.URLEncoding.DecodeString(gotRaw)
	require.NoError(t, err)
	assert.Contains(t, string(decoded), "hello")
}

func TestRealGmailAPI_Send_StatusErrors(t *testing.T) {
	t.Parallel()

	for status, want := range map[int]error{
		http.StatusBadRequest:          shared.ErrNotDelivered,
		http.StatusInternalServerError: shared.ErrDeliveryUnknown,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(status) + `,"message":"nope"}}`))
		}))
		_, err := gmailAgainst(t, srv.Client(), srv).Send(context.Background(), sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"})
		srv.Close()
		assert.ErrorIs(t, err, want, status)
	}
}

// A token endpoint that refuses the credentials: Gmail is never called and the
// failure carries the token endpoint's status.
func TestGmail_TokenRefused_NotDelivered(t *testing.T) {
	t.Parallel()

	var gmailHits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gmailHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(api.Close)
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	t.Cleanup(token.Close)

	cc := &clientcredentials.Config{ClientID: "id", ClientSecret: "secret", TokenURL: token.URL}
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, token.Client())
	httpClient := &http.Client{Transport: &oauth2.Transport{Source: cc.TokenSource(tokenCtx), Base: api.Client().Transport}}

	_, err := gmailAgainst(t, httpClient, api).Send(context.Background(), sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	var sendErr *sendemail.SendError
	require.ErrorAs(t, err, &sendErr)
	assert.Equal(t, http.StatusUnauthorized, sendErr.StatusCode)
	assert.Zero(t, gmailHits.Load(), "send is never called without a token")
}

func TestGmail_ClassifyTokenError_Variants(t *testing.T) {
	t.Parallel()

	_, notWritten := sendemail.TraceWrites(context.Background())

	withStatus := &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusTooManyRequests}}
	err := classifyError(withStatus, notWritten)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsRetryable(err), "a throttled token request is transient")

	flattened := errors.New(`Post "https://oauth2.googleapis.com/token": oauth2: cannot fetch token: connection refused`)
	err = classifyError(flattened, notWritten)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsRetryable(err), "a failed token transport is transient")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteQuotedPrintable_PropagatesWriterError(t *testing.T) {
	t.Parallel()

	// Longer than one quoted-printable line, so Write reaches the writer.
	assert.Error(t, writeQuotedPrintable(failingWriter{}, strings.Repeat("a", 200)))
}

func TestBuildRawMIME_SenderName_IsEncodedIntoFrom(t *testing.T) {
	t.Parallel()

	raw := string(buildRawMIME(sendemail.EmailMessage{SenderEmail: "a@example.com", SenderName: "Zoë", ReceiverEmail: "b@example.com", Body: "x"}))
	assert.Contains(t, raw, "From: =?utf-8?q?Zo=C3=AB?= <a@example.com>\r\n")
}
