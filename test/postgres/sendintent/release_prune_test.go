//go:build integration

package sqlstore_test

import (
	"context"
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

// Release marks the intent not_delivered only while it is pending under the
// caller's token: never another caller's reservation, never a recorded
// outcome. Nothing to release is not an error.
func TestPostgres_Release_OnlyOwnPendingReservation(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	store, ctx, tenant := sqlstore.New(pool), context.Background(), "tenant-"+uuid.NewString()

	_, err := store.Release(ctx, tenant, "k", "")
	assert.ErrorIs(t, err, sendintent.ErrNoToken)
	release := func(key, token string) bool {
		t.Helper()
		released, err := store.Release(ctx, tenant, key, token)
		require.NoError(t, err)
		return released
	}
	assert.False(t, release("missing", "tok-1"), "no intent: nothing to release")

	in, ok, err := store.Reserve(ctx, tenant, "k", 0, "tok-1")
	require.NoError(t, err)
	require.True(t, ok)

	assert.False(t, release("k", "tok-other"))
	got, _, err := store.Get(ctx, tenant, "k")
	require.NoError(t, err)
	assert.Equal(t, sendintent.StatusPending, got.Status, "another caller's reservation is never released")

	assert.True(t, release("k", "tok-1"), "this caller's pending reservation is released")
	got, _, err = store.Get(ctx, tenant, "k")
	require.NoError(t, err)
	assert.Equal(t, sendintent.StatusNotDelivered, got.Status)
	assert.Equal(t, sendintent.ReleasedDetail, got.Detail)
	assert.Equal(t, in.Attempts, got.Attempts)

	again, ok, err := store.Reserve(ctx, tenant, "k", 0, "tok-2")
	require.NoError(t, err)
	require.True(t, ok, "a released intent is re-reserved by the retry")
	require.NoError(t, store.Record(ctx, again.ID, again.Attempts, sendintent.StatusAccepted, "m", ""))
	assert.False(t, release("k", "tok-2"))
	got, _, err = store.Get(ctx, tenant, "k")
	require.NoError(t, err)
	assert.Equal(t, sendintent.StatusAccepted, got.Status, "a recorded outcome is never overwritten")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	released, err := store.Release(cancelled, tenant, "k", "tok-2")
	assert.False(t, released)
	assert.ErrorContains(t, err, "sendintent: release")
}

// PruneIntents deletes finished intents older than the cutoff, at most batch
// per call, never a pending one.
func TestPostgres_PruneIntents(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	ctx := context.Background()

	_, err := store.PruneIntents(ctx, 0, 10)
	assert.ErrorIs(t, err, sendintent.ErrPruneArgs)
	_, err = store.PruneIntents(ctx, time.Hour, 0)
	assert.ErrorIs(t, err, sendintent.ErrPruneArgs)

	for _, key := range []string{"old-1", "old-2", "old-3", "old-pending", "recent"} {
		in, _, err := store.Reserve(ctx, "t", key, 0, "tok")
		require.NoError(t, err)
		if key != "old-pending" {
			require.NoError(t, store.Record(ctx, in.ID, in.Attempts, sendintent.StatusAccepted, "m", ""))
		}
	}
	require.NoError(t, execSQL(pool, `UPDATE connector_send_intents SET updated_at = now() - interval '48 hours' WHERE message_key <> 'recent'`))

	deleted, err := store.PruneIntents(ctx, 24*time.Hour, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "at most batch per call")
	deleted, err = store.PruneIntents(ctx, 24*time.Hour, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	for key, kept := range map[string]bool{"old-1": false, "old-2": false, "old-3": false, "old-pending": true, "recent": true} {
		_, found, err := store.Get(ctx, "t", key)
		require.NoError(t, err)
		assert.Equal(t, kept, found, key)
	}
	again, ok, err := store.Reserve(ctx, "t", "old-1", 0, "tok")
	require.NoError(t, err)
	assert.True(t, ok, "pruning reopens the duplicate window")
	assert.Equal(t, 1, again.Attempts)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.PruneIntents(cancelled, time.Hour, 10)
	assert.ErrorContains(t, err, "sendintent: prune intents")
}

// Migration 000003: attempts is never below 1, and applying the schema again
// is a no-op.
func TestPostgres_AttemptsCheck(t *testing.T) {
	t.Parallel()
	_, pool, dsn := isolatedSchema(t)

	err := execSQL(pool, `INSERT INTO connector_send_intents (tenant_id, message_key, status, attempts) VALUES ('t', 'k', 'pending', 0)`)
	assert.True(t, pgcommon.IsCheckViolation(err), "attempts >= 1 is enforced: %v", err)
	assert.Equal(t, "chk_connector_send_intents_attempts", pgcommon.ConstraintName(err))

	require.NoError(t, sqlstore.ApplySchema(context.Background(), &migrate.Runner{DSN: dsn}), "re-applying is a no-op")
	var n int
	require.NoError(t, pool.WithConn(context.Background(), func(ctx context.Context, conn *pgcommon.Conn) error {
		return conn.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
			WHERE conname = 'chk_connector_send_intents_attempts' AND conrelid = 'connector_send_intents'::regclass AND convalidated`).Scan(&n)
	}))
	assert.Equal(t, 1, n, "one validated constraint")
}
