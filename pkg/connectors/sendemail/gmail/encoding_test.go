package gmail

import (
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
)

func TestBuildRawMIME_BodyIsQuotedPrintable(t *testing.T) {
	t.Parallel()

	body := "Grüße — " + strings.Repeat("long line ", 200)
	raw := buildRawMIME(sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: body})
	msg := string(raw)

	assert.Contains(t, msg, "Content-Transfer-Encoding: quoted-printable")
	encoded := msg[strings.Index(msg, "\r\n\r\n")+4:]
	decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(encoded)))
	require.NoError(t, err)
	assert.Equal(t, body, string(decoded))
	for _, line := range strings.Split(msg, "\r\n") {
		assert.LessOrEqual(t, len(line), 998, "RFC 5322 line limit")
	}
}

func TestBuildRawMIME_AttachmentBase64IsWrapped(t *testing.T) {
	t.Parallel()

	raw := buildRawMIME(sendemail.EmailMessage{
		SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x",
		Attachments: []sendemail.EmailAttachment{{Filename: "a.bin", Content: make([]byte, 5000)}},
	})
	msg := string(raw)
	marker := "Content-Transfer-Encoding: base64\r\n"
	start := strings.Index(msg, marker)
	require.NotEqual(t, -1, start)
	part := msg[start+len(marker):]
	part = part[strings.Index(part, "\r\n\r\n")+4:]
	part = part[:strings.Index(part, "\r\n--")]
	lines := strings.Split(part, "\r\n")
	assert.Greater(t, len(lines), 1, "5000 bytes of base64 must span several lines")
	for _, line := range lines {
		assert.LessOrEqual(t, len(line), 76, "RFC 2045 base64 line length")
	}
}

func TestWrapBase64_RoundTrips(t *testing.T) {
	t.Parallel()

	content := []byte(strings.Repeat("abc", 100))
	wrapped := string(wrapBase64(content))
	assert.Equal(t, 76, strings.Index(wrapped, "\r\n"))
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(wrapped, "\r\n", ""))
	require.NoError(t, err)
	assert.Equal(t, content, decoded)
}
