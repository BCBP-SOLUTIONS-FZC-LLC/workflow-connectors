package sendgrid

import (
	"context"
	"errors"
	"testing"

	"github.com/sendgrid/sendgrid-go/helpers/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

type fakeSendGridAPI struct {
	lastMail   *mail.SGMailV3
	statusCode int
	messageID  string
	err        error
}

func (f *fakeSendGridAPI) send(_ context.Context, m *mail.SGMailV3) (int, string, string, error) {
	f.lastMail = m
	if f.err != nil {
		return 0, "", "", f.err
	}
	return f.statusCode, "", f.messageID, nil
}

func TestSendGridClient_Send_UsesTemplateWhenSet(t *testing.T) {
	t.Parallel()

	fake := &fakeSendGridAPI{statusCode: 202, messageID: "msg-1"}
	client := &sendGridClient{api: fake}

	id, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		TemplateID:    "tmpl-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "msg-1", id)
	assert.Equal(t, "tmpl-1", fake.lastMail.TemplateID)
	assert.Empty(t, fake.lastMail.Content)
}

func TestSendGridClient_Send_AttachesContentAndFiles(t *testing.T) {
	t.Parallel()

	fake := &fakeSendGridAPI{statusCode: 202}
	client := &sendGridClient{api: fake}

	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Body:          "hello",
		ContentType:   "text/plain",
		Attachments:   []sendemail.EmailAttachment{{Filename: "a.pdf", ContentType: "application/pdf", Content: []byte("x")}},
	})
	require.NoError(t, err)
	require.Len(t, fake.lastMail.Content, 1)
	assert.Equal(t, "hello", fake.lastMail.Content[0].Value)
	require.Len(t, fake.lastMail.Attachments, 1)
	assert.Equal(t, "a.pdf", fake.lastMail.Attachments[0].Filename)
}

func TestSendGridClient_Send_NonSuccessStatus_IsError(t *testing.T) {
	t.Parallel()

	client := &sendGridClient{api: &fakeSendGridAPI{statusCode: 400}}
	_, err := client.Send(context.Background(), sendemail.EmailMessage{Body: "x"})
	require.Error(t, err)
}

func TestSendGridClient_Send_APIError_Propagates(t *testing.T) {
	t.Parallel()

	client := &sendGridClient{api: &fakeSendGridAPI{err: errors.New("boom")}}
	_, err := client.Send(context.Background(), sendemail.EmailMessage{Body: "x"})
	require.Error(t, err)
}

func TestNewSendGridProvider_MissingAPIKey_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := NewProvider(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestNewSendGridProvider_BuildsClient(t *testing.T) {
	t.Parallel()

	client, err := NewProvider(context.Background(), map[string]any{"apiKey": "sg-key"})
	require.NoError(t, err)
	assert.NotNil(t, client)
}
