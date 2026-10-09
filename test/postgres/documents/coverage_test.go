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

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// isolatedStore applies the schema in a fresh PostgreSQL schema of its own
// (search_path in the DSN) and returns a store and pool on it, so a test can
// alter the tables to provoke failures without touching the shared ones.
func isolatedStore(t *testing.T) (*sqlstore.Store, *pgcommon.Pool) {
	t.Helper()
	shared := postgres(t)
	if shared == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	schema := "cov_documents_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, exec(shared, `CREATE SCHEMA `+schema))
	t.Cleanup(func() { _ = exec(shared, `DROP SCHEMA `+schema+` CASCADE`) })

	u, err := url.Parse(os.Getenv("TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn := u.String()

	require.NoError(t, sqlstore.ApplySchema(ctx, &migrate.Runner{DSN: dsn}))
	pool, err := pgcommon.NewPool(ctx, pgcommon.Config{DSN: dsn, MaxConns: 4, PoolName: "documents_coverage_test"})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return sqlstore.New(pool), pool
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// A database failure is reported as an error, never as a lost claim or a
// missing document.
func TestPostgres_DatabaseFailures_AreErrors(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	store, ctx, identity := sqlstore.New(pool), canceled(), newIdentity()

	_, _, err := store.Claim(ctx, identity, "a", time.Minute)
	assert.ErrorContains(t, err, "documents: claim:", "claim")
	_, _, err = store.ClaimExisting(ctx, identity, "a", time.Minute)
	assert.ErrorContains(t, err, "claim takeover", "claim existing")
	_, err = store.Advance(ctx, uuid.NewString(), "a", documents.StateUploading)
	require.Error(t, err, "advance")
	assert.NotErrorIs(t, err, documents.ErrOwnershipLost, "advance")
	_, err = store.Fail(ctx, uuid.NewString(), "a", "boom")
	require.Error(t, err, "fail")
	assert.NotErrorIs(t, err, documents.ErrOwnershipLost, "fail")
	_, _, err = store.Get(ctx, identity)
	assert.ErrorContains(t, err, "documents: get", "get")
	_, err = store.PruneAttempts(ctx, time.Now(), 10)
	assert.ErrorContains(t, err, "prune attempts", "prune")
}

// When an insert conflicts but the row is gone by the takeover, Claim inserts
// again — and gives up after a bounded number of rounds rather than looping.
// A trigger that silently drops every insert makes each round end that way.
func TestPostgres_Claim_GivesUpWhenItNeverSettles(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	require.NoError(t, exec(pool, `
		CREATE FUNCTION drop_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
		CREATE TRIGGER drop_insert BEFORE INSERT ON connector_documents FOR EACH ROW EXECUTE FUNCTION drop_insert();`))

	_, claimed, err := store.Claim(context.Background(), newIdentity(), "a", time.Minute)
	assert.False(t, claimed)
	assert.ErrorContains(t, err, "did not settle")
	assert.True(t, shared.IsTransient(err), "contention passes: a retry may claim")
}

// A takeover that finds the row busy reads it back; a failure of that read
// is an error, not a lost claim.
func TestPostgres_Takeover_ReadBackFailure_IsError(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	ctx, identity := context.Background(), newIdentity()
	_, claimed, err := store.Claim(ctx, identity, "owner", time.Hour)
	require.NoError(t, err)
	require.True(t, claimed)
	// A value the row scan cannot read: the busy row is found but unreadable.
	require.NoError(t, exec(pool, `ALTER TABLE connector_documents ALTER COLUMN size_bytes TYPE text USING 'not-a-number'`))

	_, claimed, err = store.ClaimExisting(ctx, identity, "other", time.Minute)
	assert.False(t, claimed)
	require.ErrorContains(t, err, "documents: get")
	assert.NotErrorIs(t, err, documents.ErrNotFound)
}

// A row that was never claimed through Claim has no claimed_at; its audit
// record then has no start time rather than a zero timestamp.
func TestPostgres_FinishWithoutClaimTime_AuditsNullStart(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	store, identity, id := sqlstore.New(pool), newIdentity(), uuid.NewString()
	require.NoError(t, exec(pool, `
		INSERT INTO connector_documents (id, tenant_id, provider, container, filename, state, owner, lease_expires_at)
		VALUES ($1::uuid, $2, $3, $4, $5, 'UPLOADING', 'legacy', now() + interval '1 hour')`,
		id, identity.TenantID, identity.Provider, identity.Container, identity.Filename))

	_, err := store.Fail(context.Background(), id, "legacy", "boom")
	require.NoError(t, err)
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM connector_document_attempts WHERE document_id = $1::uuid AND started_at IS NULL`, id))
}

// A claim that finds the row busy reads it back; a failure of that read is an
// error, not a lost claim.
func TestPostgres_Claim_ReadBackFailure_IsError(t *testing.T) {
	t.Parallel()
	store, pool := isolatedStore(t)
	ctx, identity := context.Background(), newIdentity()
	// The busy row is written directly, and the column altered before the
	// store prepares any statement (a prepared plan must not change type).
	require.NoError(t, exec(pool, `
		INSERT INTO connector_documents (tenant_id, provider, container, filename, state, owner, lease_expires_at, claimed_at)
		VALUES ($1, $2, $3, $4, 'PENDING_UPLOAD', 'owner', now() + interval '1 hour', now())`,
		identity.TenantID, identity.Provider, identity.Container, identity.Filename))
	require.NoError(t, exec(pool, `ALTER TABLE connector_documents ALTER COLUMN size_bytes TYPE text USING 'not-a-number'`))

	_, claimed, err := store.Claim(ctx, identity, "other", time.Minute)
	assert.False(t, claimed)
	require.ErrorContains(t, err, "documents: get")
}

// ClaimExisting takes over an idle row, returns a busy row unclaimed, and
// reports a missing row as ErrNotFound — it never inserts.
func TestStore_ClaimExisting_Contract(t *testing.T) {
	forEach(t, func(t *testing.T, sc storeCase) {
		ctx, identity := context.Background(), newIdentity()

		_, claimed, err := sc.store.ClaimExisting(ctx, identity, "a", time.Minute)
		assert.False(t, claimed)
		require.ErrorIs(t, err, documents.ErrNotFound, "no row: nothing to take over")

		doc, claimed, err := sc.store.Claim(ctx, identity, "a", time.Minute)
		require.NoError(t, err)
		require.True(t, claimed)

		busy, claimed, err := sc.store.ClaimExisting(ctx, identity, "b", time.Minute)
		require.NoError(t, err)
		assert.False(t, claimed, "a live owner keeps the row")
		assert.Equal(t, "a", busy.Owner)

		_, err = sc.store.Complete(ctx, doc.ID, "a", documents.Result{ObjectID: "obj"})
		require.NoError(t, err)
		taken, claimed, err := sc.store.ClaimExisting(ctx, identity, "b", time.Minute)
		require.NoError(t, err)
		assert.True(t, claimed, "an idle row is taken over")
		assert.Equal(t, "b", taken.Owner)
		assert.Equal(t, documents.StatePendingUpload, taken.State)
	})
}
