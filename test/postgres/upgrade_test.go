//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	docsql "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	intentsql "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
)

// The migration files as the library embeds them, read from the source tree
// so a test can stop at an older version (ApplySchema always goes to the
// latest).
const (
	intentMigrations   = "../../pkg/connectors/sendintent/sqlstore/migrations"
	documentMigrations = "../../pkg/connectors/documents/sqlstore/migrations"
)

// v200IntentVersion is the send-intent schema version v2.0.0 shipped.
const v200IntentVersion = 2

// latestVersion is the highest NNNNNN_*.up.sql version in dir.
func latestVersion(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var versions []int64
	for _, e := range entries {
		if num, _, ok := strings.Cut(e.Name(), "_"); ok && strings.HasSuffix(e.Name(), ".up.sql") {
			v, err := strconv.ParseInt(num, 10, 64)
			require.NoError(t, err, e.Name())
			versions = append(versions, v)
		}
	}
	require.NotEmpty(t, versions, dir)
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions[len(versions)-1]
}

// intentRunner runs the send-intent migrations from the source tree against
// dsn, tracked in the same table ApplySchema uses.
func intentRunner(dsn string) *migrate.Runner {
	abs, _ := filepath.Abs(intentMigrations)
	return &migrate.Runner{FS: os.DirFS(abs), DSN: dsn, MigrationsTable: intentsql.MigrationsTable}
}

// intentSchema reports the send-intent schema's tracked version and the
// objects migrations 000003-000005 create.
type intentSchema struct {
	version   int64
	dirty     bool
	check     bool // chk_connector_send_intents_attempts exists
	validated bool // ... and is validated
	index     bool // ix_connector_send_intents_prune exists
}

func readIntentSchema(t *testing.T, ctx context.Context, dsn string) intentSchema {
	t.Helper()
	var s intentSchema
	require.NoError(t, withConn(ctx, dsn, func(ctx context.Context, conn *pgcommon.Conn) error {
		if err := conn.QueryRow(ctx, "SELECT version, dirty FROM "+intentsql.MigrationsTable).Scan(&s.version, &s.dirty); err != nil {
			return err
		}
		if err := conn.QueryRow(ctx, `SELECT count(*) > 0, coalesce(bool_and(convalidated), false) FROM pg_constraint
			WHERE conname = 'chk_connector_send_intents_attempts' AND conrelid = 'connector_send_intents'::regclass`).Scan(&s.check, &s.validated); err != nil {
			return err
		}
		return conn.QueryRow(ctx, `SELECT count(*) > 0 FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = 'ix_connector_send_intents_prune'`).Scan(&s.index)
	}))
	return s
}

func directPool(t *testing.T, dsn string) *pgcommon.Pool {
	t.Helper()
	pool, err := pgcommon.NewPool(context.Background(), pgcommon.Config{DSN: dsn, MaxConns: 8, PoolName: "pg_upgrade"})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// PG-01's latest versions, pinned exactly: every embedded migration of both
// schemas is applied (not merely "some version"), including the send-intent
// migrations 000003-000005 added after v2.0.0.
func TestPG07_SchemasReachTheirLatestVersion(t *testing.T) {
	t.Parallel()
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = applyAll(ctx, dsn) })
	}
	wg.Wait()
	for i, err := range errs {
		require.NoErrorf(t, err, "replica %d", i)
	}

	require.NoError(t, withConn(ctx, dsn, func(ctx context.Context, conn *pgcommon.Conn) error {
		for table, dir := range map[string]string{docsql.MigrationsTable: documentMigrations, intentsql.MigrationsTable: intentMigrations} {
			var version int64
			require.NoError(t, conn.QueryRow(ctx, "SELECT version FROM "+table).Scan(&version), table)
			assert.Equal(t, latestVersion(t, dir), version, table)
		}
		return nil
	}))
	s := readIntentSchema(t, ctx, dsn)
	assert.True(t, s.validated, "000004 validated the attempts check")
	assert.True(t, s.index, "000005 created the prune index")
}

// PG-04: a v2.0.0 deployment (send-intent schema version 2, with live rows)
// upgrades: four replicas apply 000003-000005 at once while the old
// replicas keep reserving and recording intents. Every migration applies
// exactly once, no request fails on the migrations' locks, the existing rows
// are untouched and satisfy the new check, and PruneIntents runs on the new
// index.
func TestPG04_SendIntentsUpgradeFromV200_UnderTraffic(t *testing.T) {
	t.Parallel()
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	require.NoError(t, intentRunner(dsn).Steps(ctx, v200IntentVersion), "the v2.0.0 schema")
	s := readIntentSchema(t, ctx, dsn)
	require.Equal(t, int64(v200IntentVersion), s.version)
	require.False(t, s.check)
	require.False(t, s.index)

	pool := directPool(t, dsn)
	store := intentsql.New(pool)
	// Rows a v2.0.0 worker wrote: accepted, re-reserved after not_delivered,
	// and one still pending.
	for i := range 20 {
		key := fmt.Sprintf("old-%d", i)
		in, ok, err := store.Reserve(ctx, "t", key, 0, "tok")
		require.NoError(t, err)
		require.True(t, ok)
		status := sendintent.StatusAccepted
		if i%4 == 1 {
			status = sendintent.StatusNotDelivered
		}
		if i%4 != 3 {
			require.NoError(t, store.Record(ctx, in.ID, in.Attempts, status, "m", ""))
		}
		if status == sendintent.StatusNotDelivered {
			again, ok, err := store.Reserve(ctx, "t", key, 0, "tok-2")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, 2, again.Attempts)
			require.NoError(t, store.Record(ctx, again.ID, again.Attempts, sendintent.StatusAccepted, "m2", ""))
		}
	}
	require.NoError(t, withConn(ctx, dsn, func(ctx context.Context, conn *pgcommon.Conn) error {
		_, err := conn.Exec(ctx, `UPDATE connector_send_intents SET updated_at = now() - interval '48 hours' WHERE status <> 'pending'`)
		return err
	}))

	// Traffic during the upgrade: a worker on the old version keeps working.
	stop := make(chan struct{})
	var served atomic.Int64
	trafficErr := make(chan error, 1)
	go func() {
		defer close(trafficErr)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			in, ok, err := store.Reserve(ctx, "t", fmt.Sprintf("live-%d", i), 0, "tok")
			if err == nil && !ok {
				err = fmt.Errorf("live-%d not reserved", i)
			}
			if err == nil {
				err = store.Record(ctx, in.ID, in.Attempts, sendintent.StatusAccepted, "m", "")
			}
			if err != nil {
				trafficErr <- err
				return
			}
			served.Add(1)
		}
	}()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = intentsql.ApplySchema(ctx, &migrate.Runner{DSN: dsn}) })
	}
	wg.Wait()
	close(stop)
	require.NoError(t, <-trafficErr, "reservations and records keep working during the upgrade")
	for i, err := range errs {
		require.NoErrorf(t, err, "replica %d", i)
	}
	assert.Positive(t, served.Load())

	s = readIntentSchema(t, ctx, dsn)
	assert.Equal(t, latestVersion(t, intentMigrations), s.version)
	assert.False(t, s.dirty)
	assert.True(t, s.validated, "the existing rows satisfy attempts >= 1")
	assert.True(t, s.index)

	for i := range 20 {
		in, found, err := store.Get(ctx, "t", fmt.Sprintf("old-%d", i))
		require.NoError(t, err)
		require.True(t, found)
		if i%4 == 3 {
			assert.Equal(t, sendintent.StatusPending, in.Status)
		} else {
			assert.Equal(t, sendintent.StatusAccepted, in.Status)
		}
	}
	deleted, err := store.PruneIntents(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(15), deleted, "the old finished rows; never the pending ones or the live traffic")
}

// PG-05: the 000003-000005 down migrations take the schema back to v2.0.0's
// (check and index gone, rows kept), and the up migrations apply again.
func TestPG05_SendIntentsDownToV200AndUpAgain(t *testing.T) {
	t.Parallel()
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	require.NoError(t, intentsql.ApplySchema(ctx, &migrate.Runner{DSN: dsn}))
	latest := latestVersion(t, intentMigrations)
	pool := directPool(t, dsn)
	store := intentsql.New(pool)
	_, ok, err := store.Reserve(ctx, "t", "k", 0, "tok")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, intentRunner(dsn).Down(ctx, int(latest)-v200IntentVersion))
	s := readIntentSchema(t, ctx, dsn)
	assert.Equal(t, int64(v200IntentVersion), s.version)
	assert.False(t, s.dirty)
	assert.False(t, s.check, "000003 down drops the check")
	assert.False(t, s.index, "000005 down drops the index")
	_, found, err := store.Get(ctx, "t", "k")
	require.NoError(t, err)
	assert.True(t, found, "rows are kept")

	require.NoError(t, intentsql.ApplySchema(ctx, &migrate.Runner{DSN: dsn}), "up again")
	s = readIntentSchema(t, ctx, dsn)
	assert.Equal(t, latest, s.version)
	assert.True(t, s.validated)
	assert.True(t, s.index)
}

// PG-06: a v2.0.0 row edited by hand to attempts < 1 stops the upgrade at
// 000004 (VALIDATE), as the migration's comment says; 000003 has already
// added the check NOT VALID, so new and updated rows are checked from then
// on. Nothing is silently skipped.
func TestPG06_SendIntentsUpgrade_InvalidRowStopsAtValidate(t *testing.T) {
	t.Parallel()
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	require.NoError(t, intentRunner(dsn).Steps(ctx, v200IntentVersion))
	require.NoError(t, withConn(ctx, dsn, func(ctx context.Context, conn *pgcommon.Conn) error {
		_, err := conn.Exec(ctx, `INSERT INTO connector_send_intents (tenant_id, message_key, status, attempts) VALUES ('t', 'hand-edited', 'accepted', 0)`)
		return err
	}))

	err := intentsql.ApplySchema(ctx, &migrate.Runner{DSN: dsn})
	require.Error(t, err, "VALIDATE refuses the invalid row")
	assert.Contains(t, err.Error(), "chk_connector_send_intents_attempts")

	s := readIntentSchema(t, ctx, dsn)
	assert.True(t, s.check, "000003 applied: the check exists")
	assert.False(t, s.validated, "but is not validated")
	assert.False(t, s.index, "000005 did not run")
	require.NoError(t, withConn(ctx, dsn, func(ctx context.Context, conn *pgcommon.Conn) error {
		_, err := conn.Exec(ctx, `INSERT INTO connector_send_intents (tenant_id, message_key, status, attempts) VALUES ('t', 'new', 'accepted', 0)`)
		assert.True(t, pgcommon.IsCheckViolation(err), "new rows are checked: %v", err)
		return nil
	}))
}
