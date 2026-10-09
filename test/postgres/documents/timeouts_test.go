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

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Every store statement runs in a transaction pgcommon opens, so the pool's
// LockTimeout bounds the single-statement calls too (Claim, Advance), directly
// and through PgBouncer in transaction mode.
func TestPostgres_LockTimeout_BoundsSingleStatementCalls(t *testing.T) {
	t.Parallel()
	base := postgres(t)
	if base == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	targets := map[string]pgcommon.Config{
		"direct": {DSN: os.Getenv("TEST_POSTGRES_DSN"), MaxConns: 2, PoolName: "documents_timeout_direct", LockTimeout: 100 * time.Millisecond},
	}
	if bouncer := os.Getenv("TEST_PGBOUNCER_DSN"); bouncer != "" {
		targets["pgbouncer"] = pgcommon.Config{DSN: bouncer, MaxConns: 2, PoolName: "documents_timeout_bouncer", PGBouncerMode: true, LockTimeout: 100 * time.Millisecond}
	} else if os.Getenv("CI") != "" {
		t.Fatal("CI is set but TEST_PGBOUNCER_DSN is not")
	}
	for name, cfg := range targets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, identity := context.Background(), newIdentity()
			doc, claimed, err := sqlstore.New(base).Claim(ctx, identity, "owner", time.Hour)
			require.NoError(t, err)
			require.True(t, claimed)

			impatient, err := pgcommon.NewPool(ctx, cfg)
			require.NoError(t, err)
			t.Cleanup(impatient.Close)
			store := sqlstore.New(impatient)

			release := holdRowLock(t, base, doc.ID)
			defer release()

			for call, run := range map[string]func(context.Context) error{
				"advance": func(ctx context.Context) error {
					_, err := store.Advance(ctx, doc.ID, "owner", "UPLOADING")
					return err
				},
				"claim": func(ctx context.Context) error {
					_, _, err := store.Claim(ctx, identity, "other", time.Minute)
					return err
				},
			} {
				bounded, cancel := context.WithTimeout(ctx, 10*time.Second) // never hang the suite
				start := time.Now()
				err := run(bounded)
				cancel()
				require.Errorf(t, err, "%s waited on the lock instead of timing out", call)
				assert.Truef(t, pgcommon.IsLockNotAvailable(err), "%s: got %v", call, err)
				assert.True(t, shared.IsTransient(err), call)
				assert.Less(t, time.Since(start), 5*time.Second, call)
			}
		})
	}
}

// The pool's StatementTimeout cancels a slow single statement (57014,
// transient) instead of letting it hold a connection.
func TestPostgres_StatementTimeout_BoundsSingleStatementCalls(t *testing.T) {
	t.Parallel()
	admin := postgres(t)
	if admin == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	schema := "timeout_documents_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, exec(admin, `CREATE SCHEMA `+schema))
	t.Cleanup(func() { _ = exec(admin, `DROP SCHEMA `+schema+` CASCADE`) })
	u, err := url.Parse(os.Getenv("TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	require.NoError(t, sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: u.String()}))

	pool, err := pgcommon.NewPool(ctx, pgcommon.Config{DSN: u.String(), MaxConns: 2, PoolName: "documents_statement_timeout", StatementTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, exec(pool, `
		CREATE FUNCTION slow_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(5); RETURN NEW; END $$;
		CREATE TRIGGER slow_insert BEFORE INSERT ON connector_documents FOR EACH ROW EXECUTE FUNCTION slow_insert();`))

	start := time.Now()
	_, _, err = sqlstore.New(pool).Claim(ctx, newIdentity(), "a", time.Minute)
	require.Error(t, err)
	assert.True(t, pgcommon.IsQueryCanceled(err), "got %v", err)
	assert.True(t, shared.IsTransient(err))
	assert.Less(t, time.Since(start), 3*time.Second)
}

// holdRowLock locks the document row in another transaction until release.
func holdRowLock(t *testing.T, pool *pgcommon.Pool, id string) (release func()) {
	t.Helper()
	locked, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- pgcommon.RunInTx(context.Background(), pool, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM connector_documents WHERE id = $1::uuid FOR UPDATE`, id); err != nil {
				close(locked)
				return err
			}
			close(locked)
			<-stop
			return nil
		})
	}()
	<-locked
	return func() {
		close(stop)
		require.NoError(t, <-done)
	}
}
