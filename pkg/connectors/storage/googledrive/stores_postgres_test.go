//go:build integration

package googledrive

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
)

// postgresStoreCases adds the PostgreSQL registry (through platform-pgcommon)
// when TEST_POSTGRES_DSN is set (make test-postgres; CI's postgres suite).
func postgresStoreCases(t *testing.T) []storeCase {
	// In CI the PostgreSQL legs are mandatory: a missing DSN must fail, not
	// silently skip the tests that prove the database enforces uniqueness.
	if os.Getenv("CI") != "" && os.Getenv("TEST_POSTGRES_DSN") == "" {
		t.Fatal("CI is set but TEST_POSTGRES_DSN is not: the PostgreSQL tests cannot be skipped in CI")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		return nil
	}
	pool := openTestPool(t, dsn)
	return []storeCase{{name: "postgres", new: func(*testing.T) documents.Store { return sqlstore.New(pool) }}}
}

var (
	poolOnce sync.Once
	testPool *pgcommon.Pool
	poolErr  error
)

func openTestPool(t *testing.T, dsn string) *pgcommon.Pool {
	t.Helper()
	poolOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		// The migrate runner takes its own advisory lock, so parallel test
		// packages can apply the schema at once.
		if poolErr = sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: dsn}); poolErr != nil {
			return
		}
		testPool, poolErr = pgcommon.NewPool(ctx, pgcommon.Config{DSN: dsn, MaxConns: 40, PoolName: "googledrive_test"})
	})
	require.NoError(t, poolErr)
	return testPool
}
