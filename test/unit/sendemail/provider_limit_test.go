package sendemail_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// limitedClient accepts at most limit raw attachment bytes.
type limitedClient struct {
	*sendemail.MockSendEmailClient
	limit int64
}

func (c limitedClient) MaxAttachmentBytes() int64 { return c.limit }

func attachmentOf(t *testing.T, docRefs *docref.Service, size int) string {
	t.Helper()
	ref, err := docRefs.Create(shared.WithTenant(context.Background(), "t"), "t", "application/pdf", make([]byte, size))
	require.NoError(t, err)
	return ref.ID
}

// The core enforces a provider's own, smaller limit before sending: a
// permanent validation failure, not delivered, recorded on the intent.
func TestSendEmail_ProviderAttachmentLimit_EnforcedBeforeSend(t *testing.T) {
	t.Parallel()

	docRefs := docref.NewMemoryService()
	client := limitedClient{MockSendEmailClient: sendemail.NewMockSendEmailClient(), limit: 3 << 20}
	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(client), docRefs, sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	over := []any{attachmentOf(t, docRefs, 2<<20), attachmentOf(t, docRefs, 1<<20+1)}
	out, err := conn.Execute(ctx, emailInput("attachments", over, "messageKey", "k"))
	require.ErrorIs(t, err, shared.ErrValidation)
	assert.True(t, shared.IsPermanent(err))
	assert.Contains(t, err.Error(), `provider "sendgrid"'s 3145728-byte limit`)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Empty(t, client.Sent())
	got, _, err := intents.Get(ctx, "t", "k")
	require.NoError(t, err)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)

	exact := []any{attachmentOf(t, docRefs, 2<<20), attachmentOf(t, docRefs, 1<<20)}
	_, err = conn.Execute(ctx, emailInput("attachments", exact))
	require.NoError(t, err, "exactly the limit is accepted")
	assert.Len(t, client.Sent(), 1)
}

// A provider limit at or above the shared limit, or none (0), leaves the
// shared limit in force.
func TestSendEmail_ProviderAttachmentLimit_NeverRaisesSharedLimit(t *testing.T) {
	t.Parallel()

	for _, limit := range []int64{0, shared.MaxAttachmentBytes, 2 * shared.MaxAttachmentBytes} {
		docRefs := docref.NewMemoryService()
		client := limitedClient{MockSendEmailClient: sendemail.NewMockSendEmailClient(), limit: limit}
		conn := sendemail.New(providersFor(client), docRefs)
		_, err := conn.Execute(shared.WithTenant(context.Background(), "t"),
			emailInput("attachments", []any{attachmentOf(t, docRefs, 4<<20)}))
		require.NoError(t, err, "limit %d", limit)
	}
}
