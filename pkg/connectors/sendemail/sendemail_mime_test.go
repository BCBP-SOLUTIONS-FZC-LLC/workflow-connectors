package sendemail

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRawMIME_NoAttachments_SimpleBody(t *testing.T) {
	t.Parallel()

	raw, err := buildRawMIME(EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Subject:       "hi",
		Body:          "hello world",
		ContentType:   "text/plain",
	})
	require.NoError(t, err)
	msg := string(raw)
	assert.Contains(t, msg, "From: a@example.com")
	assert.Contains(t, msg, "To: b@example.com")
	assert.Contains(t, msg, "hello world")
	assert.NotContains(t, msg, "multipart/mixed")
}

func TestBuildRawMIME_ReceiverNameWithCRLF_CannotInjectHeaders(t *testing.T) {
	t.Parallel()

	raw, err := buildRawMIME(EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		ReceiverName:  "Evil\r\nBcc: attacker@evil.com",
		Subject:       "hi\r\nX-Injected: true",
		Body:          "hello",
	})
	require.NoError(t, err)
	msg := string(raw)
	lines := strings.Split(msg, "\r\n")
	for _, line := range lines {
		assert.False(t, strings.HasPrefix(line, "Bcc:"), "no line should be an injected Bcc header, got: %q", line)
		assert.False(t, strings.HasPrefix(line, "X-Injected:"), "no line should be an injected header, got: %q", line)
	}
}

func TestBuildRawMIME_WithAttachment_IsMultipart(t *testing.T) {
	t.Parallel()

	raw, err := buildRawMIME(EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Body:          "hello",
		Attachments: []EmailAttachment{
			{Filename: "a.pdf", ContentType: "application/pdf", Content: []byte("pdf-bytes")},
		},
	})
	require.NoError(t, err)
	msg := string(raw)
	assert.Contains(t, msg, "multipart/mixed")
	assert.Contains(t, msg, `filename="a.pdf"`)
	assert.Contains(t, msg, "application/pdf")
	assert.True(t, strings.Contains(msg, "hello"))
}
