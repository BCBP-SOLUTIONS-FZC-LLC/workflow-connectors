package gmail

import (
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// The attachment limit is below the shared 25 MiB, so the core enforces it,
// and attachments at the limit still encode within the message limit.
func TestGmailClient_MaxAttachmentBytes(t *testing.T) {
	t.Parallel()
	var limiter sendemail.AttachmentLimiter = &gmailClient{}
	assert.Equal(t, int64(17_000_000), limiter.MaxAttachmentBytes())
	assert.Less(t, limiter.MaxAttachmentBytes(), int64(shared.MaxAttachmentBytes))

	raw := buildRawMIME(sendemail.EmailMessage{
		SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Subject: "s", Body: strings.Repeat("b", 1_000_000),
		Attachments: []sendemail.EmailAttachment{{Filename: "a.bin", ContentType: "application/octet-stream", Content: make([]byte, maxAttachmentBytes)}},
	})
	assert.LessOrEqual(t, len(raw), maxMessageBytes, "a 1 MB body and the largest attachments fit")
	assert.Less(t, maxMessageBytes, 36_700_160, "under the API's media-upload limit")
}

// A message over Gmail's limit once encoded is refused before anything is
// sent: not delivered, permanent.
func TestGmailClient_Send_MessageOverLimit_NotSent(t *testing.T) {
	t.Parallel()

	fake := &fakeGmailMessagesAPI{messageID: "never"}
	_, err := (&gmailClient{api: fake}).Send(context.Background(), sendemail.EmailMessage{
		SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x",
		Attachments: []sendemail.EmailAttachment{{Filename: "a.bin", Content: make([]byte, 19_000_000)}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsPermanent(err))
	assert.ErrorContains(t, err, "message limit")
	assert.Nil(t, fake.lastRaw, "nothing was sent")
}

// headerLines returns the Subject header's physical lines.
func headerLines(t *testing.T, raw []byte) []string {
	t.Helper()
	text := string(raw)
	start := strings.Index(text, "Subject: ")
	require.GreaterOrEqual(t, start, 0)
	end := strings.Index(text[start:], "\r\nMIME-Version:")
	require.Greater(t, end, 0)
	return strings.Split(text[start:start+end], "\r\n")
}

// Every Subject line stays within RFC 5322's 998 characters, and the
// subject decodes back to the original text.
func TestBuildRawMIME_Subject_FoldedWithinLineLimit(t *testing.T) {
	t.Parallel()

	decoder := new(mime.WordDecoder)
	for name, subject := range map[string]string{
		"short ASCII":        "Invoice 42",
		"ASCII at the limit": strings.Repeat("a", 998-len("Subject: ")),
		"long ASCII":         strings.Repeat("a", 998),
		"long ASCII words":   strings.Repeat("word ", 199),
		"long non-ASCII":     strings.Repeat("é", 499),
	} {
		raw := buildRawMIME(sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Subject: subject, Body: "x"})
		lines := headerLines(t, raw)
		for i, line := range lines {
			assert.LessOrEqualf(t, len(line), maxHeaderLine, "%s: line %d", name, i)
			if i > 0 {
				assert.Truef(t, strings.HasPrefix(line, " "), "%s: a continuation line starts with whitespace", name)
			}
		}
		// Unfold (RFC 5322 §2.2.3), then decode the encoded-words.
		value := strings.TrimPrefix(strings.Join(lines, "\r\n"), "Subject: ")
		value = strings.ReplaceAll(value, "\r\n", "")
		decoded, err := decoder.DecodeHeader(value)
		require.NoError(t, err, name)
		assert.Equal(t, subject, decoded, name)
	}

	short := headerLines(t, buildRawMIME(sendemail.EmailMessage{Subject: "Invoice 42", Body: "x"}))
	assert.Equal(t, []string{"Subject: Invoice 42"}, short, "a short ASCII subject is unchanged")
}

// The token client never follows a redirect: the signed assertion (or
// client secret) must not be sent to another host.
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

	built, err := NewProvider(context.Background(), map[string]any{"serviceAccountKey": serviceAccountKey, "senderEmail": "a@example.com"})
	require.NoError(t, err)
	gc := built.(*gmailClient)
	require.NotNil(t, gc.tokenClient)
	assert.NotNil(t, gc.tokenClient.CheckRedirect)
	assert.NoError(t, gc.Close())
}
