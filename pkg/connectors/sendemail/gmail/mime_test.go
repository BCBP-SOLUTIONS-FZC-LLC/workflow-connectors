package gmail

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
)

func TestBuildRawMIME_NoAttachments_SimpleBody(t *testing.T) {
	t.Parallel()

	raw := buildRawMIME(sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Subject:       "hi",
		Body:          "hello world",
		ContentType:   "text/plain",
	})
	msg := string(raw)
	assert.Contains(t, msg, "From: a@example.com")
	assert.Contains(t, msg, "To: b@example.com")
	assert.Contains(t, msg, "hello world")
	assert.NotContains(t, msg, "multipart/mixed")
}

func TestBuildRawMIME_ReceiverNameWithCRLF_CannotInjectHeaders(t *testing.T) {
	t.Parallel()

	raw := buildRawMIME(sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		ReceiverName:  "Evil\r\nBcc: attacker@evil.com",
		Subject:       "hi\r\nX-Injected: true",
		Body:          "hello",
	})
	msg := string(raw)
	lines := strings.Split(msg, "\r\n")
	for _, line := range lines {
		assert.False(t, strings.HasPrefix(line, "Bcc:"), "no line should be an injected Bcc header, got: %q", line)
		assert.False(t, strings.HasPrefix(line, "X-Injected:"), "no line should be an injected header, got: %q", line)
	}
}

func TestBuildRawMIME_WithAttachment_IsMultipart(t *testing.T) {
	t.Parallel()

	raw := buildRawMIME(sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Body:          "hello",
		Attachments: []sendemail.EmailAttachment{
			{Filename: "a.pdf", ContentType: "application/pdf", Content: []byte("pdf-bytes")},
		},
	})
	msg := string(raw)
	assert.Contains(t, msg, "multipart/mixed")
	assert.Contains(t, msg, `filename="a.pdf"`)
	assert.Contains(t, msg, "application/pdf")
	assert.True(t, strings.Contains(msg, "hello"))
}

// An attachment's stored content type reaches a part header only parsed and
// re-formatted; one that does not parse is application/octet-stream, so it
// can never inject header text.
func TestBuildRawMIME_AttachmentContentType_IsNormalised(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"application/pdf":                     "application/pdf",
		"Text/CSV; Charset=UTF-8":             "text/csv; charset=UTF-8",
		"text/plain; name=\"a b.txt\"":        `text/plain; name="a b.txt"`,
		"":                                    "application/octet-stream",
		"application/pdf\r\nBcc: evil@x.test": "application/octet-stream",
		"not a media type":                    "application/octet-stream",
		"text/plain; charset=\"unterminated":  "application/octet-stream",
	}
	for stored, want := range cases {
		raw := string(buildRawMIME(sendemail.EmailMessage{
			SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "hello",
			Attachments: []sendemail.EmailAttachment{{Filename: "a", ContentType: stored, Content: []byte("x")}},
		}))
		assert.Containsf(t, raw, "\r\nContent-Type: "+want+"\r\n", "stored %q", stored)
		for _, line := range strings.Split(raw, "\r\n") {
			assert.Falsef(t, strings.HasPrefix(line, "Bcc:"), "stored %q injected a header", stored)
		}
	}
	assert.Equal(t, "application/octet-stream", attachmentMediaType("/"))
}

// The body's media type is text/html or text/plain, never the raw input.
func TestBuildRawMIME_BodyContentType_OnlyTheEnum(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{"text/html": "text/html", "text/plain": "text/plain", "": "text/plain", "text/html\r\nBcc: x@y.test": "text/plain"} {
		raw := string(buildRawMIME(sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "hello", ContentType: input}))
		assert.Containsf(t, raw, "Content-Type: "+want+"; charset=UTF-8\r\n", "input %q", input)
		assert.NotContains(t, raw, "Bcc:")
	}
}
