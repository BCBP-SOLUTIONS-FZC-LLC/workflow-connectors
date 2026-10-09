// Package sqlstore is the PostgreSQL implementation of documents.Store, built
// on platform-pgcommon like every other service database access on the
// platform.
//
// The worker supplies its *pgcommon.Pool, so this store inherits the pool's
// configuration — RLS GUC injection, PgBouncer mode, query metrics and
// tracing. It opens no connection of its own. Every statement runs in a
// transaction pgcommon opens (RunInTx), so the pool's StatementTimeout and
// LockTimeout (PG_STATEMENT_TIMEOUT / PG_LOCK_TIMEOUT) bound each call, behind
// PgBouncer too.
// Apply the schema with ApplySchema (from the worker's migration step) before
// use.
//
// Every method is atomic: Claim is one INSERT … ON CONFLICT DO UPDATE … WHERE
// against uq_connector_documents_identity (insert, or take over an idle or
// expired row); the other transitions are a single conditional UPDATE or DELETE on
// (id, owner, live lease), with the audit insert in the same transaction
// (pgcommon.RunInTx). Uniqueness never depends on reading before writing.
package sqlstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// MigrationsTable tracks this package's schema versions, separately from the
// service's own migrations so the version numbers never collide (the same rule
// as platform-events' outbox.ApplySchema).
const MigrationsTable = "connector_documents_migrations"

// ApplySchema migrates connector_documents and connector_document_attempts
// up to date. It reads only DSN, Logger and LockTimeout from runner — pass
// the service's own migration Runner, typically from the same migrate step
// that applies the service schema — and tracks versions in MigrationsTable.
func ApplySchema(ctx context.Context, runner *migrate.Runner) error {
	if runner == nil || runner.DSN == "" {
		return errors.New("documents: ApplySchema needs a migrate.Runner with a DSN")
	}
	// fs.Sub fails only for an invalid path, and "migrations" is valid.
	sub, _ := fs.Sub(migrationFS, "migrations")
	return (&migrate.Runner{
		FS:              sub,
		DSN:             runner.DSN,
		Logger:          runner.Logger,
		LockTimeout:     runner.LockTimeout,
		MigrationsTable: MigrationsTable,
	}).Up(ctx)
}

type Store struct {
	pool *pgcommon.Pool
}

// New returns a Store on the worker's pool.
func New(pool *pgcommon.Pool) *Store { return &Store{pool: pool} }

const columns = `id::text, tenant_id, provider, container, filename, state, version, object_id,
	content_type, size_bytes, owner, lease_expires_at, claimed_at, last_error,
	failed_attempts, created_at, updated_at`

// claimRetries bounds Claim's loop when a concurrent delete removes a busy
// row between the claim and the read of its owner.
const claimRetries = 3

func (s *Store) Claim(ctx context.Context, identity documents.Identity, attempt string, lease time.Duration) (documents.Document, bool, error) {
	for range claimRetries {
		// One statement: insert the row, or take over an idle row or one
		// whose lease expired. The conflict is resolved on the locked row, so
		// a concurrent delete cannot slip between an insert and a takeover.
		doc, err := s.queryOne(ctx, `
			INSERT INTO connector_documents
				(tenant_id, provider, container, filename, state, owner, lease_expires_at, claimed_at)
			VALUES ($1, $2, $3, $4, 'PENDING_UPLOAD', $5, now() + $6 * interval '1 millisecond', now())
			ON CONFLICT ON CONSTRAINT uq_connector_documents_identity DO UPDATE
			SET state = 'PENDING_UPLOAD', version = connector_documents.version + 1, owner = EXCLUDED.owner,
				lease_expires_at = EXCLUDED.lease_expires_at, claimed_at = now(), updated_at = now()
			WHERE connector_documents.state IN ('AVAILABLE', 'FAILED') OR connector_documents.lease_expires_at <= now()
			RETURNING `+columns,
			identity.TenantID, identity.Provider, identity.Container, identity.Filename, attempt, lease.Milliseconds())
		if err == nil {
			return doc, true, nil
		}
		if !errors.Is(err, pgcommon.ErrNoRows) {
			return documents.Document{}, false, fmt.Errorf("documents: claim: %w", classify(err))
		}

		// The row exists and a live call owns it: return it unclaimed.
		doc, found, err := s.Get(ctx, identity)
		if err != nil {
			return documents.Document{}, false, err
		}
		if found {
			return doc, false, nil
		}
		// The busy row was deleted since: claim again.
	}
	return documents.Document{}, false, shared.Transient("claim contention", fmt.Errorf("documents: claim did not settle after %d attempts", claimRetries))
}

func (s *Store) ClaimExisting(ctx context.Context, identity documents.Identity, attempt string, lease time.Duration) (documents.Document, bool, error) {
	return s.takeover(ctx, identity, attempt, lease)
}

// takeover claims an idle row, or one whose lease expired, by compare-and-set.
// When the row is owned by a live call it returns that row unclaimed, and
// ErrNotFound when there is no row.
func (s *Store) takeover(ctx context.Context, identity documents.Identity, attempt string, lease time.Duration) (documents.Document, bool, error) {
	doc, err := s.queryOne(ctx, `
		UPDATE connector_documents
		SET state = 'PENDING_UPLOAD', version = version + 1, owner = $5,
			lease_expires_at = now() + $6 * interval '1 millisecond', claimed_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND provider = $2 AND container = $3 AND filename = $4
			AND (state IN ('AVAILABLE', 'FAILED') OR lease_expires_at <= now())
		RETURNING `+columns,
		identity.TenantID, identity.Provider, identity.Container, identity.Filename, attempt, lease.Milliseconds())
	if err == nil {
		return doc, true, nil
	}
	if !errors.Is(err, pgcommon.ErrNoRows) {
		return documents.Document{}, false, fmt.Errorf("documents: claim takeover: %w", classify(err))
	}

	doc, found, err := s.Get(ctx, identity)
	if err != nil {
		return documents.Document{}, false, err
	}
	if !found {
		return documents.Document{}, false, documents.ErrNotFound
	}
	return doc, false, nil
}

func (s *Store) Advance(ctx context.Context, docID, attempt string, to documents.State) (documents.Document, error) {
	doc, err := s.queryOne(ctx, `
		UPDATE connector_documents
		SET state = $3, version = version + 1, updated_at = now()
		WHERE id = $1::uuid AND owner = $2 AND lease_expires_at > now()
		RETURNING `+columns, docID, attempt, string(to))
	if errors.Is(err, pgcommon.ErrNoRows) {
		return documents.Document{}, documents.ErrOwnershipLost
	}
	if err != nil {
		return documents.Document{}, fmt.Errorf("documents: advance: %w", classify(err))
	}
	return doc, nil
}

func (s *Store) Complete(ctx context.Context, docID, attempt string, result documents.Result) (documents.Document, error) {
	return s.finish(ctx, attempt, "available", "", `
		UPDATE connector_documents
		SET state = 'AVAILABLE', version = version + 1, object_id = $3, content_type = $4, size_bytes = $5,
			last_error = '', owner = NULL, lease_expires_at = NULL, updated_at = now()
		WHERE id = $1::uuid AND owner = $2 AND lease_expires_at > now()
		RETURNING `+columns, docID, attempt, result.ObjectID, result.ContentType, result.SizeBytes)
}

func (s *Store) Fail(ctx context.Context, docID, attempt, reason string) (documents.Document, error) {
	return s.finish(ctx, attempt, "failed", reason, `
		UPDATE connector_documents
		SET state = 'FAILED', version = version + 1, last_error = $3, failed_attempts = failed_attempts + 1,
			owner = NULL, lease_expires_at = NULL, updated_at = now()
		WHERE id = $1::uuid AND owner = $2 AND lease_expires_at > now()
		RETURNING `+columns, docID, attempt, reason)
}

func (s *Store) Remove(ctx context.Context, docID, attempt string) error {
	_, err := s.finish(ctx, attempt, "deleted", "", `
		DELETE FROM connector_documents
		WHERE id = $1::uuid AND owner = $2 AND lease_expires_at > now()
		RETURNING `+columns, docID, attempt)
	return err
}

// finish runs one owner-checked UPDATE or DELETE and appends the audit record
// in a single transaction, so a document never changes state without its
// attempt being recorded.
func (s *Store) finish(ctx context.Context, attempt, outcome, reason, stmt string, args ...any) (documents.Document, error) {
	var doc documents.Document
	err := pgcommon.RunInTx(ctx, s.pool, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
		var err error
		if doc, err = scan(tx.QueryRow(ctx, stmt, args...)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO connector_document_attempts (document_id, tenant_id, attempt, outcome, error, started_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6)`,
			doc.ID, doc.TenantID, attempt, outcome, reason, nullTime(doc.ClaimedAt))
		return err
	})
	if errors.Is(err, pgcommon.ErrNoRows) {
		return documents.Document{}, documents.ErrOwnershipLost
	}
	if err != nil {
		return documents.Document{}, fmt.Errorf("documents: %s: %w", outcome, classify(err))
	}
	return doc, nil
}

func (s *Store) Get(ctx context.Context, identity documents.Identity) (documents.Document, bool, error) {
	doc, err := s.queryOne(ctx, `
		SELECT `+columns+` FROM connector_documents
		WHERE tenant_id = $1 AND provider = $2 AND container = $3 AND filename = $4`,
		identity.TenantID, identity.Provider, identity.Container, identity.Filename)
	if errors.Is(err, pgcommon.ErrNoRows) {
		return documents.Document{}, false, nil
	}
	if err != nil {
		return documents.Document{}, false, fmt.Errorf("documents: get: %w", classify(err))
	}
	return doc, true, nil
}

// PruneAttempts deletes audit rows (connector_document_attempts) that
// finished before cutoff, at most batch rows per call, and returns how many it
// deleted. The audit table otherwise grows with every upload and delete: run
// it on a schedule (for example daily, with the retention your audit policy
// requires), repeating while it returns batch.
func (s *Store) PruneAttempts(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	if batch <= 0 {
		return 0, fmt.Errorf("documents: prune attempts: batch must be positive")
	}
	var deleted int64
	err := pgcommon.RunInTx(ctx, s.pool, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM connector_document_attempts
			WHERE id IN (
				SELECT id FROM connector_document_attempts
				WHERE finished_at < $1
				ORDER BY finished_at
				LIMIT $2)`, cutoff, batch)
		deleted = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("documents: prune attempts: %w", classify(err))
	}
	return deleted, nil
}

// classify attaches a retry class to a database error, so every consumer of
// the store — the Drive adapter, and through it the worker's DecideRetry —
// reads a passing database condition as transient: a serialization failure,
// a deadlock, a lock or statement timeout (lock_not_available,
// query_canceled), an operator intervention (admin or crash shutdown, "cannot
// connect now"), a connection exception or insufficient resources (too many
// connections, out of memory or disk). Network and context failures are
// classified by their cause (a deadline is transient, a cancellation unknown).
//
// A closed pool is unknown, not transient: the pool is closed only when this
// process is shutting down, so no retry here can succeed, yet the same call
// would succeed on another replica, so it is not permanent either. Unknown is
// never retried automatically, and the task is left for redelivery.
// Anything else (a constraint violation, a schema error) is a defect: unknown.
func classify(err error) error {
	switch {
	case pgcommon.IsSerializationFailure(err):
		return shared.Transient("postgres serialization failure", err)
	case pgcommon.IsDeadlock(err):
		return shared.Transient("postgres deadlock", err)
	case pgcommon.IsLockNotAvailable(err):
		return shared.Transient("postgres lock not available", err)
	case pgcommon.IsQueryCanceled(err):
		return shared.Transient("postgres query canceled", err)
	case pgcommon.IsOperatorIntervention(err):
		return shared.Transient("postgres operator intervention", err)
	case pgcommon.IsConnectionException(err):
		return shared.Transient("postgres connection exception", err)
	case pgcommon.IsInsufficientResources(err):
		return shared.Transient("postgres insufficient resources", err)
	case pgcommon.IsPoolClosed(err):
		return shared.WithClass(shared.ClassUnknown, "postgres pool closed", err)
	}
	return shared.ClassifyByCause(err)
}

// queryOne runs one statement returning at most one row in its own
// transaction (pgcommon.RunInTx), so the pool's statement and lock timeouts
// apply to it.
func (s *Store) queryOne(ctx context.Context, stmt string, args ...any) (documents.Document, error) {
	var doc documents.Document
	err := pgcommon.RunInTx(ctx, s.pool, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
		var err error
		doc, err = scan(tx.QueryRow(ctx, stmt, args...))
		return err
	})
	return doc, err
}

func scan(row pgcommon.Row) (documents.Document, error) {
	var (
		d                documents.Document
		state            string
		owner            *string
		lease, claimedAt *time.Time
	)
	err := row.Scan(&d.ID, &d.TenantID, &d.Provider, &d.Container, &d.Filename, &state, &d.Version,
		&d.ObjectID, &d.ContentType, &d.SizeBytes, &owner, &lease, &claimedAt, &d.LastError,
		&d.FailedAttempts, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return documents.Document{}, err
	}
	d.State = documents.State(state)
	if owner != nil {
		d.Owner = *owner
	}
	if lease != nil {
		d.LeaseExpiresAt = *lease
	}
	if claimedAt != nil {
		d.ClaimedAt = *claimedAt
	}
	return d, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

var _ documents.Store = (*Store)(nil)
