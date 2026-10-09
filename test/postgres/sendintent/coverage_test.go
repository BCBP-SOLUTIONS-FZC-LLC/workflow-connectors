//go:build integration

package sqlstore_test

import (
	"context"
	"net/url"
	"os"
	"strings"
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

func execSQL(pool *pgcommon.Pool, query string, args ...any) error {
	return pool.WithConn(context.Background(), func(ctx context.Context, conn *pgcommon.Conn) error {
		_, err := conn.Exec(ctx, query, args...)
		return err
	})
}

// isolatedStore applies the schema in a fresh PostgreSQL schema of its own
// (search_path in the DSN), so a test can alter the table to provoke
// failures without touching the shared one.
func isolatedStore(t *testing.T) (*sqlstore.Store, *pgcommon.Pool) {
	t.Helper()
	shared := postgres(t)
	if shared == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	schema := "cov_sendintent_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, execSQL(shared, `CREATE SCHEMA `+schema))
	t.Cleanup(func() { _ = execSQL(shared, `DROP SCHEMA `+schema+` CASCADE`) })

	u, err := url.Parse(os.Getenv("TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn := u.String()

	require.NoError(t, sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: dsn}))
	pool, err := pgcommon.NewPool(ctx, pgcommon.Config{DSN: dsn, MaxConns: 4, PoolName: "sendintent_coverage_test"})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return sqlstore.New(pool), pool
}

func TestApplySchema_NeedsADSN(t *testing.T) {
	t.Parallel()
	assert.ErrorContains(t, sqlstore.ApplySchema(context.Background(), nil), "DSN")
	assert.ErrorContains(t, sqlstore.ApplySchema(context.Background(), &migrate.Runner{}), "DSN")
}

func TestPostgres_Get_MissingThenReserved(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	store, ctx, tenant := sqlstore.New(pool), context.Background(), "tenant-"+uuid.NewString()
	_, found, err := store.Get(ctx, tenant, "invoice-1")
	require.NoError(t, err)
	assert.False(t, found)

	want, _, err := store.Reserve(ctx, tenant, "invoice-1", 0, "")
	require.NoError(t, err)
	got, found, err := store.Get(ctx, tenant, "invoice-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, want.ID, got.ID)
}

// A database failure is an error, never a duplicate or a missing intent.
func TestPostgres_DatabaseFailures_AreErrors(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	store := sqlstore.New(pool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, reserved, err := store.Reserve(ctx, "tenant-1", "invoice-1", 0, "")
	assert.False(t, reserved)
	assert.ErrorContains(t, err, "sendintent: reserve", "reserve")
	err = store.Record(ctx, uuid.NewString(), 1, sendintent.StatusAccepted, "m", "")
	assert.ErrorContains(t, err, "sendintent: record", "record")
	_, _, err = store.Get(ctx, "tenant-1", "invoice-1")
	assert.ErrorContains(t, err, "sendintent: get", "get")
}

// Reading an existing intent back can fail after the insert found it; so can
// the lookup that tells a stale record from an unknown intent.
func TestPostgres_ReadBackFailures(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	ctx, tenant, id := context.Background(), "tenant-"+uuid.NewString(), uuid.NewString()
	// A pending intent with a value the row scan cannot read. The table is
	// altered before the store prepares any statement on it.
	require.NoError(t, execSQL(pool, `ALTER TABLE connector_send_intents ALTER COLUMN created_at DROP DEFAULT,
		ALTER COLUMN created_at TYPE text USING 'not-a-time', ALTER COLUMN created_at SET DEFAULT 'not-a-time'`))
	require.NoError(t, execSQL(pool, `INSERT INTO connector_send_intents (id, tenant_id, message_key, status)
		VALUES ($1::uuid, $2, 'invoice-1', 'pending')`, id, tenant))

	_, reserved, err := store.Reserve(ctx, tenant, "invoice-1", 0, "")
	assert.False(t, reserved)
	assert.ErrorContains(t, err, "load existing")

	err = store.Record(ctx, id, 2, sendintent.StatusAccepted, "m", "")
	assert.ErrorIs(t, err, sendintent.ErrIntentNotFound, "an unreadable intent cannot be shown to be stale")
}

// A pending intent may be resent only once it is stale (unchanged for
// StalePendingAfter), and only by naming its attempt.
func TestPostgres_ResendOfStalePending(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	store, ctx, tenant := sqlstore.New(pool), context.Background(), "tenant-"+uuid.NewString()

	first, ok, err := store.Reserve(ctx, tenant, "k", 0, "tok-1")
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = store.Reserve(ctx, tenant, "k", first.Attempts, "tok-2")
	require.NoError(t, err)
	assert.False(t, ok, "a fresh pending attempt may still be sending")

	require.NoError(t, execSQL(pool, `UPDATE connector_send_intents SET updated_at = now() - make_interval(secs => $2) WHERE id = $1::uuid`,
		first.ID, (sendintent.StalePendingAfter+time.Second).Seconds()))
	_, ok, err = store.Reserve(ctx, tenant, "k", 0, "tok-3")
	require.NoError(t, err)
	assert.False(t, ok, "without resend a stale pending intent is never re-reserved")

	again, ok, err := store.Reserve(ctx, tenant, "k", first.Attempts, "tok-4")
	require.NoError(t, err)
	require.True(t, ok, "a stale pending attempt may be resent")
	assert.Equal(t, 2, again.Attempts)
	assert.Equal(t, "tok-4", again.ReservationToken)

	_, ok, err = store.Reserve(ctx, tenant, "k", first.Attempts, "tok-5")
	require.NoError(t, err)
	assert.False(t, ok, "the redelivered resend names a superseded attempt")
}
