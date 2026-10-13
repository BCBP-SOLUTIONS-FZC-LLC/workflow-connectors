package msgraph

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// An inline sendMail request carries at most 2,900,000 raw attachment bytes:
// base64-encoded, they stay under Graph's 4 MB request limit with room for
// the rest of the request.
func TestGraphClient_MaxAttachmentBytes(t *testing.T) {
	t.Parallel()
	var limiter sendemail.AttachmentLimiter = &graphClient{}
	assert.Equal(t, int64(2_900_000), limiter.MaxAttachmentBytes())
	assert.Less(t, (limiter.MaxAttachmentBytes()+2)/3*4, int64(maxRequestBytes))
}

// A request over Graph's inline limit — attachments within their own limit
// plus a large body — is refused before anything is sent: not delivered,
// permanent.
func TestGraphClient_Send_OversizedRequest_NotDeliveredBeforeSending(t *testing.T) {
	t.Parallel()

	fake := &fakeGraphAPI{}
	client := &graphClient{api: fake}
	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Body:          strings.Repeat("x", 500_000),
		Attachments:   []sendemail.EmailAttachment{{Filename: "a.bin", Content: make([]byte, maxAttachmentBytes)}},
	})
	require.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsPermanent(err))
	assert.ErrorContains(t, err, "inline limit")
	assert.Nil(t, fake.lastBody, "nothing was sent")
}

// An HTML body is sent as written, not HTML-escaped: <, > and & are one byte
// each against the inline limit, not the six of a \u003c escape. A body just
// under the limit in raw bytes is sent; json.Marshal would have escaped it
// to over 4 MB and refused it.
func TestGraphClient_Send_HTMLBodyNotEscaped(t *testing.T) {
	t.Parallel()

	fake := &fakeGraphAPI{}
	client := &graphClient{api: fake}
	html := strings.Repeat("<p>a&b</p>", 390_000) // 3,900,000 raw bytes; 1,950,000 of them <, > or &
	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		ContentType:   "text/html",
		Body:          html,
	})
	require.NoError(t, err)
	require.NotNil(t, fake.lastBody)
	assert.LessOrEqual(t, len(fake.lastBody), maxRequestBytes)
	assert.False(t, strings.Contains(string(fake.lastBody), `\u003c`), "not HTML-escaped")
	assert.False(t, strings.HasSuffix(string(fake.lastBody), "\n"), "no trailing newline")

	var req graphSendMailRequest
	require.NoError(t, json.Unmarshal(fake.lastBody, &req))
	assert.Equal(t, html, req.Message.Body.Content)
}
