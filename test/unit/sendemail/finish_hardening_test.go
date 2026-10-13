package sendemail_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// panickingTextError is an adapter's own delivery-outcome error whose
// Error() panics (an adapter bug). It reports not delivered, so the core
// keeps it as is and reads its text directly (fmt would recover a panic
// inside a wrapping error's text, but not a direct call).
type panickingTextError struct{}

func (panickingTextError) Error() string { panic("error text bug") }

func (panickingTextError) Is(target error) bool { return target == shared.ErrNotDelivered }

// The outcome is recorded even when the error's text cannot be read: the
// detail falls back to a description of the panic, and the intent is not
// left pending.
func TestSendEmail_ErrorTextPanics_OutcomeStillRecorded(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(errorClient{err: panickingTextError{}}), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	out, err := conn.Execute(ctx, emailInput("messageKey", "k"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrNotDelivered))
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.NotContains(t, out, "sendIntentWarning")

	got, found, getErr := intents.Get(ctx, "t", "k")
	require.NoError(t, getErr)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status, "recorded, not left pending")
	assert.Contains(t, got.Detail, "Error panicked: error text bug")
}

// recordFailingStore reserves in memory and fails every Record.
type recordFailingStore struct{ *sendintent.MemoryStore }

func (recordFailingStore) Record(context.Context, string, int, sendintent.Status, string, string) error {
	return errors.New("database down")
}

// A validation failure found after the reservation whose not_delivered
// outcome cannot be recorded leaves the intent pending: it is reported as
// an unrecorded not-delivered SendError (class unknown, never retried
// automatically) — like every other unrecorded outcome — with the validation
// failure as its cause.
func TestSendEmail_PostReservationValidation_RecordFails_IsUnrecorded(t *testing.T) {
	t.Parallel()

	store := recordFailingStore{sendintent.NewMemoryStore()}
	client := sendemail.NewMockSendEmailClient()
	// No document-ref service: the attachment is refused after the reservation.
	conn := sendemail.New(providersFor(client), nil, sendemail.WithSendIntents(store))
	ctx := shared.WithTenant(context.Background(), "t")

	out, err := conn.Execute(ctx, emailInput("messageKey", "k", "attachments", []any{"docref:" + strings.Repeat("0", 32)}))
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.NotErrorIs(t, err, shared.ErrValidation, "the permanent class is hidden: the pending intent needs resolving")
	var sendErr *sendemail.SendError
	require.ErrorAs(t, err, &sendErr)
	assert.True(t, sendErr.IntentUnrecorded)
	assert.ErrorIs(t, sendErr.Err, shared.ErrValidation, "the cause is kept")
	class, _ := shared.ClassOf(err)
	assert.Equal(t, shared.ClassUnknown, class)
	assert.False(t, autoRetryAllowed(err))
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Contains(t, out["sendIntentWarning"], "database down")
	assert.Empty(t, client.Sent())

	got, _, getErr := store.Get(ctx, "t", "k")
	require.NoError(t, getErr)
	assert.Equal(t, sendintent.StatusPending, got.Status)
}

// The body and subject are capped before anything is reserved: 5 MiB of
// body, 998 bytes of subject.
func TestSendEmail_BodyAndSubjectSize_CheckedBeforeReservation(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		input map[string]any
		ok    bool
	}{
		"body at the limit":      {emailInput("body", strings.Repeat("a", 5<<20)), true},
		"body over the limit":    {emailInput("body", strings.Repeat("a", 5<<20+1)), false},
		"subject at the limit":   {emailInput("subject", strings.Repeat("s", 998)), true},
		"subject over the limit": {emailInput("subject", strings.Repeat("s", 999)), false},
	} {
		intents := sendintent.NewMemoryStore()
		client := sendemail.NewMockSendEmailClient()
		conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
		ctx := shared.WithTenant(context.Background(), "t")
		tc.input["messageKey"] = "k"

		out, err := conn.Execute(ctx, tc.input)
		_, found, getErr := intents.Get(ctx, "t", "k")
		require.NoError(t, getErr, name)
		if tc.ok {
			assert.NoError(t, err, name)
			assert.Len(t, client.Sent(), 1, name)
			continue
		}
		assert.ErrorIs(t, err, shared.ErrValidation, name)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"], name)
		assert.False(t, found, fmt.Sprintf("%s: nothing reserved", name))
		assert.Empty(t, client.Sent(), name)
	}
}
