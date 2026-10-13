// Package sqlstore is the PostgreSQL implementation of sendintent.Store, on
// platform-pgcommon like the platform's other database access. The worker
// supplies its *pgcommon.Pool; apply the schema with ApplySchema. Every
// statement runs in a transaction pgcommon opens (RunInTx), so the pool's
// StatementTimeout and LockTimeout bound each call, behind PgBouncer too.
//
// Reserve is one atomic statement against uq_connector_send_intents_key
// (INSERT … ON CONFLICT DO UPDATE … WHERE the intent may be re-reserved), so
// two concurrent requests with one messageKey can never both be reserved.
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

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// MigrationsTable tracks this package's schema versions separately from the
// service's own migrations.
const MigrationsTable = "connector_send_intents_migrations"

// ApplySchema migrates connector_send_intents up to date. It reads only DSN,
// Logger and LockTimeout from runner.
func ApplySchema(ctx context.Context, runner *migrate.Runner) error {
	if runner == nil || runner.DSN == "" {
		return errors.New("sendintent: ApplySchema needs a migrate.Runner with a DSN")
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

func New(pool *pgcommon.Pool) *Store { return &Store{pool: pool} }

const columns = `id::text, tenant_id, message_key, status, attempts, reservation_token, provider_message_id, detail, created_at, updated_at`

func (s *Store) Reserve(ctx context.Context, tenantID, messageKey string, resendFrom int, token string) (sendintent.Intent, bool, error) {
	// An existing intent is re-reserved only after a not_delivered outcome,
	// or for an explicit resend naming its current attempt when that attempt
	// is finished or stale — pending, unchanged for StalePendingAfter ($3 = 0:
	// no resend). The WHERE is evaluated on the locked row, so concurrent
	// re-reservations race safely: exactly one matches.
	conflict := `DO UPDATE SET status = 'pending', attempts = connector_send_intents.attempts + 1,
			reservation_token = EXCLUDED.reservation_token, updated_at = now()
		WHERE connector_send_intents.status = 'not_delivered'
		   OR ($3 > 0 AND connector_send_intents.attempts = $3
		       AND (connector_send_intents.status <> 'pending'
		            OR connector_send_intents.updated_at <= now() - make_interval(secs => $5)))`
	in, inStatement, err := s.query(ctx, `
		INSERT INTO connector_send_intents (tenant_id, message_key, status, reservation_token)
		VALUES ($1, $2, 'pending', $4)
		ON CONFLICT ON CONSTRAINT uq_connector_send_intents_key `+conflict+`
		RETURNING `+columns, tenantID, messageKey, resendFrom, token, sendintent.StalePendingAfter.Seconds())
	if err == nil {
		return in, true, nil
	}
	if !errors.Is(err, pgcommon.ErrNoRows) {
		if inStatement {
			// The statement itself failed, so COMMIT was never sent: nothing
			// was reserved. A failure outside it (COMMIT's reply lost) is
			// ambiguous and left unmarked.
			return sendintent.Intent{}, false, fmt.Errorf("sendintent: reserve: %w: %w", sendintent.ErrNotReserved, classify(err))
		}
		return sendintent.Intent{}, false, fmt.Errorf("sendintent: reserve: %w", classify(err))
	}
	// The upsert changed nothing: this call reserved nothing, whatever the
	// read-back of the existing intent does.
	in, err = s.queryOne(ctx, `SELECT `+columns+` FROM connector_send_intents WHERE tenant_id = $1 AND message_key = $2`, tenantID, messageKey)
	if err != nil {
		return sendintent.Intent{}, false, fmt.Errorf("sendintent: reserve: load existing: %w: %w", sendintent.ErrNotReserved, classify(err))
	}
	return in, false, nil
}

func (s *Store) Record(ctx context.Context, intentID string, attempt int, status sendintent.Status, providerMessageID, detail string) error {
	// Conditional on the attempt and on pending: a late outcome of a
	// superseded attempt must not overwrite the current one.
	_, err := s.queryOne(ctx, `
		UPDATE connector_send_intents
		SET status = $3, provider_message_id = $4, detail = $5, updated_at = now()
		WHERE id = $1::uuid AND attempts = $2 AND status = 'pending'
		RETURNING `+columns, intentID, attempt, string(status), providerMessageID, detail)
	if errors.Is(err, pgcommon.ErrNoRows) {
		_, found, getErr := s.byID(ctx, intentID)
		switch {
		case getErr != nil:
			// A failed lookup is not a missing intent.
			return fmt.Errorf("sendintent: record: load intent: %w", classify(getErr))
		case found:
			return sendintent.ErrStaleRecord
		}
		return sendintent.ErrIntentNotFound
	}
	if err != nil {
		return fmt.Errorf("sendintent: record: %w", classify(err))
	}
	return nil
}

// Release marks the intent not_delivered when it is still pending under
// token (sendintent.Releaser): one conditional statement, so it can never
// release another caller's reservation or overwrite a recorded outcome.
// released reports whether it changed the intent.
func (s *Store) Release(ctx context.Context, tenantID, messageKey, token string) (bool, error) {
	if token == "" {
		return false, sendintent.ErrNoToken
	}
	_, err := s.queryOne(ctx, `
		UPDATE connector_send_intents
		SET status = 'not_delivered', detail = $4, updated_at = now()
		WHERE tenant_id = $1 AND message_key = $2 AND reservation_token = $3 AND status = 'pending'
		RETURNING `+columns, tenantID, messageKey, token, sendintent.ReleasedDetail)
	switch {
	case errors.Is(err, pgcommon.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("sendintent: release: %w", classify(err))
	}
	return true, nil
}

// PruneIntents deletes finished (not pending) intents last updated more than
// olderThan ago, at most batch rows per call, and returns how many it
// deleted. connector_send_intents otherwise grows with every send that
// carries a messageKey: run it on a schedule, repeating while it returns
// batch. Pruning reopens the duplicate window: a request with a pruned
// intent's messageKey is a new message and is sent again, so keep intents
// longer than any redelivery or replay of a workflow step can arrive (days,
// not minutes). Pending intents are never pruned (an operator resolves a
// stuck one with an explicit resend).
//
// The outer DELETE repeats the subquery's conditions: under READ COMMITTED a
// row the subquery picked that a concurrent Reserve re-reserves meanwhile
// (pending again, updated_at = now()) is re-checked against the DELETE's own
// WHERE only once the Reserve commits, so without them the re-reserved
// intent would be deleted and a redelivered job would send the email again.
// FOR UPDATE SKIP LOCKED leaves a row another transaction holds to the next
// run. The partial index ix_connector_send_intents_prune serves the scan.
func (s *Store) PruneIntents(ctx context.Context, olderThan time.Duration, batch int) (int64, error) {
	if olderThan <= 0 || batch <= 0 {
		return 0, sendintent.ErrPruneArgs
	}
	var deleted int64
	err := pgcommon.RunInTx(ctx, s.pool, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM connector_send_intents
			WHERE id IN (
				SELECT id FROM connector_send_intents
				WHERE status <> 'pending' AND updated_at < now() - make_interval(secs => $1)
				LIMIT $2
				FOR UPDATE SKIP LOCKED)
			  AND status <> 'pending' AND updated_at < now() - make_interval(secs => $1)`, olderThan.Seconds(), batch)
		deleted = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("sendintent: prune intents: %w", classify(err))
	}
	return deleted, nil
}

// Get returns the intent for tenant + messageKey.
func (s *Store) Get(ctx context.Context, tenantID, messageKey string) (sendintent.Intent, bool, error) {
	in, err := s.queryOne(ctx, `SELECT `+columns+` FROM connector_send_intents WHERE tenant_id = $1 AND message_key = $2`, tenantID, messageKey)
	if errors.Is(err, pgcommon.ErrNoRows) {
		return sendintent.Intent{}, false, nil
	}
	if err != nil {
		return sendintent.Intent{}, false, fmt.Errorf("sendintent: get: %w", classify(err))
	}
	return in, true, nil
}

// byID reads one intent by its ID.
func (s *Store) byID(ctx context.Context, intentID string) (sendintent.Intent, bool, error) {
	in, err := s.queryOne(ctx, `SELECT `+columns+` FROM connector_send_intents WHERE id = $1::uuid`, intentID)
	if errors.Is(err, pgcommon.ErrNoRows) {
		return sendintent.Intent{}, false, nil
	}
	if err != nil {
		return sendintent.Intent{}, false, err
	}
	return in, true, nil
}

func (s *Store) queryOne(ctx context.Context, stmt string, args ...any) (sendintent.Intent, error) {
	in, _, err := s.query(ctx, stmt, args...)
	return in, err
}

// query runs stmt, which returns one intent, in its own transaction.
// inStatement reports that the error came from the statement: its
// transaction was rolled back, never committed. Any other error (acquiring
// a connection, BEGIN, COMMIT) leaves it open whether a write committed.
func (s *Store) query(ctx context.Context, stmt string, args ...any) (in sendintent.Intent, inStatement bool, err error) {
	err = pgcommon.RunInTx(ctx, s.pool, pgcommon.TxOptions{}, func(ctx context.Context, tx pgcommon.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, stmt, args...).Scan(&in.ID, &in.TenantID, &in.MessageKey, &status, &in.Attempts,
			&in.ReservationToken, &in.ProviderMessageID, &in.Detail, &in.CreatedAt, &in.UpdatedAt); err != nil {
			inStatement = true
			return err
		}
		in.Status = sendintent.Status(status)
		return nil
	})
	return in, inStatement, err
}

// classify attaches a retry class to a database error, as the document
// registry's store does: contention, cancellation, failover and resource
// exhaustion are transient; a closed pool (the process is shutting down) is
// unknown; anything else is classified by its cause.
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

var (
	_ sendintent.Store    = (*Store)(nil)
	_ sendintent.Releaser = (*Store)(nil)
)
