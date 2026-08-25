package connectors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
)

func mockSendEmailConfig(client *connectors.MockSendEmailClient) connectors.Config {
	cfg := validConfig()
	cfg.SendEmailProviders = map[string]connectors.SendEmailProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (connectors.SendEmailProviderClient, error) {
			return client, nil
		},
	}
	return cfg
}

func TestSendEmail_HappyPath(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockSendEmailClient()
	all, err := connectors.New(mockSendEmailConfig(client))
	require.NoError(t, err)

	out, err := all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"subject":       "hi",
		"body":          "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, true, out["sent"])
	assert.NotEmpty(t, out["messageId"])

	sent := client.Sent()
	require.Len(t, sent, 1)
	assert.Equal(t, "hello", sent[0].Body)

	client.Reset()
	assert.Empty(t, client.Sent())
}

func TestSendEmail_MissingProvider_IsValidationError(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(mockSendEmailConfig(connectors.NewMockSendEmailClient()))
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestSendEmail_UnconfiguredProvider_IsValidationError(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(mockSendEmailConfig(connectors.NewMockSendEmailClient()))
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "aws-ses",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestSendEmail_MissingBodyAndTemplate(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(mockSendEmailConfig(connectors.NewMockSendEmailClient()))
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestSendEmail_TemplatelessProviderRequiresBody_IsValidationError(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	client := connectors.NewMockSendEmailClient()
	cfg.SendEmailProviders = map[string]connectors.SendEmailProviderConstructor{
		"microsoft-365": func(context.Context, map[string]any) (connectors.SendEmailProviderClient, error) {
			return client, nil
		},
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "microsoft-365",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"templateId":    "welcome-template",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestSendEmail_UnresolvableAttachment_IsValidationError(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(mockSendEmailConfig(connectors.NewMockSendEmailClient()))
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
		"attachments":   []any{"no-such-ref"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestSendEmail_ProviderError_Propagates(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockSendEmailClient()
	client.SetError(errors.New("provider down"))
	all, err := connectors.New(mockSendEmailConfig(client))
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrUpstream))
}

func TestSendEmail_ProviderConstructorError_WrappedAsUpstream(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.SendEmailProviders = map[string]connectors.SendEmailProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (connectors.SendEmailProviderClient, error) {
			return nil, errors.New("bad credentials")
		},
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	_, err = all["send-email"].Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrUpstream))
}

func TestSendEmail_SameCredentialsDifferentSender_DoesNotShareCachedClient(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	var built []string
	cfg.SendEmailProviders = map[string]connectors.SendEmailProviderConstructor{
		"google-workspace": func(_ context.Context, params map[string]any) (connectors.SendEmailProviderClient, error) {
			client := connectors.NewMockSendEmailClient()
			built = append(built, "built-for-a-call")
			return client, nil
		},
	}
	all, err := connectors.New(cfg)
	require.NoError(t, err)

	baseInput := map[string]any{
		"provider":          "google-workspace",
		"serviceAccountKey": "same-service-account",
		"receiverEmail":     "b@example.com",
		"body":              "hello",
	}

	first := map[string]any{}
	for k, v := range baseInput {
		first[k] = v
	}
	first["senderEmail"] = "alice@example.com"
	_, err = all["send-email"].Execute(context.Background(), first)
	require.NoError(t, err)

	second := map[string]any{}
	for k, v := range baseInput {
		second[k] = v
	}
	second["senderEmail"] = "bob@example.com"
	_, err = all["send-email"].Execute(context.Background(), second)
	require.NoError(t, err)

	assert.Len(t, built, 2, "identical credentials but different senderEmail must not share a cached client — the client is bound to a specific impersonated mailbox at construction time")
}

func TestSendEmail_AttachmentFromStorage_ResolvesViaSharedDocRefStore(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	storageClient := connectors.NewMockStorageClient()
	emailClient := connectors.NewMockSendEmailClient()
	cfg.StorageProviders = map[string]connectors.StorageProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (connectors.StorageProviderClient, error) {
			return storageClient, nil
		},
	}
	cfg.SendEmailProviders = map[string]connectors.SendEmailProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (connectors.SendEmailProviderClient, error) {
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
