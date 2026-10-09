package sendemail_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// resend: true must name the attempt it resends; resendAttempt alone, or a
// value that is not a whole number, is invalid input. Nothing is sent.
func TestSendEmail_Resend_RequiresResendAttempt(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]any{
		"resend without resendAttempt": emailInput("messageKey", "k", "resend", true),
		"resend with resendAttempt 0":  emailInput("messageKey", "k", "resend", true, "resendAttempt", 0),
		"resendAttempt without resend": emailInput("messageKey", "k", "resendAttempt", 1),
		"fractional resendAttempt":     emailInput("messageKey", "k", "resend", true, "resendAttempt", 1.5),
		"text resendAttempt":           emailInput("messageKey", "k", "resend", true, "resendAttempt", "1"),
		"bad json.Number":              emailInput("messageKey", "k", "resend", true, "resendAttempt", json.Number("x")),
	}
	for name, input := range cases {
		client := sendemail.NewMockSendEmailClient()
		conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(sendintent.NewMemoryStore()))
		out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), input)
		assert.ErrorIs(t, err, shared.ErrValidation, name)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"], name)
		assert.Empty(t, client.Sent(), name)
	}
}

// resendAttempt arrives as a Go int, an int64, a JSON float64 or a
// json.Number.
func TestSendEmail_Resend_AcceptsNumericForms(t *testing.T) {
	t.Parallel()

	for name, attempt := range map[string]any{"int": 1, "int64": int64(1), "float64": float64(1), "json.Number": json.Number("1")} {
		client := &lostResponseClient{}
		conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(sendintent.NewMemoryStore()))
		ctx := shared.WithTenant(context.Background(), "t")
		_, err := conn.Execute(ctx, emailInput("messageKey", "k"))
		require.ErrorIs(t, err, shared.ErrDeliveryUnknown, name)
		_, err = conn.Execute(ctx, emailInput("messageKey", "k", "resend", true, "resendAttempt", attempt))
		require.ErrorIs(t, err, shared.ErrDeliveryUnknown, name)
		assert.Equal(t, 2, client.count(), name)
	}
}

// A duplicate of an accepted intent succeeds without sending: a job
// redelivered after a crash before its acknowledgement is idempotent.
func TestSendEmail_DuplicateOfAccepted_Succeeds(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	first, err := conn.Execute(ctx, emailInput("messageKey", "welcome-1"))
	require.NoError(t, err)

	out, err := conn.Execute(ctx, emailInput("messageKey", "welcome-1"))
	require.NoError(t, err, "a duplicate of an accepted send is a success")
	assert.Equal(t, map[string]any{
		"sent":              false,
		"duplicate":         true,
		"sendIntentId":      first["sendIntentId"],
		"status":            "accepted",
		"attempts":          1,
		"providerMessageId": first["messageId"],
		"deliveryOutcome":   "accepted",
	}, out)
	assert.Len(t, client.Sent(), 1, "nothing sent again")
}

// A duplicate of a pending or unknown intent stays a permanent
// DuplicateRequestError, and its output carries the intent's state.
func TestSendEmail_DuplicateOfPendingOrUnknown_IsRefusedWithState(t *testing.T) {
	t.Parallel()

	ctx := shared.WithTenant(context.Background(), "t")

	// unknown
	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(&lostResponseClient{}), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	first, err := conn.Execute(ctx, emailInput("messageKey", "k"))
	require.ErrorIs(t, err, shared.ErrDeliveryUnknown)
	out, err := conn.Execute(ctx, emailInput("messageKey", "k"))
	require.ErrorIs(t, err, sendintent.ErrDuplicateRequest)
	assert.True(t, shared.IsPermanent(err))
	assert.Contains(t, err.Error(), "resendAttempt: 1")
	assert.Equal(t, map[string]any{
		"sent": false, "duplicate": true, "sendIntentId": first["sendIntentId"],
		"status": "unknown", "attempts": 1, "deliveryOutcome": "unknown",
	}, out)

	// pending: reserved, never finished (the worker stopped mid-send)
	_, reserved, err := intents.Reserve(ctx, "t", "stuck", 0, "other-worker")
	require.NoError(t, err)
	require.True(t, reserved)
	out, err = conn.Execute(ctx, emailInput("messageKey", "stuck"))
	var dup *sendintent.DuplicateRequestError
	require.ErrorAs(t, err, &dup)
	assert.Equal(t, sendintent.StatusPending, dup.Status)
	assert.Equal(t, 1, dup.Attempts)
	assert.Equal(t, "pending", out["status"])
	assert.Equal(t, "unknown", out["deliveryOutcome"], "a pending intent may have been sent")

	// an explicit resend of a pending attempt is refused too
	_, err = conn.Execute(ctx, emailInput("messageKey", "stuck", "resend", true, "resendAttempt", 1))
	assert.ErrorIs(t, err, sendintent.ErrDuplicateRequest)
}

// notDeliveredDuplicateStore reports a not_delivered intent without
// re-reserving it (a store that does not follow the contract): the duplicate
// still reports that outcome and sends nothing.
type notDeliveredDuplicateStore struct{ sendintent.Store }

func (notDeliveredDuplicateStore) Reserve(context.Context, string, string, int, string) (sendintent.Intent, bool, error) {
	return sendintent.Intent{ID: "i-1", Status: sendintent.StatusNotDelivered, Attempts: 3}, false, nil
}

func TestSendEmail_DuplicateOfNotDelivered_ReportsItsOutcome(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(notDeliveredDuplicateStore{}))
	out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))
	assert.ErrorIs(t, err, sendintent.ErrDuplicateRequest)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Equal(t, 3, out["attempts"])
	assert.Empty(t, client.Sent())
}
