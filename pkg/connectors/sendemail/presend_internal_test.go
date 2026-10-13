package sendemail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// The pre-send budget leaves room, inside StalePendingAfter, for the provider
// call, the outcome record and a margin.
func TestPreSendBudget_FitsInsideStalePendingAfter(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 11*time.Minute+50*time.Second, preSendBudget)
	assert.Less(t, preSendBudget+shared.ProviderHTTPTimeout+recordTimeout, sendintent.StalePendingAfter)
}

func presendInput() map[string]any {
	return map[string]any{"provider": "p", "senderEmail": "a@example.com", "receiverEmail": "b@example.com", "body": "hi", "messageKey": "k"}
}

// A call that reaches the send after its budget is not sent: not delivered,
// transient, and recorded so a retry re-reserves the intent and sends.
func TestExecute_PreSendBudgetExceeded_NotSentAndRetryable(t *testing.T) {
	t.Parallel()

	client := NewMockSendEmailClient()
	intents := sendintent.NewMemoryStore()
	c := New(map[string]ProviderConstructor{
		"p": func(context.Context, map[string]any) (ProviderClient, error) { return client, nil },
	}, nil, WithSendIntents(intents))
	start := time.Now()
	calls := 0
	c.now = func() time.Time {
		calls++
		if calls == 1 {
			return start // the reservation
		}
		return start.Add(preSendBudget + time.Second) // the check before the send
	}
	ctx := shared.WithTenant(context.Background(), "t")

	out, err := c.Execute(ctx, presendInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, shared.IsTransient(err), "nothing was sent: %v", err)
	assert.ErrorContains(t, err, "pre-send budget")
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Empty(t, client.Sent())
	got, _, getErr := intents.Get(ctx, "t", "k")
	require.NoError(t, getErr)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)

	c.now = time.Now
	_, err = c.Execute(ctx, presendInput())
	require.NoError(t, err)
	assert.Len(t, client.Sent(), 1)
}

// Building the client (and resolving attachments) runs under the budget's
// deadline once an intent is reserved; without an intent it does not.
func TestExecute_PreSendStage_BoundedByBudgetDeadline(t *testing.T) {
	t.Parallel()

	var sawDeadline bool
	blocking := func(ctx context.Context, _ map[string]any) (ProviderClient, error) {
		_, sawDeadline = ctx.Deadline()
		if !sawDeadline {
			return NewMockSendEmailClient(), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	intents := sendintent.NewMemoryStore()
	c := New(map[string]ProviderConstructor{"p": blocking}, nil, WithSendIntents(intents))
	c.preSendBudget = 50 * time.Millisecond
	ctx := shared.WithTenant(context.Background(), "t")

	_, err := c.Execute(ctx, presendInput())
	require.Error(t, err)
	assert.True(t, sawDeadline)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.True(t, shared.IsTransient(err))
	got, _, getErr := intents.Get(ctx, "t", "k")
	require.NoError(t, getErr)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)

	plain := presendInput()
	delete(plain, "messageKey")
	_, err = c.Execute(ctx, plain)
	require.NoError(t, err)
	assert.False(t, sawDeadline, "no reservation: the call's own context")
}
