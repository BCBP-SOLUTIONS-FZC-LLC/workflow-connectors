package sendemail_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestSendEmail_RejectsAnythingButOneBareAddress(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]any{
		"second recipient via comma": {"receiverEmail": "b@example.com, attacker@evil.com"},
		"display-name form":          {"receiverEmail": "Bob <b@example.com>"},
		"not an address":             {"receiverEmail": "not-an-email"},
		"bad sender":                 {"senderEmail": "a@example.com; x@y.com"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := sendemail.NewMockSendEmailClient()
			conn := sendemail.New(mockSendEmailProviders(client), docref.NewMemoryService())
			input := map[string]any{"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com", "body": "x"}
			for k, v := range override {
				input[k] = v
			}

			_, err := conn.Execute(context.Background(), input)
			require.Error(t, err)
			assert.True(t, errors.Is(err, shared.ErrValidation))
			assert.Empty(t, client.Sent(), "nothing may be sent")
		})
	}
}

func TestSendEmail_AttachmentsOverTotalLimit_IsValidationError(t *testing.T) {
	t.Parallel()

	ctx := shared.WithTenant(context.Background(), "tenant-1")
	docRefs := docref.NewMemoryService()
	half := make([]byte, shared.MaxAttachmentBytes/2+1)
	r1, err := docRefs.Create(ctx, "tenant-1", "application/pdf", half)
	require.NoError(t, err)
	r2, err := docRefs.Create(ctx, "tenant-1", "application/pdf", half)
	require.NoError(t, err)

	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(mockSendEmailProviders(client), docRefs)
	_, err = conn.Execute(ctx, map[string]any{
		"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com",
		"body": "x", "attachments": []any{r1.ID, r2.ID},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
	assert.Empty(t, client.Sent())
}

type closingEmailClient struct {
	*sendemail.MockSendEmailClient
	closes int
}

func (c *closingEmailClient) Close() error {
	c.closes++
	return nil
}

func TestSendEmail_ResetClients_ClosesIdleClientAndRebuilds(t *testing.T) {
	t.Parallel()

	var built []*closingEmailClient
	conn := sendemail.New(map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			c := &closingEmailClient{MockSendEmailClient: sendemail.NewMockSendEmailClient()}
			built = append(built, c)
			return c, nil
		},
	}, docref.NewMemoryService())
	input := map[string]any{"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com", "body": "x"}

	_, err := conn.Execute(context.Background(), input)
	require.NoError(t, err)
	conn.ResetClients()
	require.Len(t, built, 1)
	assert.Equal(t, 1, built[0].closes, "an idle client is closed when the cache is reset")

	_, err = conn.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Len(t, built, 2, "the next call builds a new client")
	assert.Zero(t, built[1].closes)
}
