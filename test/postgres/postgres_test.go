//go:build integration

// Package postgres_test runs the library's two PostgreSQL stores — the Drive
// document registry (documents/sqlstore) and send intents
// (sendintent/sqlstore) — the way a worker deployment does: migrations
// direct to PostgreSQL from several replicas at once, and queries through
// PgBouncer in transaction mode. Store semantics (the contract suites run
// against the in-memory and PostgreSQL stores) are in test/postgres/documents
// and test/postgres/sendintent; these tests cover deployment shape.
//
// Requires `make docker-up` (docker-compose.yml): TEST_POSTGRES_DSN (direct)
// and TEST_PGBOUNCER_DSN. Runs with `make test-postgres`.
package postgres_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	docsql "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	intentsql "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
)

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("CI is set but %s is not: these tests cannot be skipped in CI", name)
		}
		t.Skipf("%s not set (make test-postgres)", name)
	}
	return v
}

// freshDatabase creates an empty database on the direct PostgreSQL and
// returns its DSN; it is dropped when the test ends.
func freshDatabase(t *testing.T) string {
	t.Helper()
	admin := env(t, "TEST_POSTGRES_DSN")
	name := "wc_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	require.NoError(t, withConn(context.Background(), admin, func(ctx context.Context, conn *pgcommon.Conn) error {
		_, err := conn.Exec(ctx, "CREATE DATABASE "+name)
		return err
	}))
	t.Cleanup(func() {
		_ = withConn(context.Background(), admin, func(ctx context.Context, conn *pgcommon.Conn) error {
			_, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			return err
		})
	})

	u, err := url.Parse(admin)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

// withConn runs fn on a connection from a short-lived pgcommon pool — every
// database access here goes through platform-pgcommon, as in production.
func withConn(ctx context.Context, dsn string, fn func(context.Context, *pgcommon.Conn) error) error {
	pool, err := pgcommon.NewPool(ctx, pgcommon.Config{DSN: dsn, MaxConns: 2, PoolName: "pg_test_admin"})
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.WithConn(ctx, fn)
}

func applyAll(ctx context.Context, dsn string) error {
	runner := &migrate.Runner{DSN: dsn}
	if err := docsql.ApplySchema(ctx, runner); err != nil {
		return fmt.Errorf("documents: %w", err)
	}
	if err := intentsql.ApplySchema(ctx, runner); err != nil {
		return fmt.Errorf("send intents: %w", err)
	}
	return nil
}

// PG-01: a rolling deployment starts several replicas that all run the
// migration step at once, then restarts them (migrations run again). Both
// schemas apply exactly once, side by side, each tracked in its own table.
func TestPG01_SchemasApplyConcurrentlyAndIdempotently(t *testing.T) {
	t.Parallel()
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const replicas = 4
	var wg sync.WaitGroup
	errs := make([]error, replicas)
	for i := range replicas {
		wg.Go(func() { errs[i] = applyAll(ctx, dsn) })
	}
	wg.Wait()
	for i, err := range errs {
		require.NoErrorf(t, err, "replica %d", i)
	}
	require.NoError(t, applyAll(ctx, dsn), "re-running the migration step is a no-op")

	require.NoError(t, withConn(ctx, dsn, func(ctx context.Context, conn *pgcommon.Conn) error {
		for _, table := range []string{docsql.MigrationsTable, intentsql.MigrationsTable} {
			var version int64
			var dirty bool
			require.NoError(t, conn.QueryRow(ctx, "SELECT version, dirty FROM "+table).Scan(&version, &dirty), table)
			assert.Positive(t, version, table)
			assert.False(t, dirty, table)
		}
		for _, constraint := range []string{"uq_connector_documents_identity", "uq_connector_send_intents_key"} {
			var n int
			require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM pg_constraint WHERE conname = $1", constraint).Scan(&n))
			assert.Equalf(t, 1, n, "constraint %s", constraint)
		}
		return nil
	}))
}

// pgbouncerPool opens a pgcommon pool through PgBouncer in transaction mode,
// with the schemas applied directly beforehand (as a deployment would).
func pgbouncerPool(t *testing.T) *pgcommon.Pool {
	t.Helper()
	direct := env(t, "TEST_POSTGRES_DSN")
	bouncer := env(t, "TEST_PGBOUNCER_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, applyAll(ctx, direct))

	pool, err := pgcommon.NewPool(ctx, pgcommon.Config{DSN: bouncer, MaxConns: 40, PoolName: "pgbouncer_test", PGBouncerMode: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// PG-02: the Drive registry's uniqueness holds through PgBouncer in
// transaction mode — 40 concurrent claims, exactly one winner.
func TestPG02_DocumentRegistry_ThroughPgBouncer(t *testing.T) {
	t.Parallel()
	store := docsql.New(pgbouncerPool(t))
	ctx := context.Background()
	identity := documents.Identity{TenantID: "tenant-" + uuid.NewString(), Provider: "google-drive", Container: "folder", Filename: "contract.pdf"}

	start := make(chan struct{})
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			<-start
			_, claimed, err := store.Claim(ctx, identity, uuid.NewString(), time.Minute)
			if assert.NoError(t, err) && claimed {
				winners.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(1), winners.Load())

	doc, found, err := store.Get(ctx, identity)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, documents.StatePendingUpload, doc.State)
}

// PG-03: send intents through PgBouncer — one reservation per key under
// concurrency, an audited transition, and the not-delivered retry rule.
func TestPG03_SendIntents_ThroughPgBouncer(t *testing.T) {
	t.Parallel()
	store := intentsql.New(pgbouncerPool(t))
	ctx, tenant := context.Background(), "tenant-"+uuid.NewString()

	start := make(chan struct{})
	var reserved atomic.Int32
	var mu sync.Mutex
	var intentID string
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			<-start
			in, ok, err := store.Reserve(ctx, tenant, "invoice-42", 0, "")
			if assert.NoError(t, err) && ok {
				reserved.Add(1)
				mu.Lock()
				intentID = in.ID
				mu.Unlock()
			}
		})
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(1), reserved.Load())

	require.NoError(t, store.Record(ctx, intentID, 1, sendintent.StatusNotDelivered, "", "429 throttled"))
	again, ok, err := store.Reserve(ctx, tenant, "invoice-42", 0, "")
	require.NoError(t, err)
	assert.True(t, ok, "an automatic retry after not_delivered proceeds")
	assert.Equal(t, 2, again.Attempts)

	require.NoError(t, store.Record(ctx, intentID, again.Attempts, sendintent.StatusAccepted, "provider-msg-1", ""))
	_, ok, err = store.Reserve(ctx, tenant, "invoice-42", 0, "")
	require.NoError(t, err)
	assert.False(t, ok, "once accepted, only an explicit resend may send again")
}
