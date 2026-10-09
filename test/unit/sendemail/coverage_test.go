package sendemail_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestSendEmail_MissingSenderOrReceiver_IsValidationError(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService())
	for _, field := range []string{"senderEmail", "receiverEmail"} {
		_, err := conn.Execute(context.Background(), emailInput(field, ""))
		assert.ErrorIs(t, err, shared.ErrValidation, field)
	}
	assert.Empty(t, client.Sent())
}

// failingIntentStore cannot reserve an intent.
type failingIntentStore struct{ sendintent.Store }

func (failingIntentStore) Reserve(context.Context, string, string, int, string) (sendintent.Intent, bool, error) {
	return sendintent.Intent{}, false, errors.New("intent store down")
}

func (failingIntentStore) Get(context.Context, string, string) (sendintent.Intent, bool, error) {
	return sendintent.Intent{}, false, errors.New("intent store down")
}

func TestSendEmail_SendIntentReserveFailure_IsNotDeliveredAndNothingSent(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(failingIntentStore{}))
	out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))

	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.Equal(t, false, out["sent"])
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Empty(t, client.Sent())
}

// A validation failure after the intent was reserved (here an unconfigured
// provider) sent nothing: not_delivered, in the output and on the intent, so
// the same messageKey may be tried again.
func TestSendEmail_ValidationFailureAfterReserve_RecordsNotDelivered(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k", "provider", "unconfigured"))

	assert.ErrorIs(t, err, shared.ErrValidation)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	got, ok, _ := intents.Get(context.Background(), "t", "k")
	require.True(t, ok)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.Contains(t, got.Detail, "not configured")
}

func TestSendEmail_Attachments_NeedDocRefServiceAndTenant(t *testing.T) {
	t.Parallel()

	ref := docref.NewID()

	noService := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), nil)
	_, err := noService.Execute(shared.WithTenant(context.Background(), "t"), emailInput("attachments", []any{ref}))
	assert.ErrorIs(t, err, shared.ErrValidation)

	noTenant := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService())
	_, err = noTenant.Execute(context.Background(), emailInput("attachments", []any{ref}))
	assert.ErrorIs(t, err, shared.ErrMissingTenant)
}

// failingRefStore cannot be read.
type failingRefStore struct {
	docref.Store
	err error
}

func (s failingRefStore) Get(context.Context, string, string) (docref.Ref, bool, error) {
	return docref.Ref{}, false, s.err
}

func TestSendEmail_DocRefStoreFailure_IsNotDelivered(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err       error
		transient bool
	}{
		"store reports itself unavailable": {err: docref.ErrUnavailable, transient: true},
		"other store failure":              {err: errors.New("valkey: protocol error"), transient: false},
	}
	for name, tc := range cases {
		client := sendemail.NewMockSendEmailClient()
		docRefs := docref.NewService(failingRefStore{err: tc.err}, docref.NewMemoryContent("test-documents"))
		conn := sendemail.New(providersFor(client), docRefs)

		_, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("attachments", []any{docref.NewID()}))

		require.Error(t, err, name)
		assert.ErrorIs(t, err, shared.ErrNotDelivered, name)
		assert.NotErrorIs(t, err, shared.ErrValidation, name)
		assert.Equal(t, tc.transient, shared.IsTransient(err), name)
		assert.Empty(t, client.Sent(), name)
	}
}
