package sendemail_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// ---- The intent is reserved before attachments are resolved ---------------

// A job redelivered after its email was accepted is the documented duplicate
// success even when its attachment ref has since expired, and nothing is
// downloaded or sent again.
func TestSendEmail_DuplicateOfAccepted_WithExpiredAttachment_Succeeds(t *testing.T) {
	t.Parallel()

	docRefs := docref.NewMemoryService()
	client := sendemail.NewMockSendEmailClient()
	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(client), docRefs, sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	ref := attachmentOf(t, docRefs, 16)
	input := emailInput("messageKey", "invoice-1", "attachments", []any{ref})
	first, err := conn.Execute(ctx, input)
	require.NoError(t, err)

	require.NoError(t, docRefs.Delete(ctx, "t", ref), "the ref expires")
	out, err := conn.Execute(ctx, input)
	require.NoError(t, err, "a duplicate of an accepted send succeeds, attachments or not")
	assert.Equal(t, true, out["duplicate"])
	assert.Equal(t, first["messageId"], out["messageId"])
	assert.Len(t, client.Sent(), 1, "nothing sent again")
}

// An attachment that cannot be resolved after the intent was reserved sent
// nothing: the intent records not_delivered, so the key may be used again.
func TestSendEmail_AttachmentFailureAfterReserve_RecordsNotDelivered(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	out, err := conn.Execute(ctx, emailInput("messageKey", "k", "attachments", []any{docref.NewID()}))
	assert.ErrorIs(t, err, shared.ErrValidation)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.NotEmpty(t, out["sendIntentId"])
	got, found, err := intents.Get(ctx, "t", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.Empty(t, client.Sent())
}

// ---- A lost reservation reply whose read-back failed is released ----------

// releasingStore loses Reserve's reply after committing, fails the
// read-back, and releases through the inner store (or fails to).
type releasingStore struct {
	lostReplyStore
	releaseErr  error
	releaseCtxs []context.Context
}

func (s *releasingStore) Release(ctx context.Context, tenantID, messageKey, token string) (bool, error) {
	s.releaseCtxs = append(s.releaseCtxs, ctx)
	if s.releaseErr != nil {
		return false, s.releaseErr
	}
	return s.Store.(sendintent.Releaser).Release(ctx, tenantID, messageKey, token)
}

// transientLostReply is a lost reply the store classed transient (a
// connection reset).
type transientLostReplyStore struct{ *releasingStore }

func (s transientLostReplyStore) Reserve(ctx context.Context, tenantID, messageKey string, resendFrom int, token string) (sendintent.Intent, bool, error) {
	_, _, err := s.releasingStore.Reserve(ctx, tenantID, messageKey, resendFrom, token)
	return sendintent.Intent{}, false, shared.Transient("connection reset", err)
}

// The reservation committed unseen and the read-back failed: it is released
// by its token (not_delivered), so the transient failure may be retried and
// the retry is not refused as a duplicate.
func TestSendEmail_LostReplyAndFailedReadBack_ReleasesTheReservation(t *testing.T) {
	t.Parallel()

	inner := sendintent.NewMemoryStore()
	store := &releasingStore{lostReplyStore: lostReplyStore{Store: inner, commit: true, getErr: errors.New("database down")}}
	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(transientLostReplyStore{store}))
	ctx, cancel := context.WithCancel(shared.WithTenant(context.Background(), "t"))
	defer cancel()

	_, err := conn.Execute(ctx, emailInput("messageKey", "k"))
	require.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.True(t, autoRetryAllowed(err), "released: a retry cannot meet a pending intent of this call")
	assert.Empty(t, client.Sent())

	got, found, err := inner.Get(context.Background(), "t", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.Equal(t, sendintent.ReleasedDetail, got.Detail)
	require.Len(t, store.releaseCtxs, 1)
	_, bounded := store.releaseCtxs[0].Deadline()
	assert.True(t, bounded, "the release is bounded")

	// The retry re-reserves the released intent and sends.
	retry := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(inner))
	_, err = retry.Execute(ctx, emailInput("messageKey", "k"))
	require.NoError(t, err)
	assert.Len(t, client.Sent(), 1)
}

// When the release fails too (or the store cannot release), the reservation
// may be left pending: the failure is not retried automatically (unknown
// class), since a retry would only meet a duplicate.
func TestSendEmail_LostReplyReadBackAndReleaseFail_IsNotRetryable(t *testing.T) {
	t.Parallel()

	cases := map[string]sendintent.Store{
		"release fails": transientLostReplyStore{&releasingStore{
			lostReplyStore: lostReplyStore{Store: sendintent.NewMemoryStore(), commit: true, getErr: errors.New("database down")},
			releaseErr:     errors.New("database still down"),
		}},
		"store cannot release": &lostReplyStore{Store: sendintent.NewMemoryStore(), commit: true, getErr: errors.New("database down")},
	}
	for name, store := range cases {
		client := sendemail.NewMockSendEmailClient()
		conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(store))
		out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))

		require.ErrorIs(t, err, shared.ErrNotDelivered, name)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"], name)
		assert.False(t, shared.IsRetryable(err), name)
		assert.False(t, autoRetryAllowed(err), name)
		class, _ := shared.ClassOf(err)
		assert.Equal(t, shared.ClassUnknown, class, name)
		var sendErr *sendemail.SendError
		require.ErrorAs(t, err, &sendErr, name)
		assert.True(t, sendErr.IntentUnrecorded, name)
		assert.Empty(t, client.Sent(), name)
	}
}

// ---- What is recorded on the intent is storable ---------------------------

// errorClient fails every send with err.
type errorClient struct{ err error }

func (c errorClient) Send(context.Context, sendemail.EmailMessage) (string, error) { return "", c.err }

// idClient accepts every send with id.
type idClient struct{ id string }

func (c idClient) Send(context.Context, sendemail.EmailMessage) (string, error) { return c.id, nil }

// A provider error with invalid UTF-8 (a response cut mid-rune), NUL bytes
// and a long text is recorded valid, NUL-free and capped, so PostgreSQL text
// accepts it and the intent is not left pending. A provider message ID is
// recorded, and returned, the same way.
func TestSendEmail_RecordedDetailAndMessageID_AreStorable(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	ctx := shared.WithTenant(context.Background(), "t")
	bad := "provider said \xff\xfe\x00 nul" + strings.Repeat("€", 4000)
	conn := sendemail.New(providersFor(errorClient{sendemail.NotDelivered("sendgrid", 400, errors.New(bad))}),
		docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	_, err := conn.Execute(ctx, emailInput("messageKey", "failed"))
	require.ErrorIs(t, err, shared.ErrNotDelivered)

	got, _, err := intents.Get(ctx, "t", "failed")
	require.NoError(t, err)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.True(t, utf8.ValidString(got.Detail))
	assert.NotContains(t, got.Detail, "\x00")
	assert.LessOrEqual(t, len(got.Detail), 4096)
	assert.Contains(t, got.Detail, "provider said")

	conn = sendemail.New(providersFor(idClient{"id-\x00\xff"}), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	out, err := conn.Execute(ctx, emailInput("messageKey", "accepted"))
	require.NoError(t, err)
	assert.Equal(t, "id-�", out["messageId"], "the output is storable as jsonb too: no NUL, valid UTF-8")
	got, _, err = intents.Get(ctx, "t", "accepted")
	require.NoError(t, err)
	assert.Equal(t, "id-�", got.ProviderMessageID)
	assert.Equal(t, out["messageId"], got.ProviderMessageID, "the output and the intent agree")

	// Without an intent the output is sanitised all the same.
	conn = sendemail.New(providersFor(idClient{"id-\x00\xff"}), docref.NewMemoryService())
	out, err = conn.Execute(ctx, emailInput())
	require.NoError(t, err)
	assert.Equal(t, "id-�", out["messageId"])
}

// ---- Input validation before the store is touched -------------------------

func TestSendEmail_InvalidMessageKeyOrResendAttempt_IsValidationError(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]any{
		"messageKey over 512 bytes":  emailInput("messageKey", strings.Repeat("k", 513)),
		"messageKey invalid UTF-8":   emailInput("messageKey", "k\xff"),
		"messageKey with NUL":        emailInput("messageKey", "k\x00"),
		"messageKey with newline":    emailInput("messageKey", "k\n"),
		"resendAttempt negative":     emailInput("messageKey", "k", "resend", true, "resendAttempt", -1),
		"resendAttempt over int32":   emailInput("messageKey", "k", "resend", true, "resendAttempt", int64(math.MaxInt32)+1),
		"resendAttempt huge float":   emailInput("messageKey", "k", "resend", true, "resendAttempt", 1e300),
		"resendAttempt huge number":  emailInput("messageKey", "k", "resend", true, "resendAttempt", json.Number("99999999999")),
		"resendAttempt 0 w/o resend": emailInput("messageKey", "k", "resendAttempt", 0),
	}
	for name, input := range cases {
		intents := sendintent.NewMemoryStore()
		client := sendemail.NewMockSendEmailClient()
		conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
		ctx := shared.WithTenant(context.Background(), "t")

		out, err := conn.Execute(ctx, input)
		assert.ErrorIs(t, err, shared.ErrValidation, name)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"], name)
		assert.NotContains(t, out, "sendIntentId", name)
		_, found, getErr := intents.Get(ctx, "t", input["messageKey"].(string))
		require.NoError(t, getErr, name)
		assert.False(t, found, "%s: the store is not touched", name)
		assert.Empty(t, client.Sent(), name)
	}

	// The bounds themselves are valid.
	conn := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService(), sendemail.WithSendIntents(sendintent.NewMemoryStore()))
	_, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", strings.Repeat("é", 256)))
	assert.NoError(t, err, "512 bytes of UTF-8")
}

// contentType is the registry's enum (text/plain, text/html, or empty).
func TestSendEmail_ContentType_OnlyTheRegistryEnum(t *testing.T) {
	t.Parallel()

	client := sendemail.NewMockSendEmailClient()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService())
	for _, ct := range []string{"text/csv", "text/html; charset=utf-8", "text/html\r\nBcc: x@y.test", "TEXT/HTML"} {
		out, err := conn.Execute(context.Background(), emailInput("contentType", ct))
		assert.ErrorIsf(t, err, shared.ErrValidation, "contentType %q", ct)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	}
	assert.Empty(t, client.Sent())
	for _, ct := range []string{"", "text/plain", "text/html"} {
		_, err := conn.Execute(context.Background(), emailInput("contentType", ct))
		assert.NoErrorf(t, err, "contentType %q", ct)
	}
}

// ---- A panicking client ---------------------------------------------------

type panicClient struct{}

func (panicClient) Send(context.Context, sendemail.EmailMessage) (string, error) {
	panic("adapter bug")
}

// A panic in the provider client is re-raised (a bug is not hidden), after
// its outcome — unknown — is recorded, so the intent is not left pending.
func TestSendEmail_ClientPanic_RecordsUnknownAndRepanics(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(panicClient{}), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "t")

	assert.PanicsWithValue(t, "adapter bug", func() {
		_, _ = conn.Execute(ctx, emailInput("messageKey", "k"))
	})
	got, found, err := intents.Get(ctx, "t", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusUnknown, got.Status)
	assert.Contains(t, got.Detail, "panicked")

	// Without an intent the panic still propagates.
	plain := sendemail.New(providersFor(panicClient{}), docref.NewMemoryService())
	assert.Panics(t, func() { _, _ = plain.Execute(context.Background(), emailInput()) })
}

// A missing or unconfigured provider is refused with the other input checks,
// before the messageKey is reserved: it used to be checked only when the
// client was built, after the reservation, leaving a not_delivered intent.
func TestSendEmail_InvalidProvider_ReservesNothing(t *testing.T) {
	t.Parallel()

	for name, provider := range map[string]any{"missing": "", "unconfigured": "aws-ses"} {
		intents := sendintent.NewMemoryStore()
		client := sendemail.NewMockSendEmailClient()
		conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
		ctx := shared.WithTenant(context.Background(), "t")

		out, err := conn.Execute(ctx, emailInput("provider", provider, "messageKey", "k"))
		assert.ErrorIs(t, err, shared.ErrValidation, name)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"], name)
		assert.NotContains(t, out, "sendIntentId", name)
		_, found, getErr := intents.Get(ctx, "t", "k")
		require.NoError(t, getErr, name)
		assert.False(t, found, "%s: no intent is reserved", name)
		assert.Empty(t, client.Sent(), name)
	}
}
