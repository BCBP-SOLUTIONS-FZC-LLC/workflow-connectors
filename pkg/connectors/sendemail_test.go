package connectors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
)

func TestSendEmail_HappyPath(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockSendEmailClient()
	cfg := validConfig()
	cfg.SendEmailClient = client
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	out, err := all["send-email"].Execute(context.Background(), map[string]any{
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"subject":       "hi",
		"body":          "hello",
		"attachments":   []any{"doc-1", "doc-2"},
	})
	require.NoError(t, err)
	assert.Equal(t, true, out["sent"])
	assert.NotEmpty(t, out["messageId"])

	sent := client.Sent()
	require.Len(t, sent, 1)
	assert.Equal(t, "hello", sent[0].Body)
	assert.Equal(t, []string{"doc-1", "doc-2"}, sent[0].Attachments)

	client.Reset()
	assert.Empty(t, client.Sent())
}

func TestSendEmail_MissingBodyAndTemplate(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(validConfig())
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestSendEmail_ProviderError_Propagates(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockSendEmailClient()
	client.SetError(errors.New("provider down"))
	cfg := validConfig()
	cfg.SendEmailClient = client
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrUpstream))
}
