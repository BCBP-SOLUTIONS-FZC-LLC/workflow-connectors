package connectors_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/storage"
)

// This exercises the one thing only root's New() can: storage and send-email
// resolving document refs through the exact same DocRefStore instance.
func TestSendEmail_AttachmentFromStorage_ResolvesViaSharedDocRefStore(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	storageClient := storage.NewMockStorageClient()
	emailClient := sendemail.NewMockSendEmailClient()
	cfg.StorageProviders = map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			return storageClient, nil
		},
	}
	cfg.SendEmailProviders = map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			return emailClient, nil
		},
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	_, err = all["storage"].Execute(context.Background(), map[string]any{
		"operation":   "upload",
		"provider":    "aws-s3",
		"bucket":      "b1",
		"key":         "k1",
		"content":     "invoice contents",
		"contentType": "application/pdf",
	})
	require.NoError(t, err)

	fetchOut, err := all["storage"].Execute(context.Background(), map[string]any{
		"operation":      "fetch",
		"provider":       "aws-s3",
		"bucket":         "b1",
		"key":            "k1",
		"createDocument": true,
	})
	require.NoError(t, err)
	ref, ok := fetchOut["contentRef"].(string)
	require.True(t, ok)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "invoice attached",
		"attachments":   []any{ref},
	})
	require.NoError(t, err)

	sent := emailClient.Sent()
	require.Len(t, sent, 1)
	require.Len(t, sent[0].Attachments, 1)
	assert.Equal(t, "invoice contents", string(sent[0].Attachments[0].Content))
	assert.Equal(t, "application/pdf", sent[0].Attachments[0].ContentType)
	assert.Equal(t, "attachment-1.pdf", sent[0].Attachments[0].Filename)
}
