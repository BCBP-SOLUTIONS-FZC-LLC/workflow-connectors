package sendemail_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// ---- A Reserve failure the store knows reserved nothing -------------------

// notReservedStore fails Reserve with an error the store marks
// sendintent.ErrNotReserved (the statement failed: nothing committed).
type notReservedStore struct {
	sendintent.Store
	gets int
}

func (s *notReservedStore) Reserve(context.Context, string, string, int, string) (sendintent.Intent, bool, error) {
	return sendintent.Intent{}, false, fmt.Errorf("%w: %w", sendintent.ErrNotReserved, shared.Transient("postgres lock not available", errors.New("lock timeout")))
}

func (s *notReservedStore) Get(ctx context.Context, tenantID, messageKey string) (sendintent.Intent, bool, error) {
	s.gets++
	return s.Store.Get(ctx, tenantID, messageKey)
}

// Nothing was reserved, so there is nothing to recover: the failure keeps its
// class and a transient one may be retried.
func TestSendEmail_ReserveFailedBeforeCommit_KeepsItsClass(t *testing.T) {
	t.Parallel()

	store := &notReservedStore{Store: sendintent.NewMemoryStore()}
	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(store))

	out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))
	require.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.ErrorIs(t, err, sendintent.ErrNotReserved)
	assert.True(t, autoRetryAllowed(err), "nothing was reserved: the retry cannot meet a pending intent")
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Zero(t, store.gets, "no read-back")
	assert.Empty(t, client.Sent())
}

// ---- An ambiguous Reserve failure that cannot be settled ------------------

// lateCommitStore loses Reserve's reply before the reservation is visible:
// the read-back finds nothing and the release matches nothing. commitLate
// then makes the reservation visible, as a commit that waited for a
// synchronous standby does.
type lateCommitStore struct {
	*sendintent.MemoryStore
	tenant, key, token string
}

func (s *lateCommitStore) Reserve(_ context.Context, tenantID, messageKey string, _ int, token string) (sendintent.Intent, bool, error) {
	s.tenant, s.key, s.token = tenantID, messageKey, token
	return sendintent.Intent{}, false, shared.Transient("connection reset", errLostReply)
}

func (s *lateCommitStore) commitLate(t *testing.T) {
	t.Helper()
	_, reserved, err := s.MemoryStore.Reserve(context.Background(), s.tenant, s.key, 0, s.token)
	require.NoError(t, err)
	require.True(t, reserved)
}

// Not finding the reservation, and releasing nothing, is no proof that it
// did not commit: the failure is not retried automatically (class unknown),
// since the reservation may surface pending and refuse the retry.
func TestSendEmail_AmbiguousReserve_NothingReleased_IsNotRetryable(t *testing.T) {
	t.Parallel()

	store := &lateCommitStore{MemoryStore: sendintent.NewMemoryStore()}
	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(store))
	ctx := shared.WithTenant(context.Background(), "t")

	out, err := conn.Execute(ctx, emailInput("messageKey", "k"))
	require.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.False(t, autoRetryAllowed(err), "a transient cause does not make an unsettled reservation retryable")
	class, _ := shared.ClassOf(err)
	assert.Equal(t, shared.ClassUnknown, class)
	var sendErr *sendemail.SendError
	require.ErrorAs(t, err, &sendErr)
	assert.True(t, sendErr.IntentUnrecorded)
	assert.Empty(t, client.Sent())

	// The commit surfaces: the reservation is pending, as feared.
	store.commitLate(t)
	got, found, err := store.Get(ctx, "t", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusPending, got.Status)
}

// ---- A panic after the reservation is recorded, then re-raised ------------

// The provider constructor panics: nothing was sent, so the intent records
// not_delivered (the key may be used again) and the panic propagates.
func TestSendEmail_ConstructorPanic_RecordsNotDeliveredAndRepanics(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	providers := map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { panic("constructor bug") },
	}
	conn := sendemail.New(providers, docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	assert.PanicsWithValue(t, "constructor bug", func() { _, _ = conn.Execute(ctx, emailInput("messageKey", "k")) })
	got, found, err := intents.Get(ctx, "t", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.Contains(t, got.Detail, "panicked before the send")
}

// panickingRefs panics when a ref is looked up.
type panickingRefs struct{ *docref.MemoryStore }

func (panickingRefs) Get(context.Context, string, string) (docref.Ref, bool, error) {
	panic("ref store bug")
}

// A panic while resolving attachments sent nothing: not_delivered.
func TestSendEmail_AttachmentPanic_RecordsNotDeliveredAndRepanics(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	client := sendemail.NewMockSendEmailClient()
	docRefs := docref.NewService(panickingRefs{docref.NewMemoryStore()}, docref.NewMemoryContent("b"))
	conn := sendemail.New(providersFor(client), docRefs, sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	assert.PanicsWithValue(t, "ref store bug", func() {
		_, _ = conn.Execute(ctx, emailInput("messageKey", "k", "attachments", []any{docref.NewID()}))
	})
	got, found, err := intents.Get(ctx, "t", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.Empty(t, client.Sent())
}

// panickingRecordStore panics when an outcome is recorded.
type panickingRecordStore struct {
	*sendintent.MemoryStore
	records int
}

func (s *panickingRecordStore) Record(context.Context, string, int, sendintent.Status, string, string) error {
	s.records++
	panic("record bug")
}

// A panic while the outcome is being recorded is re-raised as is: the
// outcome is not recorded a second time.
func TestSendEmail_PanicWhileRecording_IsNotRecordedAgain(t *testing.T) {
	t.Parallel()

	store := &panickingRecordStore{MemoryStore: sendintent.NewMemoryStore()}
	conn := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService(), sendemail.WithSendIntents(store))

	assert.PanicsWithValue(t, "record bug", func() {
		_, _ = conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))
	})
	assert.Equal(t, 1, store.records)
}
