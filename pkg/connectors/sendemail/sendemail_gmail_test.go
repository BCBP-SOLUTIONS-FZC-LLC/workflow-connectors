package sendemail

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

type fakeGmailMessagesAPI struct {
	lastRaw   string
	messageID string
	err       error
}

func (f *fakeGmailMessagesAPI) send(_ context.Context, raw string) (string, error) {
	f.lastRaw = raw
	if f.err != nil {
		return "", f.err
	}
	return f.messageID, nil
}

func TestGmailClient_Send_EncodesRawMessage(t *testing.T) {
	t.Parallel()

	fake := &fakeGmailMessagesAPI{messageID: "gmail-1"}
	client := &gmailClient{api: fake}

	id, err := client.Send(context.Background(), EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Subject:       "hi",
		Body:          "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "gmail-1", id)

	decoded, err := base64.URLEncoding.DecodeString(fake.lastRaw)
	require.NoError(t, err)
	assert.Contains(t, string(decoded), "hello")
}

func TestGmailClient_Send_APIError_Propagates(t *testing.T) {
	t.Parallel()

	client := &gmailClient{api: &fakeGmailMessagesAPI{err: errors.New("boom")}}
	_, err := client.Send(context.Background(), EmailMessage{SenderEmail: "a@example.com", Body: "x"})
	require.Error(t, err)
}

func TestNewGmailProvider_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := NewGmailProvider(context.Background(), map[string]any{"senderEmail": "a@example.com"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestNewGmailProvider_MissingSenderEmail_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := NewGmailProvider(context.Background(), map[string]any{"serviceAccountKey": "{}"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}
