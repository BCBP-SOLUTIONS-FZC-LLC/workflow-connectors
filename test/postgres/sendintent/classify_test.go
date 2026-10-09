//go:build integration

package sqlstore_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Database errors carry their retry class, as in the document registry's
// store: passing conditions are transient, defects stay unknown.
func TestPostgres_Errors_CarryTheirRetryClass(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]struct {
		class  shared.Class
		reason string
	}{
		"40001": {shared.ClassTransient, "postgres serialization failure"},
		"40P01": {shared.ClassTransient, "postgres deadlock"},
		"55P03": {shared.ClassTransient, "postgres lock not available"},
		"57014": {shared.ClassTransient, "postgres query canceled"},
		"57P01": {shared.ClassTransient, "postgres operator intervention"},
		"08006": {shared.ClassTransient, "postgres connection exception"},
		"53300": {shared.ClassTransient, "postgres insufficient resources"},
		"23514": {shared.ClassUnknown, "unclassified"},
	} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			store, pool := isolatedStore(t)
			require.NoError(t, execSQL(pool, fmt.Sprintf(`
				CREATE FUNCTION raise_code() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'injected' USING ERRCODE = '%s'; END $$;
				CREATE TRIGGER raise_code BEFORE INSERT ON connector_send_intents FOR EACH ROW EXECUTE FUNCTION raise_code();`, code)))

			_, _, err := store.Reserve(context.Background(), "tenant", "k", 0, "tok")
			require.Error(t, err)
			assert.Equal(t, code, pgcommon.SQLState(err), "the PostgreSQL error stays in the chain")
			class, reason := shared.ClassOf(err)
			assert.Equal(t, want.class, class)
			assert.Equal(t, want.reason, reason)
		})
	}
}

// A closed pool means the process is shutting down: unknown, not retried here.
func TestPostgres_PoolClosed_IsUnknown(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	pool.Close()
	_, _, err := store.Get(context.Background(), "tenant", "k")
	require.Error(t, err)
	class, reason := shared.ClassOf(err)
	assert.Equal(t, shared.ClassUnknown, class)
	assert.Equal(t, "postgres pool closed", reason)
}

// Every store statement runs in a transaction pgcommon opens, so the pool's
// LockTimeout bounds Reserve and Record, directly and through PgBouncer.
func TestPostgres_LockTimeout_BoundsEveryCall(t *testing.T) {
	t.Parallel()
	base := postgres(t)
	if base == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	targets := map[string]pgcommon.Config{
		"direct": {DSN: os.Getenv("TEST_POSTGRES_DSN"), MaxConns: 2, PoolName: "sendintent_timeout_direct", LockTimeout: 100 * time.Millisecond},
	}
	if bouncer := os.Getenv("TEST_PGBOUNCER_DSN"); bouncer != "" {
		targets["pgbouncer"] = pgcommon.Config{DSN: bouncer, MaxConns: 2, PoolName: "sendintent_timeout_bouncer", PGBouncerMode: true, LockTimeout: 100 * time.Millisecond}
	} else if os.Getenv("CI") != "" {
		t.Fatal("CI is set but TEST_PGBOUNCER_DSN is not")
	}
	for name, cfg := range targets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, tenant := context.Background(), "tenant-"+uuid.NewString()
			in, ok, err := sqlstore.New(base).Reserve(ctx, tenant, "k", 0, "tok")
			require.NoError(t, err)
			require.True(t, ok)

			impatient, err := pgcommon.NewPool(ctx, cfg)
			require.NoError(t, err)
			t.Cleanup(impatient.Close)
			store := sqlstore.New(impatient)

			locked, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				done <- pgcommon.RunInTx(ctx, base, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
					_, err := tx.Exec(ctx, `SELECT 1 FROM connector_send_intents WHERE id = $1::uuid FOR UPDATE`, in.ID)
					close(locked)
					if err != nil {
						return err
					}
					<-stop
					return nil
				})
			}()
			<-locked
			defer func() { close(stop); require.NoError(t, <-done) }()

			for call, run := range map[string]func(context.Context) error{
				"reserve": func(ctx context.Context) error { _, _, err := store.Reserve(ctx, tenant, "k", 0, "tok-2"); return err },
				"record": func(ctx context.Context) error {
					return store.Record(ctx, in.ID, in.Attempts, sendintent.StatusAccepted, "m", "")
				},
			} {
				bounded, cancel := context.WithTimeout(ctx, 10*time.Second) // never hang the suite
				err := run(bounded)
				cancel()
				require.Errorf(t, err, "%s waited on the lock instead of timing out", call)
				assert.Truef(t, pgcommon.IsLockNotAvailable(err), "%s: got %v", call, err)
				assert.True(t, shared.IsTransient(err), call)
			}
		})
	}
}
