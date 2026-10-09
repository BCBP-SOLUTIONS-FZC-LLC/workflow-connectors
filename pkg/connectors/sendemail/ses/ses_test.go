package ses

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

type fakeSESAPI struct {
	lastInput *sesv2.SendEmailInput
	messageID string
	err       error
}

func (f *fakeSESAPI) sendEmail(_ context.Context, in *sesv2.SendEmailInput) (string, error) {
	f.lastInput = in
	if f.err != nil {
		return "", f.err
	}
	return f.messageID, nil
}

func TestSESClient_Send_SimpleBody(t *testing.T) {
	t.Parallel()

	fake := &fakeSESAPI{messageID: "ses-1"}
	client := &sesClient{api: fake}

	id, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Subject:       "hi",
		Body:          "hello",
		ContentType:   "text/plain",
	})
	require.NoError(t, err)
	assert.Equal(t, "ses-1", id)
	require.NotNil(t, fake.lastInput.Content.Simple)
	assert.Equal(t, "hello", *fake.lastInput.Content.Simple.Body.Text.Data)
	assert.Nil(t, fake.lastInput.Content.Simple.Body.Html)
}

func TestSESClient_Send_HTMLBody(t *testing.T) {
	t.Parallel()

	fake := &fakeSESAPI{}
	client := &sesClient{api: fake}

	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Body:          "<p>hi</p>",
		ContentType:   "text/html",
	})
	require.NoError(t, err)
	assert.Equal(t, "<p>hi</p>", *fake.lastInput.Content.Simple.Body.Html.Data)
}

func TestSESClient_Send_Template(t *testing.T) {
	t.Parallel()

	fake := &fakeSESAPI{}
	client := &sesClient{api: fake}

	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		TemplateID:    "welcome",
	})
	require.NoError(t, err)
	require.NotNil(t, fake.lastInput.Content.Template)
	assert.Equal(t, "welcome", *fake.lastInput.Content.Template.TemplateName)
	assert.Nil(t, fake.lastInput.Content.Simple)
}

func TestSESClient_Send_WithAttachments(t *testing.T) {
	t.Parallel()

	fake := &fakeSESAPI{}
	client := &sesClient{api: fake}

	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Body:          "hello",
		Attachments:   []sendemail.EmailAttachment{{Filename: "a.pdf", ContentType: "application/pdf", Content: []byte("x")}},
	})
	require.NoError(t, err)
	require.Len(t, fake.lastInput.Content.Simple.Attachments, 1)
	assert.Equal(t, "a.pdf", *fake.lastInput.Content.Simple.Attachments[0].FileName)
}

func TestSESClient_Send_APIError_Propagates(t *testing.T) {
	t.Parallel()

	client := &sesClient{api: &fakeSESAPI{err: errors.New("boom")}}
	_, err := client.Send(context.Background(), sendemail.EmailMessage{Body: "x"})
	require.Error(t, err)
}

func TestNewSESProvider_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := NewProvider(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestNewSESProvider_BuildsClient(t *testing.T) {
	t.Parallel()

	client, err := NewProvider(context.Background(), map[string]any{
		"accessKey": "AKIA...",
		"secretKey": "secret",
		"region":    "us-east-1",
	})
	require.NoError(t, err)
	assert.NotNil(t, client)
}
