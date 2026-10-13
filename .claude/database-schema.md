# Database Schema

The library owns three PostgreSQL tables, created in the **consuming worker's** database by migrations embedded in the library, plus a Valkey key schema and an S3 key layout for document refs. It reads no connection settings: the worker passes its `*pgcommon.Pool`, go-redis client and S3 client in.

## PostgreSQL

### Migration sets and tracking tables

| Package | Embedded files | `ApplySchema` | Tracking table (`MigrationsTable`) |
|---|---|---|---|
| `pkg/connectors/documents/sqlstore` | `migrations/000001_connector_documents.{up,down}.sql`, `migrations/000002_connector_document_attempts_retention.{up,down}.sql` | `sqlstore.ApplySchema(ctx, runner)` | `connector_documents_migrations` |
| `pkg/connectors/sendintent/sqlstore` | `migrations/000001_connector_send_intents.{up,down}.sql`, `migrations/000002_connector_send_intents_reservation_token.{up,down}.sql`, `migrations/000003_connector_send_intents_attempts_check.{up,down}.sql`, `migrations/000004_connector_send_intents_attempts_validate.{up,down}.sql`, `migrations/000005_connector_send_intents_prune_index.{up,down}.sql` | `sqlstore.ApplySchema(ctx, runner)` | `connector_send_intents_migrations` |

- `ApplySchema` builds its own `migrate.Runner` (platform-pgcommon `pkg/migrate`) from the caller's `DSN`, `Logger` and `LockTimeout` only, with this package's `FS` and `MigrationsTable`, and calls `Up`. A runner that is nil or has an empty DSN is an error.
- Separate tracking tables keep these versions from colliding with the service's own migrations (the same pattern as platform-events' `outbox.ApplySchema`).
- Run on a **direct** connection (`pgcommon.MigrationDSNFromEnv()` / `MIGRATION_DATABASE_URL`), never through PgBouncer: `migrate.Runner` takes a session-scoped advisory lock. Concurrent `ApplySchema` from several replicas is safe (`TestPG01_SchemasApplyConcurrentlyAndIdempotently`).
- Requires PostgreSQL 13+ (`gen_random_uuid()` without an extension). The DDL is idempotent (`IF NOT EXISTS` / `IF EXISTS`).
- CODEOWNERS requires the platform team on both `migrations/` directories.

### `connector_documents` — Drive document registry (`000001`)

```sql
CREATE TABLE IF NOT EXISTS connector_documents (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        text        NOT NULL,
    provider         text        NOT NULL,
    container        text        NOT NULL,      -- bucket, or Drive folder ID
    filename         text        NOT NULL,
    state            text        NOT NULL,
    version          bigint      NOT NULL DEFAULT 1,
    object_id        text        NOT NULL DEFAULT '',   -- Drive file ID once written; kept on replace and on FAILED
    content_type     text        NOT NULL DEFAULT '',
    size_bytes       bigint      NOT NULL DEFAULT 0,
    owner            text,                              -- attempt UUID of the owning call
    lease_expires_at timestamptz,
    claimed_at       timestamptz,
    last_error       text        NOT NULL DEFAULT '',
    failed_attempts  integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_connector_documents_identity UNIQUE (tenant_id, provider, container, filename),
    CONSTRAINT chk_connector_documents_state
        CHECK (state IN ('PENDING_UPLOAD', 'UPLOADING', 'AVAILABLE', 'FAILED', 'DELETING')),
    CONSTRAINT chk_connector_documents_owner
        CHECK ((state IN ('AVAILABLE', 'FAILED')) = (owner IS NULL AND lease_expires_at IS NULL))
);
```

- **`uq_connector_documents_identity` is the uniqueness authority** for Drive uploads; uniqueness never comes from reading before writing, and never from listing Drive.
- **`chk_connector_documents_owner`:** a busy row (`PENDING_UPLOAD`, `UPLOADING`, `DELETING`) always has an owner and a lease; an idle row (`AVAILABLE`, `FAILED`) never does.
- `version` increments on every transition. Today the only provider value written is `google-drive`.
- `last_error` (and the audit row's `error`) is bounded by `errorText`: invalid UTF-8 replaced, NUL removed, cut to 1 KiB with an ellipsis.
- Claim: one `INSERT … ON CONFLICT ON CONSTRAINT uq_connector_documents_identity DO UPDATE … WHERE state IN ('AVAILABLE','FAILED') OR lease_expires_at <= now() RETURNING …`. Transitions: `UPDATE … WHERE id = $1::uuid AND owner = $2 AND lease_expires_at > now()`; `Remove` is the equivalent `DELETE`. No row → `documents.ErrOwnershipLost`.

### `connector_document_attempts` — audit trail (`000001`, index `000002`)

```sql
CREATE TABLE IF NOT EXISTS connector_document_attempts (
    id          bigserial   PRIMARY KEY,
    document_id uuid        NOT NULL,        -- no FK: a deleted document keeps its trail
    tenant_id   text        NOT NULL,
    attempt     text        NOT NULL,
    outcome     text        NOT NULL,
    error       text        NOT NULL DEFAULT '',
    started_at  timestamptz,                 -- the document's claimed_at
    finished_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_connector_document_attempts_outcome CHECK (outcome IN ('available', 'failed', 'deleted'))
);
CREATE INDEX IF NOT EXISTS idx_connector_document_attempts_document    ON connector_document_attempts (document_id);
CREATE INDEX IF NOT EXISTS idx_connector_document_attempts_finished_at ON connector_document_attempts (finished_at); -- 000002
```

- One row per finished attempt, inserted in the **same transaction** as `Complete` / `Fail` / `Remove`.
- `000002`'s plain `CREATE INDEX` holds a `SHARE` lock while it builds: on a large audit table, prune first or apply it in a quiet period.
- Grows without bound unless the worker schedules `documents/sqlstore.Store.PruneAttempts(ctx, cutoff, batch)` (deletes rows with `finished_at < cutoff`, oldest first, at most `batch` per call; repeat while it returns `batch`).

### `connector_send_intents` — send-email duplicate-request protection (`000001` – `000005`)

```sql
CREATE TABLE IF NOT EXISTS connector_send_intents (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           text        NOT NULL,
    message_key         text        NOT NULL,
    status              text        NOT NULL,
    attempts            integer     NOT NULL DEFAULT 1,
    provider_message_id text        NOT NULL DEFAULT '',
    detail              text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_connector_send_intents_key UNIQUE (tenant_id, message_key),
    CONSTRAINT chk_connector_send_intents_status
        CHECK (status IN ('pending', 'accepted', 'not_delivered', 'unknown'))
);
-- 000002
ALTER TABLE connector_send_intents ADD COLUMN IF NOT EXISTS reservation_token text NOT NULL DEFAULT '';
-- 000003 (added NOT VALID, only when missing)
ALTER TABLE connector_send_intents
    ADD CONSTRAINT chk_connector_send_intents_attempts CHECK (attempts >= 1) NOT VALID;
-- 000004 (its own migration; a no-op once valid)
ALTER TABLE connector_send_intents VALIDATE CONSTRAINT chk_connector_send_intents_attempts;
-- 000005
CREATE INDEX IF NOT EXISTS ix_connector_send_intents_prune
    ON connector_send_intents (updated_at)
    WHERE status <> 'pending';
```

**Locks taken by the migrations.** golang-migrate (through pgcommon's runner) sends each file as one multi-statement query, so each file is **one implicit transaction**, and `CREATE INDEX CONCURRENTLY` is impossible.
- `000003` takes `ACCESS EXCLUSIVE` on `connector_send_intents` only for the catalog change: `NOT VALID` skips the scan. New and updated rows are checked from then on.
- `000004` validates the existing rows under `SHARE UPDATE EXCLUSIVE`: `Reserve`, `Record`, `Release`, `Get` and `PruneIntents` carry on during the scan. It is split from `000003` because in the same file the scan would run under `000003`'s `ACCESS EXCLUSIVE` lock, blocking every statement on the table. A hand-edited row with `attempts < 1` fails it until corrected.
- `000005` builds the partial index with a plain `CREATE INDEX`, under a `SHARE` lock for the build: reads go on, but `Reserve` / `Record` / `Release` / `PruneIntents` wait until it finishes. The table holds one row per `messageKey` within the retention period, so the build is short; on a very large table apply the release's migrations in a quiet period (or create the index `CONCURRENTLY` by hand beforehand under the same name: `IF NOT EXISTS` then makes `000005` a no-op).

- **`uq_connector_send_intents_key`** guarantees one intent per tenant + `messageKey`; `Reserve` is one `INSERT … ON CONFLICT ON CONSTRAINT uq_connector_send_intents_key DO UPDATE SET status = 'pending', attempts = attempts + 1, reservation_token = EXCLUDED.reservation_token … WHERE status = 'not_delivered' OR ($3 > 0 AND attempts = $3 AND (status <> 'pending' OR updated_at <= now() - make_interval(secs => $5)))` (`$5` = `StalePendingAfter`, 15 min). If nothing is returned, the existing row is read and reported as a duplicate.
- `Record`: `UPDATE … WHERE id = $1::uuid AND attempts = $2 AND status = 'pending'`; no row → `ErrStaleRecord` if the intent exists, else `ErrIntentNotFound` (a failed lookup is returned as the classified database error).
- `reservation_token` lets a caller whose `Reserve` reply was lost recognise its own reservation (`Get`, token match, still pending).
- `Reserve` errors: a failure of the upsert statement itself (its transaction rolled back, `COMMIT` never sent) or of the read-back after an upsert that changed nothing is wrapped with `sendintent.ErrNotReserved`; a failure outside the statement (acquire, `BEGIN`, `COMMIT`) is left unmarked — ambiguous, the reservation may have committed.
- `Release` (`sendintent.Releaser`): `UPDATE … SET status = 'not_delivered' WHERE tenant_id = $1 AND message_key = $2 AND reservation_token = $3 AND status = 'pending' RETURNING …`; returns `released` = a row changed. Used when an ambiguous `Reserve` failure cannot be confirmed as this call's reservation.
- `PruneIntents(ctx, olderThan, batch)` deletes up to `batch` finished (not `pending`) intents last updated before the cutoff. Pruning a key reopens its duplicate window, so keep `olderThan` beyond the longest redelivery / resend window (`docs/integration/connector-worker.md` § Operations). The statement is `DELETE FROM connector_send_intents WHERE id IN (SELECT id … WHERE status <> 'pending' AND updated_at < cutoff LIMIT $2 FOR UPDATE SKIP LOCKED) AND status <> 'pending' AND updated_at < cutoff`. The conditions are repeated on the DELETE because under READ COMMITTED a row the subquery picked that a concurrent `Reserve` re-reserves (pending, `updated_at = now()`) is re-checked after that commit only against the DELETE's own `WHERE`: with `id IN (…)` alone the re-reserved intent was deleted and a redelivered job re-inserted the key and sent a duplicate (`test/postgres/sendintent/prune_race_test.go`). `SKIP LOCKED` leaves rows another transaction holds to a later run. The scan uses the partial index `ix_connector_send_intents_prune` (`000005`).

### Transactions and timeouts

Every store statement runs in `pgcommon.RunInTx` on the worker's pool (`queryOne`, `finish`, `PruneAttempts`), so `PG_STATEMENT_TIMEOUT` and `PG_LOCK_TIMEOUT` bound it, also behind PgBouncer. No transaction is held open across a Drive or provider call. Tests: `test/postgres/documents/timeouts_test.go`, `test/postgres/sendintent/classify_test.go`.

## Valkey — document-ref metadata (`docref/valkeystore`)

| Item | Value |
|---|---|
| Key | `docref:<uuid>` — the ref ID itself (`valkeystore.Key(id)`) |
| Type | hash |
| Fields (`valkeystore.Fields`) | `reference_id`, `tenant_id`, `bucket`, `object_key`, `content_type`, `size`, `sha256`, `created_at`, `updated_at` (ms since epoch, server `TIME`); **no content field** |
| Expiry | `PEXPIREAT` = creation + `Options.TTL` (default `docref.DefaultTTL`, 24 h) |
| Create | Lua `putScript`: create only if absent; an existing hash with identical field values returns the stored timestamps (idempotent retry); any other existing hash → `docref.ErrExists` |
| Durability | script + `WAITAOF 1 <WaitReplicas> <timeout ms>` pipelined on the key's node (`Options.DisableWaitAOF` skips it — tests only) |
| Delete | Lua `deleteScript`: `DEL` only when `tenant_id` matches |
| Reads | `HGETALL` on the key's master; another tenant's hash is reported as not found; an undecodable hash → `docref.ErrIntegrityViolation` |
| Required server settings | `appendonly yes`, `appendfsync always` or `everysec`, `maxmemory-policy noeviction` (`CheckDurability`) |
| Size | a few hundred bytes per ref; memory is independent of document size |

The worker's ACL line and the per-command rationale are in `docs/runbooks/document-refs.md` (verified by `TestACL_DocumentedRulesSuffice`).

## S3 — document-ref content (`docref/s3content`)

| Item | Value |
|---|---|
| Bucket | one platform-owned bucket per environment, accessed with the **worker's** credentials (never a tenant's) |
| Object key | `<prefix><tenant>/<uuid>` — `prefix` from `docref.WithKeyPrefix` (production / compose: `docrefs/`), `<uuid>` = the ref ID without `docref:` |
| Integrity | every resolution requires the stored key to equal exactly that key for the ref's own ID and tenant, in the service's bucket, then verifies size and SHA-256 |
| Lifecycle | expire `<prefix>` after **2 days** (TTL 24 h + margin, day granularity); an object must outlive its ref |
| Missing object | only `NoSuchKey` → `docref.ErrObjectNotFound` → `ErrSourceMissing`; needs `s3:ListBucket` |
| Startup check | `Store.Check(ctx, prefix)`: `HeadBucket`, a random `<prefix>.docref-check/<random>` key must read as `NoSuchKey`, then an empty object is put and deleted there (`s3:PutObject`, `s3:DeleteObject`) |
| Immutable | the content bucket and key prefix must not change while refs are live (`Lookup` requires the exact key in the service's bucket) |
| Dev bucket | `workflow-connectors-documents` with the `docrefs/` 2-day rule, created by `scripts/init-floci.sh` in the compose floci container |

## Data NOT owned here

Workflow definitions, instances, `context_json` and history (workflow services); tenant credentials (OpenBao, resolved by the worker); the alias registry contents and its host allowlist (definition service / worker configuration); tenants' own buckets, Drive folders and mailboxes (the tenants).
