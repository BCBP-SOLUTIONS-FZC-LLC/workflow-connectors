//go:build integration

package sendemail_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

var (
	poolOnce sync.Once
	testPool *pgcommon.Pool
	poolErr  error
)

func postgresStore(t *testing.T) *sqlstore.Store {
	if os.Getenv("CI") != "" && os.Getenv("TEST_POSTGRES_DSN") == "" {
		t.Fatal("CI is set but TEST_POSTGRES_DSN is not: the PostgreSQL tests cannot be skipped in CI")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	poolOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if poolErr = sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: dsn}); poolErr != nil {
			return
		}
		testPool, poolErr = pgcommon.NewPool(ctx, pgcommon.Config{DSN: dsn, MaxConns: 10, PoolName: "sendemail_test"})
	})
	require.NoError(t, poolErr)
	return sqlstore.New(testPool)
}

// lostReplyStore lets the reservation commit in PostgreSQL and then loses
// its reply, as a connection dropped after COMMIT would.
type lostReplyStore struct {
	*sqlstore.Store
	steal bool // another caller's token is stamped instead of ours
}

func (s lostReplyStore) Reserve(ctx context.Context, tenantID, messageKey string, resendFrom int, token string) (sendintent.Intent, bool, error) {
	if s.steal {
		token = "someone-else"
	}
	if _, _, err := s.Store.Reserve(ctx, tenantID, messageKey, resendFrom, token); err != nil {
		return sendintent.Intent{}, false, err
	}
	return sendintent.Intent{}, false, errors.New("connection reset after commit")
}

func emailInput(key string, extra ...any) map[string]any {
	in := map[string]any{"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com", "body": "hi", "messageKey": key}
	for i := 0; i < len(extra); i += 2 {
		in[extra[i].(string)] = extra[i+1]
	}
	return in
}

func connector(store sendintent.Store, client sendemail.ProviderClient) *sendemail.Connector {
	return sendemail.New(map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return client, nil },
	}, docref.NewMemoryService(), sendemail.WithSendIntents(store))
}

// A committed reservation whose reply was lost is recognised by its token:
// the call sends once and records accepted on the row.
func TestLostReservationReply_OwnTokenSendsOnce(t *testing.T) {
	t.Parallel()
	store := postgresStore(t)
	tenant := "tenant-" + uuid.NewString()
	ctx := shared.WithTenant(context.Background(), tenant)
	client := sendemail.NewMockSendEmailClient()

	out, err := connector(lostReplyStore{Store: store}, client).Execute(ctx, emailInput("invoice-1"))
	require.NoError(t, err)
	assert.Equal(t, "accepted", out["deliveryOutcome"])
	assert.Len(t, client.Sent(), 1)

	got, found, err := store.Get(context.Background(), tenant, "invoice-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sendintent.StatusAccepted, got.Status)
	assert.Equal(t, got.ID, out["sendIntentId"])
	assert.NotEmpty(t, got.ReservationToken)

	// The job redelivered after the success: a duplicate of an accepted
	// intent succeeds without sending again.
	out, err = connector(store, client).Execute(ctx, emailInput("invoice-1"))
	require.NoError(t, err)
	assert.Equal(t, true, out["duplicate"])
	assert.Len(t, client.Sent(), 1)
}

// A reservation stamped with another caller's token is not ours: nothing is
// sent, and that caller's pending intent is left alone.
func TestLostReservationReply_OtherTokenSendsNothing(t *testing.T) {
	t.Parallel()
	store := postgresStore(t)
	tenant := "tenant-" + uuid.NewString()
	client := sendemail.NewMockSendEmailClient()

	out, err := connector(lostReplyStore{Store: store, steal: true}, client).Execute(shared.WithTenant(context.Background(), tenant), emailInput("invoice-1"))
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
	assert.Equal(t, "not_delivered", out["deliveryOutcome"])
	assert.Empty(t, client.Sent())

	got, _, err := store.Get(context.Background(), tenant, "invoice-1")
	require.NoError(t, err)
	assert.Equal(t, sendintent.StatusPending, got.Status)
}

// Explicit resend through PostgreSQL is a compare-and-swap on the attempt:
// the resend job redelivered is a duplicate and sends nothing.
func TestResend_RedeliveredResendJob_SendsOnce(t *testing.T) {
	t.Parallel()
	store := postgresStore(t)
	tenant := "tenant-" + uuid.NewString()
	ctx := shared.WithTenant(context.Background(), tenant)
	client := sendemail.NewMockSendEmailClient()
	conn := connector(store, client)

	first, _, err := store.Reserve(context.Background(), tenant, "invoice-1", 0, "")
	require.NoError(t, err)
	require.NoError(t, store.Record(context.Background(), first.ID, first.Attempts, sendintent.StatusUnknown, "", "timed out"))

	_, err = conn.Execute(ctx, emailInput("invoice-1", "resend", true, "resendAttempt", 1))
	require.NoError(t, err)
	out, err := conn.Execute(ctx, emailInput("invoice-1", "resend", true, "resendAttempt", 1))
	require.NoError(t, err, "the resend was accepted, so its redelivery is an accepted duplicate")
	assert.Equal(t, true, out["duplicate"])
	assert.Equal(t, 2, out["attempts"])
	assert.Len(t, client.Sent(), 1)
}
