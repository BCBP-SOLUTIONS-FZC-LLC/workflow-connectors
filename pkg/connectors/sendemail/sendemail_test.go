package sendemail_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors/shared"
)

func mockSendEmailProviders(client *sendemail.MockSendEmailClient) map[string]sendemail.ProviderConstructor {
	return map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			return client, nil
		},
	}
}

func TestSendEmail_HappyPath(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(mockSendEmailProviders(client), shared.NewDocRefStore())

	out, err := conn.Execute(context.Background(), map[string]any{
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

	conn := sendemail.New(mockSendEmailProviders(sendemail.NewMockSendEmailClient()), shared.NewDocRefStore())

	_, err := conn.Execute(context.Background(), map[string]any{
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestSendEmail_UnconfiguredProvider_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := sendemail.New(mockSendEmailProviders(sendemail.NewMockSendEmailClient()), shared.NewDocRefStore())

	_, err := conn.Execute(context.Background(), map[string]any{
		"provider":      "aws-ses",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestSendEmail_MissingBodyAndTemplate(t *testing.T) {
	t.Parallel()

	conn := sendemail.New(mockSendEmailProviders(sendemail.NewMockSendEmailClient()), shared.NewDocRefStore())

	_, err := conn.Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestSendEmail_TemplatelessProviderRequiresBody_IsValidationError(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	providers := map[string]sendemail.ProviderConstructor{
		"microsoft-365": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			return client, nil
		},
	}
	conn := sendemail.New(providers, shared.NewDocRefStore())

	_, err := conn.Execute(context.Background(), map[string]any{
		"provider":      "microsoft-365",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"templateId":    "welcome-template",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestSendEmail_UnresolvableAttachment_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := sendemail.New(mockSendEmailProviders(sendemail.NewMockSendEmailClient()), shared.NewDocRefStore())

	_, err := conn.Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
		"attachments":   []any{"no-such-ref"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestSendEmail_ProviderError_Propagates(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	client.SetError(errors.New("provider down"))
	conn := sendemail.New(mockSendEmailProviders(client), shared.NewDocRefStore())

	_, err := conn.Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
}

func TestSendEmail_ProviderConstructorError_WrappedAsUpstream(t *testing.T) {
	t.Parallel()

	providers := map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			return nil, errors.New("bad credentials")
		},
	}
	conn := sendemail.New(providers, shared.NewDocRefStore())

	_, err := conn.Execute(context.Background(), map[string]any{
		"provider":      "sendgrid",
		"senderEmail":   "a@example.com",
		"receiverEmail": "b@example.com",
		"body":          "hello",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrUpstream))
}

func TestSendEmail_SameCredentialsDifferentSender_DoesNotShareCachedClient(t *testing.T) {
	t.Parallel()

	var built []string
	providers := map[string]sendemail.ProviderConstructor{
		"google-workspace": func(_ context.Context, params map[string]any) (sendemail.ProviderClient, error) {
			client := sendemail.NewMockSendEmailClient()
			built = append(built, "built-for-a-call")
			return client, nil
		},
	}
	conn := sendemail.New(providers, shared.NewDocRefStore())

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
	_, err := conn.Execute(context.Background(), first)
	require.NoError(t, err)

	second := map[string]any{}
	for k, v := range baseInput {
		second[k] = v
	}
	second["senderEmail"] = "bob@example.com"
	_, err = conn.Execute(context.Background(), second)
	require.NoError(t, err)

	assert.Len(t, built, 2, "identical credentials but different senderEmail must not share a cached client — the client is bound to a specific impersonated mailbox at construction time")
}
