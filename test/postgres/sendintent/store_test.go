//go:build integration

package sqlstore_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
)

var (
	poolOnce sync.Once
	testPool *pgcommon.Pool
	poolErr  error
)

func postgres(t *testing.T) *pgcommon.Pool {
	// In CI the PostgreSQL legs are mandatory: a missing DSN must fail, not
	// silently skip the tests that prove the database enforces uniqueness.
	if os.Getenv("CI") != "" && os.Getenv("TEST_POSTGRES_DSN") == "" {
		t.Fatal("CI is set but TEST_POSTGRES_DSN is not: the PostgreSQL tests cannot be skipped in CI")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		return nil
	}
	poolOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if poolErr = sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: dsn}); poolErr != nil {
			return
		}
		testPool, poolErr = pgcommon.NewPool(ctx, pgcommon.Config{DSN: dsn, MaxConns: 50, PoolName: "sendintent_test"})
	})
	require.NoError(t, poolErr)
	return testPool
}

func stores(t *testing.T) map[string]sendintent.Store {
	out := map[string]sendintent.Store{"memory": sendintent.NewMemoryStore()}
	if pool := postgres(t); pool != nil {
		out["postgres"] = sqlstore.New(pool)
	}
	return out
}

func TestReserve_ConcurrentSameKey_ExactlyOneReserved(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tenant, key := "tenant-"+uuid.NewString(), "invoice-1"

			const callers = 40
			start := make(chan struct{})
			var reserved atomic.Int32
			ids := make([]string, callers)
			var wg sync.WaitGroup
			for i := range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					in, ok, err := store.Reserve(context.Background(), tenant, key, 0, "")
					if assert.NoError(t, err) {
						if ok {
							reserved.Add(1)
						}
						ids[i] = in.ID
					}
				}()
			}
			close(start)
			wg.Wait()

			assert.Equal(t, int32(1), reserved.Load(), "exactly one request may proceed to send")
			for _, id := range ids {
				assert.Equal(t, ids[0], id, "every duplicate sees the one intent")
			}
		})
	}
}

func TestReserve_ResendIsExplicitAndCounted(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, tenant := context.Background(), "tenant-"+uuid.NewString()

			first, ok, err := store.Reserve(ctx, tenant, "k", 0, "")
			require.NoError(t, err)
			require.True(t, ok)
			require.NoError(t, store.Record(ctx, first.ID, first.Attempts, sendintent.StatusUnknown, "", "timed out"))

			dup, ok, err := store.Reserve(ctx, tenant, "k", 0, "")
			require.NoError(t, err)
			assert.False(t, ok, "without resend a duplicate is refused")
			assert.Equal(t, sendintent.StatusUnknown, dup.Status)

			_, ok, err = store.Reserve(ctx, tenant, "k", 2, "")
			require.NoError(t, err)
			assert.False(t, ok, "a resend must name the current attempt")

			again, ok, err := store.Reserve(ctx, tenant, "k", first.Attempts, "tok-2")
			require.NoError(t, err)
			assert.True(t, ok, "resend is the explicit opt-in")
			assert.Equal(t, first.ID, again.ID)
			assert.Equal(t, 2, again.Attempts)
			assert.Equal(t, sendintent.StatusPending, again.Status)
			assert.Equal(t, "tok-2", again.ReservationToken)

			// The resend request redelivered: attempt 1 is no longer current.
			redelivered, ok, err := store.Reserve(ctx, tenant, "k", first.Attempts, "tok-3")
			require.NoError(t, err)
			assert.False(t, ok, "a redelivered resend is a duplicate, never a second resend")
			assert.Equal(t, 2, redelivered.Attempts)
			assert.Equal(t, "tok-2", redelivered.ReservationToken)

			_, ok, err = store.Reserve(ctx, "other-"+tenant, "k", 0, "")
			require.NoError(t, err)
			assert.True(t, ok, "keys are per tenant")
		})
	}
}

// A not-delivered intent may be tried again without resend — nothing reached
// the provider — and concurrent retries still let exactly one through.
func TestReserve_NotDelivered_RetriesWithoutResend_OneWinner(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, tenant := context.Background(), "tenant-"+uuid.NewString()

			first, ok, err := store.Reserve(ctx, tenant, "k", 0, "")
			require.NoError(t, err)
			require.True(t, ok)
			require.NoError(t, store.Record(ctx, first.ID, first.Attempts, sendintent.StatusNotDelivered, "", "dns failure"))

			const retries = 20
			start := make(chan struct{})
			var reserved atomic.Int32
			var wg sync.WaitGroup
			for range retries {
				wg.Go(func() {
					<-start
					in, ok, err := store.Reserve(ctx, tenant, "k", 0, "")
					if assert.NoError(t, err) && ok {
						reserved.Add(1)
						assert.Equal(t, first.ID, in.ID)
						assert.Equal(t, 2, in.Attempts)
					}
				})
			}
			close(start)
			wg.Wait()
			assert.Equal(t, int32(1), reserved.Load(), "exactly one retry proceeds")

			for _, status := range []sendintent.Status{sendintent.StatusAccepted, sendintent.StatusUnknown, sendintent.StatusPending} {
				in, ok, err := store.Reserve(ctx, tenant, "k-"+string(status), 0, "")
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, store.Record(ctx, in.ID, in.Attempts, status, "", ""))
				_, ok, err = store.Reserve(ctx, tenant, "k-"+string(status), 0, "")
				require.NoError(t, err)
				assert.Falsef(t, ok, "status %s may have been delivered: only resend may send again", status)
			}
		})
	}
}

func TestRecord_UnknownIntent(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := store.Record(context.Background(), uuid.NewString(), 1, sendintent.StatusAccepted, "", "")
			assert.ErrorIs(t, err, sendintent.ErrIntentNotFound)
		})
	}
}

// A late outcome of a superseded attempt never overwrites the current one:
// attempt 1 ends unknown, an operator resends it (attempt 2, pending, in
// flight), then attempt 1's worker writes its outcome again — which, recorded
// as not_delivered, would let an automatic retry send a duplicate alongside
// attempt 2.
func TestRecord_StaleAttempt_NeverOverwritesNewer(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, tenant := context.Background(), "tenant-"+uuid.NewString()

			first, ok, err := store.Reserve(ctx, tenant, "k", 0, "")
			require.NoError(t, err)
			require.True(t, ok)
			require.NoError(t, store.Record(ctx, first.ID, first.Attempts, sendintent.StatusUnknown, "", "timed out"))
			second, ok, err := store.Reserve(ctx, tenant, "k", first.Attempts, "") // explicit resend of attempt 1
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, 2, second.Attempts)

			err = store.Record(ctx, first.ID, first.Attempts, sendintent.StatusNotDelivered, "", "late failure of attempt 1")
			assert.ErrorIs(t, err, sendintent.ErrStaleRecord)

			_, ok, err = store.Reserve(ctx, tenant, "k", 0, "")
			require.NoError(t, err)
			assert.False(t, ok, "attempt 2 is still pending: no automatic retry may start")
			_, ok, err = store.Reserve(ctx, tenant, "k", second.Attempts, "")
			require.NoError(t, err)
			assert.False(t, ok, "a pending attempt is never resent")

			require.NoError(t, store.Record(ctx, second.ID, second.Attempts, sendintent.StatusAccepted, "msg-2", ""))
			err = store.Record(ctx, second.ID, second.Attempts, sendintent.StatusNotDelivered, "", "duplicate write")
			assert.ErrorIs(t, err, sendintent.ErrStaleRecord, "an outcome is recorded once")
		})
	}
}

// Reserve stamps the caller's token, so a caller whose reply was lost can
// recognise its own reservation with Get.
func TestReserve_StampsReservationToken(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, tenant := context.Background(), "tenant-"+uuid.NewString()

			first, ok, err := store.Reserve(ctx, tenant, "k", 0, "tok-1")
			require.NoError(t, err)
			require.True(t, ok)
			got, found, err := store.Get(ctx, tenant, "k")
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, "tok-1", got.ReservationToken)

			dup, ok, err := store.Reserve(ctx, tenant, "k", 0, "tok-2")
			require.NoError(t, err)
			require.False(t, ok)
			assert.Equal(t, "tok-1", dup.ReservationToken, "a refused reservation leaves the token")

			require.NoError(t, store.Record(ctx, first.ID, first.Attempts, sendintent.StatusNotDelivered, "", ""))
			retry, ok, err := store.Reserve(ctx, tenant, "k", 0, "tok-3")
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, "tok-3", retry.ReservationToken)

			_, found, err = store.Get(ctx, tenant, "missing")
			require.NoError(t, err)
			assert.False(t, found)
		})
	}
}

func TestPostgres_SendIntentConstraints(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	tenant := "tenant-" + uuid.NewString()
	exec := func(q string, args ...any) error {
		return pool.WithConn(context.Background(), func(ctx context.Context, conn *pgcommon.Conn) error {
			_, err := conn.Exec(ctx, q, args...)
			return err
		})
	}
	ins := `INSERT INTO connector_send_intents (tenant_id, message_key, status) VALUES ($1, $2, $3)`
	require.NoError(t, exec(ins, tenant, "k", "pending"))

	err := exec(ins, tenant, "k", "pending")
	assert.True(t, pgcommon.IsUniqueViolation(err), "one intent per tenant + messageKey, enforced by the database: %v", err)
	assert.Equal(t, "uq_connector_send_intents_key", pgcommon.ConstraintName(err))

	err = exec(ins, tenant, "k2", "delivered-twice")
	assert.True(t, pgcommon.IsCheckViolation(err), "status is constrained: %v", err)
}
