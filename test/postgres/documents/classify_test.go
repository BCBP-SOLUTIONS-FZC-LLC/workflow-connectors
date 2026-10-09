//go:build integration

package sqlstore_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Each SQLSTATE the store can meet is raised for real by a trigger on the
// claim insert, and comes back from the Store with its retry class: passing
// database conditions are transient, defects stay unknown.
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
			require.NoError(t, exec(pool, fmt.Sprintf(`
				CREATE FUNCTION raise_code() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'injected' USING ERRCODE = '%s'; END $$;
				CREATE TRIGGER raise_code BEFORE INSERT ON connector_documents FOR EACH ROW EXECUTE FUNCTION raise_code();`, code)))

			_, _, err := store.Claim(context.Background(), newIdentity(), "a", time.Minute)
			require.Error(t, err)
			assert.Equal(t, code, pgcommon.SQLState(err), "the PostgreSQL error stays in the chain")
			class, reason := shared.ClassOf(err)
			assert.Equal(t, want.class, class)
			assert.Equal(t, want.reason, reason)
		})
	}
}

// A real lock timeout: another transaction holds the row, and the store's
// pool waits at most 100 ms for it (pgcommon applies LockTimeout to the
// transactions it opens: the audited transitions). The error is transient.
func TestPostgres_LockTimeout_IsTransient(t *testing.T) {
	t.Parallel()
	base := postgres(t)
	if base == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	ctx, identity := context.Background(), newIdentity()
	doc, claimed, err := sqlstore.New(base).Claim(ctx, identity, "owner", time.Hour)
	require.NoError(t, err)
	require.True(t, claimed)

	impatient, err := pgcommon.NewPool(ctx, pgcommon.Config{DSN: os.Getenv("TEST_POSTGRES_DSN"), MaxConns: 2, PoolName: "documents_lock_test", LockTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(impatient.Close)

	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- pgcommon.RunInTx(ctx, base, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM connector_documents WHERE id = $1::uuid FOR UPDATE`, doc.ID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second) // never hang the suite
	_, err = sqlstore.New(impatient).Fail(bounded, doc.ID, "owner", "boom")
	cancel()
	close(release)
	require.NoError(t, <-done)

	require.Error(t, err)
	assert.True(t, pgcommon.IsLockNotAvailable(err), "got %v", err)
	assert.True(t, shared.IsTransient(err))
}

// A closed pool is unknown: no retry in this (shutting-down) process can
// succeed, yet the call is not wrong, so it is not permanent either.
func TestPostgres_PoolClosed_IsUnknown(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	pool.Close()

	_, _, err := store.Get(context.Background(), newIdentity())
	require.Error(t, err)
	assert.True(t, pgcommon.IsPoolClosed(err))
	class, reason := shared.ClassOf(err)
	assert.Equal(t, shared.ClassUnknown, class)
	assert.Equal(t, "postgres pool closed", reason)
}
