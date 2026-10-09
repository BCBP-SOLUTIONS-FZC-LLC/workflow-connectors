package ses

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
)

// A templated send keeps its attachments: SES v2 takes them on the template.
func TestSESClient_Send_TemplateCarriesAttachments(t *testing.T) {
	t.Parallel()

	fake := &fakeSESAPI{messageID: "ses-t"}
	client := &sesClient{api: fake}
	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		TemplateID:    "welcome",
		Attachments:   []sendemail.EmailAttachment{{Filename: "attachment-1.pdf", ContentType: "application/pdf", Content: []byte("%PDF")}},
	})
	require.NoError(t, err)

	tmpl := fake.lastInput.Content.Template
	require.NotNil(t, tmpl)
	assert.Nil(t, fake.lastInput.Content.Simple)
	assert.Equal(t, "welcome", *tmpl.TemplateName)
	require.Len(t, tmpl.Attachments, 1)
	assert.Equal(t, "attachment-1.pdf", *tmpl.Attachments[0].FileName)
	assert.Equal(t, "application/pdf", *tmpl.Attachments[0].ContentType)
	assert.Equal(t, []byte("%PDF"), tmpl.Attachments[0].RawContent)
}

func TestSESClient_MaxAttachmentBytes(t *testing.T) {
	t.Parallel()
	var limiter sendemail.AttachmentLimiter = &sesClient{}
	assert.Equal(t, int64(25<<20), limiter.MaxAttachmentBytes())
}
