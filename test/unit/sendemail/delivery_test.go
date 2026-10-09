package sendemail_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// lostResponseClient models a provider that ACCEPTS every message (it is
// delivered) but whose success response never arrives: the caller sees a
// timeout. This is the ambiguous case exactly-once delivery cannot handle.
type lostResponseClient struct {
	mu        sync.Mutex
	delivered []sendemail.EmailMessage
}

func (c *lostResponseClient) Send(_ context.Context, msg sendemail.EmailMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delivered = append(c.delivered, msg)
	return "", context.DeadlineExceeded
}

func (c *lostResponseClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.delivered)
}

func providersFor(client sendemail.ProviderClient) map[string]sendemail.ProviderConstructor {
	return map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return client, nil },
	}
}

func emailInput(extra ...any) map[string]any {
	in := map[string]any{"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com", "body": "hello"}
	for i := 0; i < len(extra); i += 2 {
		in[extra[i].(string)] = extra[i+1]
	}
	return in
}

// autoRetryAllowed mirrors connectors.DecideRetry for send-email without an
// import cycle: a transient error that was provably not delivered.
func autoRetryAllowed(err error) bool {
	return registry.All()[registry.TypeSendEmail].Retry == registry.RetryPolicyNotDelivered &&
		shared.IsRetryable(err) && errors.Is(err, shared.ErrNotDelivered)
}

// ---- 1. The automatic retry path never retries an ambiguous send ----------

func TestSendEmail_AmbiguousOrUnclassifiedFailures_AreNeverAutomaticallyRetryable(t *testing.T) {
	t.Parallel()

	failing := map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			return nil, errors.New("cannot build client")
		},
	}
	upstreamWrapped := sendemail.NewMockSendEmailClient()
	upstreamWrapped.SetError(fmt.Errorf("%w: custom client error", shared.ErrUpstream))

	cases := map[string]*sendemail.Connector{
		"client build failure":                sendemail.New(failing, docref.NewMemoryService()),
		"ambiguous lost response":             sendemail.New(providersFor(&lostResponseClient{}), docref.NewMemoryService()),
		"provider error carrying ErrUpstream": sendemail.New(providersFor(upstreamWrapped), docref.NewMemoryService()),
	}
	for name, conn := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := conn.Execute(context.Background(), emailInput())
			require.Error(t, err)
			assert.False(t, errors.Is(err, shared.ErrUpstream), "a send-email failure must never carry the retryable ErrUpstream class")
			assert.False(t, shared.IsRetryable(err))
			assert.False(t, autoRetryAllowed(err))
			assert.True(t, errors.Is(err, shared.ErrNotDelivered) || errors.Is(err, shared.ErrDeliveryUnknown),
				"every failure reports a delivery outcome")
		})
	}
	assert.Equal(t, registry.RetryPolicyNotDelivered, registry.All()[registry.TypeSendEmail].Retry)
}

// ---- 2. Ambiguous outcomes are surfaced ------------------------------------

func TestSendEmail_LostResponse_IsSurfacedAsUnknown(t *testing.T) {
	t.Parallel()

	client := &lostResponseClient{}
	conn := sendemail.New(providersFor(client), docref.NewMemoryService())

	out, err := conn.Execute(context.Background(), emailInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrDeliveryUnknown)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the cause stays available")
	assert.Contains(t, err.Error(), "may duplicate", "the message warns operators")

	var sendErr *sendemail.SendError
	require.ErrorAs(t, err, &sendErr)
	assert.Equal(t, sendemail.OutcomeUnknown, sendErr.Outcome)
	assert.Equal(t, "sendgrid", sendErr.Provider)

	assert.Equal(t, false, out["sent"])
	assert.Equal(t, "unknown", out["deliveryOutcome"], "the output carries the outcome alongside the error")
	assert.Equal(t, 1, client.count(), "the provider did deliver it")
}

func TestSendEmail_CancelledBeforeSend_IsNotDelivered(t *testing.T) {
	t.Parallel()

	client := &lostResponseClient{}
	conn := sendemail.New(providersFor(client), docref.NewMemoryService())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := conn.Execute(ctx, emailInput())
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Zero(t, client.count(), "nothing reached the provider")
}

func TestSendEmail_Success_ReportsAccepted(t *testing.T) {
	t.Parallel()

	conn := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService())
	out, err := conn.Execute(context.Background(), emailInput())
	require.NoError(t, err)
	assert.Equal(t, true, out["sent"])
	assert.Equal(t, "accepted", out["deliveryOutcome"])
}

// ---- Classification rules ---------------------------------------------------

func TestClassifyStatus(t *testing.T) {
	t.Parallel()

	for _, code := range []int{400, 401, 403, 404, 413, 422, 429} {
		assert.ErrorIsf(t, sendemail.ClassifyStatus("p", code, errors.New("x")), shared.ErrNotDelivered, "status %d", code)
	}
	for _, code := range []int{500, 502, 503, 504, 302} {
		assert.ErrorIsf(t, sendemail.ClassifyStatus("p", code, errors.New("x")), shared.ErrDeliveryUnknown, "status %d", code)
	}
}

func TestClassifyTransport(t *testing.T) {
	t.Parallel()

	// Connection never established: provably not delivered.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	_, dialErr := net.Dial("tcp", addr)
	require.Error(t, dialErr)
	assert.ErrorIs(t, sendemail.ClassifyTransport("p", dialErr), shared.ErrNotDelivered)

	assert.ErrorIs(t, sendemail.ClassifyTransport("p", &net.DNSError{Err: "no such host", Name: "x.invalid"}), shared.ErrNotDelivered)

	// Anything after the connection: unknown.
	for _, err := range []error{context.DeadlineExceeded, errors.New("connection reset by peer"), errors.New("unexpected EOF")} {
		assert.ErrorIs(t, sendemail.ClassifyTransport("p", err), shared.ErrDeliveryUnknown, err.Error())
	}
}

func TestSendError_NeverExposesErrUpstream(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("%w: something", shared.ErrUpstream)
	for _, err := range []error{sendemail.NotDelivered("p", 0, cause), sendemail.Unknown("p", 0, cause)} {
		assert.False(t, errors.Is(err, shared.ErrUpstream))
		var se *sendemail.SendError
		require.ErrorAs(t, err, &se)
		assert.Same(t, cause, se.Err, "the cause is kept on the field")
	}
	plain := errors.New("plain")
	assert.ErrorIs(t, sendemail.Unknown("p", 0, plain), plain, "an ordinary cause is unwrapped for errors.As")
}

// ---- 3. Operator resend after an ambiguous outcome may duplicate ----------

func TestSendEmail_SendIntent_RejectsDuplicateRequest_ExplicitResendMayDuplicate(t *testing.T) {
	t.Parallel()

	client := &lostResponseClient{}
	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(client), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	ctx := shared.WithTenant(context.Background(), "tenant-1")

	// First request: delivered, but the response was lost.
	out, err := conn.Execute(ctx, emailInput("messageKey", "invoice-42"))
	require.ErrorIs(t, err, shared.ErrDeliveryUnknown)
	intentID := out["sendIntentId"]
	require.NotEmpty(t, intentID)

	// The same request arrives again (a retried step, a double submit):
	// rejected before anything is sent.
	_, err = conn.Execute(ctx, emailInput("messageKey", "invoice-42"))
	var dup *sendintent.DuplicateRequestError
	require.ErrorAs(t, err, &dup)
	assert.ErrorIs(t, err, shared.ErrValidation, "a duplicate request is never retried")
	assert.Equal(t, sendintent.StatusUnknown, dup.Status, "the earlier intent's uncertain outcome is visible")
	assert.Equal(t, 1, client.count(), "no second send")

	// An operator explicitly resends. The provider had in fact accepted the
	// first one, so the recipient now gets the email twice — the documented
	// limitation, not a bug: exactly-once delivery is not possible.
	_, err = conn.Execute(ctx, emailInput("messageKey", "invoice-42", "resend", true, "resendAttempt", dup.Attempts))
	require.ErrorIs(t, err, shared.ErrDeliveryUnknown)
	assert.Equal(t, 2, client.count(), "an explicit resend after an ambiguous outcome can duplicate")

	// The resend request is redelivered: it names attempt 1, which is no
	// longer current, so it is a duplicate and sends nothing.
	_, err = conn.Execute(ctx, emailInput("messageKey", "invoice-42", "resend", true, "resendAttempt", dup.Attempts))
	require.ErrorIs(t, err, sendintent.ErrDuplicateRequest)
	assert.Equal(t, 2, client.count(), "a redelivered resend never sends a third time")

	got, ok, _ := intents.Get(context.Background(), "tenant-1", "invoice-42")
	require.True(t, ok)
	assert.Equal(t, 2, got.Attempts, "the resend is recorded")
	assert.Equal(t, sendintent.StatusUnknown, got.Status)
	assert.Contains(t, got.Detail, "may duplicate")
}

func TestSendEmail_SendIntent_RequiresStoreAndTenant(t *testing.T) {
	t.Parallel()

	noStore := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService())
	_, err := noStore.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "k"))
	assert.ErrorIs(t, err, shared.ErrValidation, "a messageKey without a store must not be silently ignored")

	withStore := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService(),
		sendemail.WithSendIntents(sendintent.NewMemoryStore()))
	_, err = withStore.Execute(context.Background(), emailInput("messageKey", "k"))
	assert.ErrorIs(t, err, shared.ErrMissingTenant)
}

func TestSendEmail_SendIntent_RecordsAccepted(t *testing.T) {
	t.Parallel()

	intents := sendintent.NewMemoryStore()
	conn := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService(), sendemail.WithSendIntents(intents))
	out, err := conn.Execute(shared.WithTenant(context.Background(), "t"), emailInput("messageKey", "welcome-7"))
	require.NoError(t, err)

	got, ok, _ := intents.Get(context.Background(), "t", "welcome-7")
	require.True(t, ok)
	assert.Equal(t, sendintent.StatusAccepted, got.Status)
	assert.Equal(t, out["messageId"], got.ProviderMessageID)
}

// ---- 4. Documentation and API behaviour stay aligned ---------------------

func TestSendEmail_RegistryDocumentsDeliverySemantics(t *testing.T) {
	t.Parallel()

	def := registry.All()[registry.TypeSendEmail]
	assert.Equal(t, registry.RetryPolicyNotDelivered, def.Retry)
	for _, phrase := range []string{"Not idempotent", "at-most-once when not retried", "at-least-once when retried after an uncertain outcome", "Exactly-once delivery is not provided"} {
		assert.Contains(t, def.Description, phrase)
	}

	var outcome registry.Field
	for _, f := range def.Outputs {
		if f.Name == "deliveryOutcome" {
			outcome = f
		}
	}
	assert.Equal(t, []string{string(sendemail.OutcomeAccepted), string(sendemail.OutcomeNotDelivered), string(sendemail.OutcomeUnknown)},
		outcome.EnumValues, "the registry enum matches the outcomes the code reports")
	assert.False(t, strings.Contains(strings.ToLower(def.Description), "exactly-once delivery is provided"))
}
