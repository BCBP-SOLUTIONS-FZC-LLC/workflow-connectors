//go:build integration

package sqlstore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// An old finished intent that a concurrent request re-reserves while the
// prune runs is not deleted: deleting it would let the redelivered job
// re-insert the key and send the email again. Transaction A re-reserves the
// row (pending, updated_at = now()) and holds its lock while the prune runs;
// A then commits. A prune whose DELETE re-checked only `id IN (subquery)`
// would wait for A and then delete the now-pending row (READ COMMITTED
// re-evaluates only the DELETE's own conditions on the updated row).
func TestPostgres_PruneIntents_SparesAConcurrentReReservation(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	ctx := context.Background()

	in, _, err := store.Reserve(ctx, "t", "k", 0, "tok-1")
	require.NoError(t, err)
	require.NoError(t, store.Record(ctx, in.ID, in.Attempts, sendintent.StatusNotDelivered, "", "throttled"))
	require.NoError(t, execSQL(pool, `UPDATE connector_send_intents SET updated_at = now() - interval '48 hours' WHERE id = $1::uuid`, in.ID))

	locked, commit, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- pgcommon.RunInTx(ctx, pool, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
			// What Reserve's upsert does to a not_delivered intent.
			_, err := tx.Exec(ctx, `UPDATE connector_send_intents
				SET status = 'pending', attempts = attempts + 1, reservation_token = 'tok-2', updated_at = now()
				WHERE id = $1::uuid`, in.ID)
			close(locked)
			if err != nil {
				return err
			}
			<-commit
			return nil
		})
	}()
	<-locked

	type result struct {
		deleted int64
		err     error
	}
	pruned := make(chan result, 1)
	go func() {
		n, err := store.PruneIntents(ctx, 24*time.Hour, 10)
		pruned <- result{n, err}
	}()

	// Let the prune either finish (it skips the locked row) or block on the
	// lock (as a DELETE without the re-check does), then commit A.
	var got *result
	for deadline := time.Now().Add(10 * time.Second); got == nil && time.Now().Before(deadline); {
		select {
		case r := <-pruned:
			got = &r
		case <-time.After(20 * time.Millisecond):
			if blockedPrunes(t, pool) > 0 {
				deadline = time.Now() // blocked: commit A now
			}
		}
	}
	close(commit)
	require.NoError(t, <-done)
	if got == nil {
		r := <-pruned
		got = &r
	}

	require.NoError(t, got.err)
	assert.Zero(t, got.deleted, "the re-reserved intent is not pruned")
	after, found, err := store.Get(ctx, "t", "k")
	require.NoError(t, err)
	require.True(t, found, "the re-reserved intent survives the prune")
	assert.Equal(t, sendintent.StatusPending, after.Status)
	assert.Equal(t, 2, after.Attempts)
	assert.Equal(t, "tok-2", after.ReservationToken)

	// The redelivered job meets the pending intent: a duplicate, not a send.
	_, reserved, err := store.Reserve(ctx, "t", "k", 0, "tok-3")
	require.NoError(t, err)
	assert.False(t, reserved)
}

// blockedPrunes counts prune DELETEs of this database waiting on a lock.
func blockedPrunes(t *testing.T, pool *pgcommon.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.WithConn(context.Background(), func(ctx context.Context, conn *pgcommon.Conn) error {
		return conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%DELETE FROM connector_send_intents%'`).Scan(&n)
	}))
	return n
}

// A Reserve that failed before COMMIT was sent reserved nothing
// (sendintent.ErrNotReserved): its statement failed, or no connection could
// be acquired (pool closed, pool exhausted until the context expired). One
// that failed at COMMIT may have committed, and is not marked.
func TestPostgres_Reserve_MarksOnlyFailuresThatReservedNothing(t *testing.T) {
	t.Parallel()

	t.Run("statement failed", func(t *testing.T) {
		t.Parallel()
		store, pool := isolatedStore(t)
		require.NoError(t, execSQL(pool, `
			CREATE FUNCTION raise_code() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected' USING ERRCODE = '55P03'; END $$;
			CREATE TRIGGER raise_code BEFORE INSERT ON connector_send_intents FOR EACH ROW EXECUTE FUNCTION raise_code();`))
		_, _, err := store.Reserve(context.Background(), "t", "k", 0, "tok")
		require.Error(t, err)
		assert.ErrorIs(t, err, sendintent.ErrNotReserved)
		assert.True(t, shared.IsTransient(err), "the class is kept")
	})

	t.Run("pool closed", func(t *testing.T) {
		t.Parallel()
		store, pool := isolatedStore(t)
		pool.Close()
		_, _, err := store.Reserve(context.Background(), "t", "k", 0, "tok")
		require.Error(t, err)
		assert.ErrorIs(t, err, sendintent.ErrNotReserved, "no connection: nothing was sent to PostgreSQL")
		class, reason := shared.ClassOf(err)
		assert.Equal(t, shared.ClassUnknown, class, "a closed pool keeps its class")
		assert.Equal(t, "postgres pool closed", reason)
	})

	t.Run("pool exhausted", func(t *testing.T) {
		t.Parallel()
		store, pool, release := exhaustedPool(t)
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, _, err := store.Reserve(ctx, "t", "k", 0, "tok")
		require.Error(t, err)
		assert.ErrorIs(t, err, sendintent.ErrNotReserved, "no connection was acquired: nothing was reserved")
		assert.True(t, shared.IsTransient(err), "an acquire timeout is transient: %v", err)
		release()
		_, found, err := sqlstore.New(pool).Get(context.Background(), "t", "k")
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("commit failed", func(t *testing.T) {
		t.Parallel()
		store, pool := isolatedStore(t)
		// A deferred constraint trigger runs at COMMIT: the statement
		// succeeded, so the store cannot tell the commit's outcome apart.
		require.NoError(t, execSQL(pool, `
			CREATE FUNCTION raise_at_commit() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected at commit' USING ERRCODE = '08006'; END $$;
			CREATE CONSTRAINT TRIGGER raise_at_commit AFTER INSERT ON connector_send_intents
				DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION raise_at_commit();`))
		_, _, err := store.Reserve(context.Background(), "t", "k", 0, "tok")
		require.Error(t, err)
		assert.NotErrorIs(t, err, sendintent.ErrNotReserved, "a failed COMMIT is ambiguous")
		assert.ErrorContains(t, err, "sendintent: reserve")
	})
}

// Migrations 000004 (validate the attempts check) and 000005 (the partial
// prune index) apply after 000003, and PruneIntents uses the index.
func TestPostgres_Migrations_ValidateAndPruneIndex(t *testing.T) {
	t.Parallel()
	_, pool, _ := isolatedSchema(t)
	ctx := context.Background()

	require.NoError(t, pool.WithConn(ctx, func(ctx context.Context, conn *pgcommon.Conn) error {
		var version int64
		var dirty bool
		require.NoError(t, conn.QueryRow(ctx, "SELECT version, dirty FROM "+sqlstore.MigrationsTable).Scan(&version, &dirty))
		assert.Equal(t, int64(5), version)
		assert.False(t, dirty)

		var validated bool
		require.NoError(t, conn.QueryRow(ctx, `SELECT convalidated FROM pg_constraint
			WHERE conname = 'chk_connector_send_intents_attempts' AND conrelid = 'connector_send_intents'::regclass`).Scan(&validated))
		assert.True(t, validated)

		var def string
		require.NoError(t, conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = 'ix_connector_send_intents_prune'`).Scan(&def))
		assert.Contains(t, def, "(updated_at)")
		assert.Contains(t, def, "WHERE (status <> 'pending'::text)")

		// The planner can serve the prune's scan from the partial index.
		_, err := conn.Exec(ctx, `SET enable_seqscan = off`)
		require.NoError(t, err)
		rows, err := conn.Query(ctx, fmt.Sprintf(`EXPLAIN SELECT id FROM connector_send_intents
			WHERE status <> 'pending' AND updated_at < now() - make_interval(secs => %d) LIMIT 10`, 3600))
		require.NoError(t, err)
		var plan string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			plan += line + "\n"
		}
		require.NoError(t, rows.Err())
		assert.Contains(t, plan, "ix_connector_send_intents_prune")
		_, err = conn.Exec(ctx, `RESET enable_seqscan`)
		return err
	}))
}

// exhaustedPool is an isolated store on a pool of one connection, held by
// another caller until release is called (idempotent).
func exhaustedPool(t *testing.T) (*sqlstore.Store, *pgcommon.Pool, func()) {
	t.Helper()
	_, _, dsn := isolatedSchema(t)
	pool, err := pgcommon.NewPool(context.Background(), pgcommon.Config{DSN: dsn, MaxConns: 1, PoolName: "sendintent_exhausted_test"})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	held, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- pool.WithConn(context.Background(), func(context.Context, *pgcommon.Conn) error {
			close(held)
			<-stop
			return nil
		})
	}()
	<-held
	var once sync.Once
	release := func() { once.Do(func() { close(stop); require.NoError(t, <-done) }) }
	t.Cleanup(release)
	return sqlstore.New(pool), pool, release
}

// Migration 000005 repairs what an operator's CREATE INDEX CONCURRENTLY can
// leave behind: an INVALID index (the build failed or was cancelled) is
// dropped and rebuilt, a valid one is kept, a missing one is created.
func TestPostgres_Migration000005_RepairsAnInvalidPruneIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	indexState := func(t *testing.T, pool *pgcommon.Pool) (oid uint32, valid, found bool) {
		t.Helper()
		require.NoError(t, pool.WithConn(ctx, func(ctx context.Context, conn *pgcommon.Conn) error {
			err := conn.QueryRow(ctx, `SELECT i.indexrelid::oid, i.indisvalid FROM pg_index i
				JOIN pg_class c ON c.oid = i.indexrelid
				WHERE i.indrelid = 'connector_send_intents'::regclass AND c.relname = 'ix_connector_send_intents_prune'`).Scan(&oid, &valid)
			if errors.Is(err, pgcommon.ErrNoRows) {
				return nil
			}
			found = err == nil
			return err
		}))
		return oid, valid, found
	}
	reapply := func(t *testing.T, pool *pgcommon.Pool, dsn string) {
		t.Helper()
		require.NoError(t, execSQL(pool, `UPDATE `+sqlstore.MigrationsTable+` SET version = 4, dirty = false`))
		require.NoError(t, sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: dsn}))
	}

	t.Run("invalid index is rebuilt", func(t *testing.T) {
		t.Parallel()
		_, pool, dsn := isolatedSchema(t)
		before, _, _ := indexState(t, pool)
		// What a failed CREATE INDEX CONCURRENTLY leaves: the index exists
		// but is not valid (the test database's role is a superuser).
		require.NoError(t, execSQL(pool, `UPDATE pg_index SET indisvalid = false
			WHERE indexrelid = 'ix_connector_send_intents_prune'::regclass`))
		_, valid, found := indexState(t, pool)
		require.True(t, found)
		require.False(t, valid)

		reapply(t, pool, dsn)
		after, valid, found := indexState(t, pool)
		require.True(t, found)
		assert.True(t, valid, "the invalid index was rebuilt")
		assert.NotEqual(t, before, after, "a new index, not the invalid one")
	})

	t.Run("valid index is kept", func(t *testing.T) {
		t.Parallel()
		_, pool, dsn := isolatedSchema(t)
		before, _, _ := indexState(t, pool)
		reapply(t, pool, dsn)
		after, valid, found := indexState(t, pool)
		require.True(t, found)
		assert.True(t, valid)
		assert.Equal(t, before, after, "a valid index (built CONCURRENTLY beforehand) is not rebuilt")
	})

	t.Run("missing index is created", func(t *testing.T) {
		t.Parallel()
		_, pool, dsn := isolatedSchema(t)
		require.NoError(t, execSQL(pool, `DROP INDEX ix_connector_send_intents_prune`))
		reapply(t, pool, dsn)
		_, valid, found := indexState(t, pool)
		require.True(t, found)
		assert.True(t, valid)
	})
}
