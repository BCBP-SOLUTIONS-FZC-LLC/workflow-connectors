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

// lostReplyStore commits each reservation and then loses the reply, like a
// connection that drops after the database committed.
type lostReplyStore struct {
	sendintent.Store
	commit  bool  // run the inner Reserve before failing
	steal   bool  // another caller re-stamps the intent before the read-back
	getErr  error // the read-back fails too
	getCtxs []context.Context
	getErrs []error // each read-back context's Err when it was used
}

var errLostReply = errors.New("connection reset while reading the reply")

func (s *lostReplyStore) Reserve(ctx context.Context, tenantID, messageKey string, resendFrom int, token string) (sendintent.Intent, bool, error) {
	if s.commit {
		if s.steal {
			token = "someone-else"
		}
		if _, _, err := s.Store.Reserve(ctx, tenantID, messageKey, resendFrom, token); err != nil {
			return sendintent.Intent{}, false, err
		}
	}
	return sendintent.Intent{}, false, errLostReply
}

func (s *lostReplyStore) Get(ctx context.Context, tenantID, messageKey string) (sendintent.Intent, bool, error) {
	s.getCtxs = append(s.getCtxs, ctx)
	s.getErrs = append(s.getErrs, ctx.Err())
	if s.getErr != nil {
		return sendintent.Intent{}, false, s.getErr
	}
	return s.Store.Get(ctx, tenantID, messageKey)
}

// A reservation that committed but whose reply was lost is recognised by
// its token: the call owns it, sends once and records the outcome.
func TestSendEmail_LostReservationReply_OwnTokenProceeds(t *testing.T) {
	t.Parallel()

	inner := sendintent.NewMemoryStore()
	store := &lostReplyStore{Store: inner, commit: true}
	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(store))
	ctx, cancel := context.WithCancel(shared.WithTenant(context.Background(), "t"))
	defer cancel()

	out, err := conn.Execute(ctx, emailInput("messageKey", "k"))
	require.NoError(t, err)
	assert.Equal(t, "accepted", out["deliveryOutcome"])
	assert.Len(t, client.Sent(), 1)

	got, found, err := inner.Get(context.Background(), "t", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusAccepted, got.Status, "the outcome is recorded on the recovered intent")
	assert.Equal(t, got.ID, out["sendIntentId"])

	require.Len(t, store.getCtxs, 1)
	_, hasDeadline := store.getCtxs[0].Deadline()
	assert.True(t, hasDeadline, "the read-back is bounded")
}

// The read-back is detached from the call: a cancelled call still learns
// whether it owns the reservation (and then records not_delivered).
func TestSendEmail_LostReservationReply_CancelledCallStillRecovers(t *testing.T) {
	t.Parallel()

	inner := sendintent.NewMemoryStore()
	store := &lostReplyStore{Store: inner, commit: true}
	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(store))
	ctx, cancel := context.WithCancel(shared.WithTenant(context.Background(), "t"))
	cancel()

	out, err := conn.Execute(ctx, emailInput("messageKey", "k"))
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Empty(t, client.Sent())
	require.Len(t, store.getErrs, 1)
	assert.NoError(t, store.getErrs[0], "the read-back is detached from the cancelled call")

	got, _, err := inner.Get(context.Background(), "t", "k")
	require.NoError(t, err)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status, "the recovered intent is not left pending")
}

// Without proof of ownership nothing is sent: the reservation never
// committed, another caller's token is on it, or the read-back failed.
func TestSendEmail_LostReservationReply_NotOursIsNotDelivered(t *testing.T) {
	t.Parallel()

	cases := map[string]*lostReplyStore{
		"never committed":  {commit: false},
		"another caller's": {commit: true, steal: true},
		"read-back failed": {commit: true, getErr: errors.New("database down")},
	}
	for name, store := range cases {
		store.Store = sendintent.NewMemoryStore()
		client := sendemail.NewMockSendEmailClient()
		conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(store))

		out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))
		assert.ErrorIs(t, err, shared.ErrNotDelivered, name)
		assert.ErrorIs(t, err, errLostReply, name)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"], name)
		assert.NotContains(t, out, "sendIntentId", name)
		assert.Empty(t, client.Sent(), name)
	}
}

// An intent of ours that is no longer pending (already finished by
// someone) is not ours to send.
func TestSendEmail_LostReservationReply_FinishedIntentIsNotOurs(t *testing.T) {
	t.Parallel()

	inner := sendintent.NewMemoryStore()
	store := &finishingStore{lostReplyStore: lostReplyStore{Store: inner, commit: true}}
	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(store))

	_, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.Empty(t, client.Sent())
}

// finishingStore records an outcome on the intent between the lost reply and
// the read-back.
type finishingStore struct{ lostReplyStore }

func (s *finishingStore) Get(ctx context.Context, tenantID, messageKey string) (sendintent.Intent, bool, error) {
	in, _, _ := s.Store.Get(ctx, tenantID, messageKey)
	_ = s.Record(ctx, in.ID, in.Attempts, sendintent.StatusUnknown, "", "")
	return s.Store.Get(ctx, tenantID, messageKey)
}
