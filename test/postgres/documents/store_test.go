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

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
)

// The contract suite runs against the in-memory store always and against
// PostgreSQL when TEST_POSTGRES_DSN is set, so both implementations are held
// to the same semantics.

type storeCase struct {
	name  string
	store documents.Store
	pool  *pgcommon.Pool // nil for the in-memory store
}

var (
	poolOnce sync.Once
	testPool *pgcommon.Pool
	poolErr  error
)

// postgres returns a pgcommon pool on TEST_POSTGRES_DSN with the schema
// applied through ApplySchema, or nil when the DSN is unset.
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
		// The migrate runner takes its own advisory lock, so parallel test
		// packages can apply the schema at once.
		if poolErr = sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: dsn}); poolErr != nil {
			return
		}
		testPool, poolErr = pgcommon.NewPool(ctx, pgcommon.Config{DSN: dsn, MaxConns: 60, PoolName: "sqlstore_test"})
	})
	require.NoError(t, poolErr)
	return testPool
}

func stores(t *testing.T) []storeCase {
	cases := []storeCase{{name: "memory", store: documents.NewMemoryStore()}}
	if pool := postgres(t); pool != nil {
		cases = append(cases, storeCase{name: "postgres", store: sqlstore.New(pool), pool: pool})
	}
	return cases
}

// count runs a single-integer query on the test pool.
func count(t *testing.T, pool *pgcommon.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.WithConn(context.Background(), func(ctx context.Context, conn *pgcommon.Conn) error {
		return conn.QueryRow(ctx, query, args...).Scan(&n)
	}))
	return n
}

// exec runs one statement on the test pool.
func exec(pool *pgcommon.Pool, query string, args ...any) error {
	return pool.WithConn(context.Background(), func(ctx context.Context, conn *pgcommon.Conn) error {
		_, err := conn.Exec(ctx, query, args...)
		return err
	})
}

func forEach(t *testing.T, fn func(t *testing.T, sc storeCase)) {
	for _, sc := range stores(t) {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			fn(t, sc)
		})
	}
}

func newIdentity() documents.Identity {
	return documents.Identity{TenantID: "tenant-" + uuid.NewString(), Provider: "google-drive", Container: "folder", Filename: "file.pdf"}
}

func TestStore_ConcurrentClaimsOnNewIdentity_ExactlyOneWinner(t *testing.T) {
	forEach(t, func(t *testing.T, sc storeCase) {
		ctx, identity := context.Background(), newIdentity()

		const claimers = 40
		start := make(chan struct{})
		var winners atomic.Int32
		ids := make([]string, claimers)
		var wg sync.WaitGroup
		for i := range claimers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				doc, claimed, err := sc.store.Claim(ctx, identity, uuid.NewString(), time.Minute)
				if !assert.NoError(t, err) {
					return
				}
				if claimed {
					winners.Add(1)
				} else {
					assert.Equal(t, documents.StatePendingUpload, doc.State, "a loser sees the winner's row")
				}
				ids[i] = doc.ID
			}()
		}
		close(start)
		wg.Wait()

		assert.Equal(t, int32(1), winners.Load(), "exactly one claim may win")
		for _, id := range ids {
			assert.Equal(t, ids[0], id, "every caller sees the one canonical document")
		}
		if sc.pool != nil {
			assert.Equal(t, 1, count(t, sc.pool, `SELECT count(*) FROM connector_documents WHERE tenant_id = $1`, identity.TenantID))
		}
	})
}

func TestStore_ConcurrentTakeoverOfAvailableRow_ExactlyOneWinner(t *testing.T) {
	forEach(t, func(t *testing.T, sc storeCase) {
		ctx, identity := context.Background(), newIdentity()
		doc, _, err := sc.store.Claim(ctx, identity, "first", time.Minute)
		require.NoError(t, err)
		_, err = sc.store.Complete(ctx, doc.ID, "first", documents.Result{ObjectID: "file-1"})
		require.NoError(t, err)

		const claimers = 30
		start := make(chan struct{})
		var winners atomic.Int32
		var wg sync.WaitGroup
		for range claimers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got, claimed, err := sc.store.Claim(ctx, identity, uuid.NewString(), time.Minute)
				if assert.NoError(t, err) && claimed {
					winners.Add(1)
					assert.Equal(t, "file-1", got.ObjectID, "a replace keeps the recorded object")
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.Equal(t, int32(1), winners.Load(), "the compare-and-set lets exactly one re-upload own the row")
	})
}

func TestStore_TransitionsRequireOwnership(t *testing.T) {
	forEach(t, func(t *testing.T, sc storeCase) {
		ctx, identity := context.Background(), newIdentity()
		doc, _, err := sc.store.Claim(ctx, identity, "owner", time.Minute)
		require.NoError(t, err)

		_, err = sc.store.Advance(ctx, doc.ID, "intruder", documents.StateUploading)
		assert.ErrorIs(t, err, documents.ErrOwnershipLost)
		_, err = sc.store.Complete(ctx, doc.ID, "intruder", documents.Result{})
		assert.ErrorIs(t, err, documents.ErrOwnershipLost)
		_, err = sc.store.Fail(ctx, doc.ID, "intruder", "x")
		assert.ErrorIs(t, err, documents.ErrOwnershipLost)
		assert.ErrorIs(t, sc.store.Remove(ctx, doc.ID, "intruder"), documents.ErrOwnershipLost)

		advanced, err := sc.store.Advance(ctx, doc.ID, "owner", documents.StateUploading)
		require.NoError(t, err)
		assert.Equal(t, documents.StateUploading, advanced.State)
		assert.Greater(t, advanced.Version, doc.Version)

		done, err := sc.store.Complete(ctx, doc.ID, "owner", documents.Result{ObjectID: "obj", ContentType: "text/plain", SizeBytes: 3})
		require.NoError(t, err)
		assert.Equal(t, documents.StateAvailable, done.State)
		assert.Empty(t, done.Owner)
		_, err = sc.store.Complete(ctx, doc.ID, "owner", documents.Result{})
		assert.ErrorIs(t, err, documents.ErrOwnershipLost, "completing twice is refused")
	})
}

func TestStore_LiveLeaseBlocks_ExpiredLeaseIsTakenOver(t *testing.T) {
	forEach(t, func(t *testing.T, sc storeCase) {
		ctx, identity := context.Background(), newIdentity()
		// The lease must outlast the blocked claim below even under -race and
		// parallel suites (50 ms did not, on CI); then wait it out.
		const lease = time.Second
		start := time.Now()
		doc, _, err := sc.store.Claim(ctx, identity, "crashed", lease)
		require.NoError(t, err)

		blocked, claimed, err := sc.store.Claim(ctx, identity, "second", time.Minute)
		require.NoError(t, err)
		assert.False(t, claimed)
		assert.Equal(t, doc.ID, blocked.ID)

		time.Sleep(time.Until(start.Add(lease + 250*time.Millisecond)))
		taken, claimed, err := sc.store.Claim(ctx, identity, "second", time.Minute)
		require.NoError(t, err)
		assert.True(t, claimed, "an expired lease is claimable")
		assert.Equal(t, doc.ID, taken.ID, "takeover keeps the canonical row")
		assert.Equal(t, "second", taken.Owner)
	})
}

func TestStore_RemoveThenClaim_StartsANewDocument(t *testing.T) {
	forEach(t, func(t *testing.T, sc storeCase) {
		ctx, identity := context.Background(), newIdentity()
		doc, _, err := sc.store.Claim(ctx, identity, "a", time.Minute)
		require.NoError(t, err)
		require.NoError(t, sc.store.Remove(ctx, doc.ID, "a"))

		_, found, err := sc.store.Get(ctx, identity)
		require.NoError(t, err)
		assert.False(t, found)
		_, _, err = sc.store.ClaimExisting(ctx, identity, "b", time.Minute)
		assert.ErrorIs(t, err, documents.ErrNotFound)

		again, claimed, err := sc.store.Claim(ctx, identity, "b", time.Minute)
		require.NoError(t, err)
		assert.True(t, claimed)
		assert.NotEqual(t, doc.ID, again.ID)
	})
}

func TestStore_ConcurrentClaimAndRemoveCycles_NeverTwoRows(t *testing.T) {
	forEach(t, func(t *testing.T, sc storeCase) {
		ctx, identity := context.Background(), newIdentity()
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 40 {
					attempt := uuid.NewString()
					doc, claimed, err := sc.store.Claim(ctx, identity, attempt, time.Minute)
					if !assert.NoError(t, err) || !claimed {
						continue
					}
					if i%2 == 0 {
						assert.NoError(t, sc.store.Remove(ctx, doc.ID, attempt))
					} else {
						_, err := sc.store.Complete(ctx, doc.ID, attempt, documents.Result{ObjectID: "o"})
						assert.NoError(t, err)
					}
				}
			}()
		}
		wg.Wait()

		if sc.pool != nil {
			assert.LessOrEqual(t, count(t, sc.pool, `SELECT count(*) FROM connector_documents WHERE tenant_id = $1`, identity.TenantID), 1)
		}
	})
}

// ---- PostgreSQL-only: the constraints themselves --------------------------

func TestPostgres_UniqueConstraintRejectsSecondRowForIdentity(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	identity := newIdentity()
	insert := `INSERT INTO connector_documents (tenant_id, provider, container, filename, state)
	           VALUES ($1, $2, $3, $4, 'AVAILABLE')`

	require.NoError(t, exec(pool, insert, identity.TenantID, identity.Provider, identity.Container, identity.Filename))
	err := exec(pool, insert, identity.TenantID, identity.Provider, identity.Container, identity.Filename)

	require.Error(t, err)
	assert.True(t, pgcommon.IsUniqueViolation(err), "a direct duplicate insert must be refused by the database: %v", err)
	assert.Equal(t, "uq_connector_documents_identity", pgcommon.ConstraintName(err))
}

func TestPostgres_OwnerCheckConstraint(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	identity := newIdentity()

	err := exec(pool, `INSERT INTO connector_documents (tenant_id, provider, container, filename, state)
	                   VALUES ($1, $2, $3, $4, 'UPLOADING')`,
		identity.TenantID, identity.Provider, identity.Container, identity.Filename)
	require.Error(t, err)
	assert.True(t, pgcommon.IsCheckViolation(err), "a busy row without an owner must be refused: %v", err)
	assert.Equal(t, "chk_connector_documents_owner", pgcommon.ConstraintName(err))
}

func TestPostgres_ApplySchemaIsIdempotentAndTrackedSeparately(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	require.NoError(t, sqlstore.ApplySchema(context.Background(), &migrate.Runner{DSN: dsn}), "re-applying is a no-op")
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM `+sqlstore.MigrationsTable), "versions live in the package's own tracking table")
	assert.ErrorContains(t, sqlstore.ApplySchema(context.Background(), &migrate.Runner{}), "DSN")
}

func TestPostgres_FinishedAttemptsAreAudited(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	ctx, identity := context.Background(), newIdentity()
	store := sqlstore.New(pool)

	doc, _, err := store.Claim(ctx, identity, "a1", time.Minute)
	require.NoError(t, err)
	_, err = store.Fail(ctx, doc.ID, "a1", "drive 503")
	require.NoError(t, err)
	_, _, err = store.Claim(ctx, identity, "a2", time.Minute)
	require.NoError(t, err)
	_, err = store.Complete(ctx, doc.ID, "a2", documents.Result{ObjectID: "f"})
	require.NoError(t, err)
	_, _, err = store.Claim(ctx, identity, "a3", time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.Remove(ctx, doc.ID, "a3"))

	var got [][3]string
	require.NoError(t, pool.WithConn(ctx, func(ctx context.Context, conn *pgcommon.Conn) error {
		rows, err := conn.Query(ctx, `SELECT attempt, outcome, error FROM connector_document_attempts
		                              WHERE document_id = $1::uuid ORDER BY id`, doc.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r [3]string
			if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
				return err
			}
			got = append(got, r)
		}
		return rows.Err()
	}))
	assert.Equal(t, [][3]string{{"a1", "failed", "drive 503"}, {"a2", "available", ""}, {"a3", "deleted", ""}}, got,
		"every finished attempt is recorded, and the trail survives the document's deletion")
}

// PruneAttempts deletes audit rows older than the cutoff, in batches, and
// leaves newer rows alone.
func TestPostgres_PruneAttempts_DeletesOnlyOlderRowsInBatches(t *testing.T) {
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	store, ctx := sqlstore.New(pool), context.Background()

	finishOne := func(identity documents.Identity) {
		attempt := uuid.NewString()
		doc, claimed, err := store.Claim(ctx, identity, attempt, time.Minute)
		require.NoError(t, err)
		require.True(t, claimed)
		_, err = store.Fail(ctx, doc.ID, attempt, "boom")
		require.NoError(t, err)
	}
	old, fresh := newIdentity(), newIdentity()
	for range 5 {
		finishOne(old)
	}
	finishOne(fresh)
	require.NoError(t, exec(pool, `UPDATE connector_document_attempts SET finished_at = now() - interval '10 days' WHERE tenant_id = $1`, old.TenantID))

	cutoff := time.Now().Add(-24 * time.Hour)
	for {
		n, err := store.PruneAttempts(ctx, cutoff, 2)
		require.NoError(t, err)
		assert.LessOrEqual(t, n, int64(2), "never more than one batch per call")
		if n < 2 {
			break
		}
	}
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM connector_document_attempts WHERE tenant_id = $1`, old.TenantID))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM connector_document_attempts WHERE tenant_id = $1`, fresh.TenantID))

	_, err := store.PruneAttempts(ctx, cutoff, 0)
	assert.Error(t, err)
}
