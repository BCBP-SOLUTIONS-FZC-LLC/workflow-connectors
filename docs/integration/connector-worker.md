# Wiring workflow-connectors into a connector worker

This guide is for the team that runs the library: `execution_service`'s `cmd/connector-worker` (LLD OQ-14 and OQ-17). It lists everything the worker must provide, in the order to do it. The library reads no configuration, opens no connection and runs no background work of its own: every item below is the worker's job.

`workflow-definition-service` needs only `pkg/registry` (README, *Definition Service — compile side*); nothing in this guide applies to it.

| Step | What | Section |
|---|---|---|
| 1 | Module, Go version, platform-pgcommon v2 | [Dependencies](#1-dependencies) |
| 2 | PostgreSQL, PgBouncer, Valkey, S3 bucket, provider network access | [Infrastructure](#2-infrastructure) |
| 3 | Environment and secrets | [Configuration](#3-configuration) |
| 4 | Pool, migrations, durability checks, `connectors.New` | [Startup](#4-startup) |
| 5 | Tenant, departments, secrets, timeout, `Execute` | [Per task](#5-per-task) |
| 6 | `DecideRetry`, backoff, logs, metrics | [Failures and retries](#6-failures-and-retries) |
| 7 | Credential rotation, audit pruning, shutdown | [Operations](#7-operations) |
| 8 | Before go-live | [Verification checklist](#8-verification-checklist) |

A complete, compiling worker skeleton is in [Appendix: worker skeleton](#appendix-worker-skeleton). It was compiled against this version of the library.

---

## 1. Dependencies

```bash
go env -w GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*
go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2@v2.0.0
go get github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2@v2.0.1
go mod tidy
```

- **Go 1.26.9 or newer.** This module's `go` directive is `1.26.9` (standard-library security fixes), so the worker's `go.mod` must say at least that.
- **platform-pgcommon v2 (v2.0.1 or newer).** The document registry and send-intent stores take a `*pgcommon.Pool` from v2. `execution_service` is on v1 today: move it first. v2.0.1 is the minimum: the stores rely on its transaction timeouts.
- **Import path `…/workflow-connectors/v2/pkg/…`.** v1.0.0 import paths have no `/v2` and do not compile against v2.
- **Private modules in CI and Docker builds.** Both modules are private: the worker's image build needs a token for `github.com/BCBP-SOLUTIONS-FZC-LLC` (for example a `GO_PRIVATE_TOKEN` build secret, as the IAM services use).

## 2. Infrastructure

### PostgreSQL

| Item | Requirement |
|---|---|
| Runtime path | Through **PgBouncer in transaction mode** (`PG_BOUNCER_MODE=true`). Every store statement runs in a transaction pgcommon opens, so it is safe under transaction pooling. |
| Migration path | **Direct** to PostgreSQL (`MIGRATION_DATABASE_URL`), with a role that may run DDL. Never PgBouncer: the migrate runner holds a session advisory lock. |
| Tables | `connector_documents`, `connector_document_attempts` (Drive document registry) and `connector_send_intents` (send-email duplicate protection). Migration versions are tracked in `connector_documents_migrations` and `connector_send_intents_migrations`, separate from the service's own. |
| Grants | The runtime role needs `SELECT, INSERT, UPDATE, DELETE` on the three tables and `USAGE` on `connector_document_attempts_id_seq` (the audit table's `bigserial`). Only the migration role needs DDL. The tables use `gen_random_uuid()` (built in since PostgreSQL 13). |
| Row-level security | The tables carry `tenant_id` and every query filters on it. If the service enables RLS on its schema, add a tenant policy for these tables too. |

### Valkey (document-ref metadata)

| Item | Requirement |
|---|---|
| Persistence | `appendonly yes`, `appendfsync always` (or `everysec`: each write still waits for its `WAITAOF` confirmation, at up to about a second's latency), `maxmemory-policy noeviction`. `valkeystore.CheckDurability` refuses anything else at startup (on every master and, on a cluster, every replica). |
| Worker user | Exactly the runbook's ACL line — the integration suite runs every operation as a user created from it: |
| | `ACL SETUSER docref-worker on ><password> resetkeys ~docref:* resetchannels -@all +eval +evalsha +exists +hmget +hset +time +pexpireat +hgetall +hget +del +waitaof +config\|get +ping` |
| | On Valkey Cluster also add `+cluster\|slots +command` (derived from go-redis; not covered by the test). |
| Client read timeout | Keep it above `valkeystore.Options.WaitAOFTimeout` (default 2 s; go-redis read timeout default 3 s). The store clamps `WaitAOFTimeout` 250 ms below the read timeout. |
| Sizing | A ref is a few hundred bytes; document size does not matter. 1,000,000 refs a day ≈ 500 MB. With `noeviction`, a full Valkey refuses new refs (transient `docref.ErrUnavailable`) instead of dropping live ones. |

Details: [`docs/runbooks/document-refs.md`](../runbooks/document-refs.md).

### S3 document bucket (document-ref content)

| Item | Requirement |
|---|---|
| Ownership | One platform-owned bucket per environment, read and written with the **worker's** IAM role — never a tenant's credentials. |
| Worker IAM | `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject` on `arn:aws:s3:::<bucket>/docrefs/*`; `s3:ListBucket` on the bucket (makes a missing object a 404 instead of a 403). Nothing else. |
| Lifecycle | Expire objects under `docrefs/` after **2 days** (ref TTL 24 h + margin). |
| Encryption | SSE-KMS with a platform key; a bucket policy denying non-TLS requests. |

### Network egress from the worker

| Destination | Used by |
|---|---|
| `api.sendgrid.com`, `email.<region>.amazonaws.com`, `graph.microsoft.com` + `login.microsoftonline.com`, `gmail.googleapis.com` + `oauth2.googleapis.com` | `send-email` providers |
| `s3.<region>.amazonaws.com`, `<account>.blob.core.windows.net`, `storage.googleapis.com`, `www.googleapis.com` + `oauth2.googleapis.com` | `storage` providers |
| Internal services named in the alias registry | `rest-call` (and `sql-query` once wired) |

Provider clients never follow redirects and speak HTTP/1.1 only, so a proxy must not require HTTP/2.

## 3. Configuration

The library reads no environment variables. The worker reads these and passes the results in.

| Variable | Read by | Value |
|---|---|---|
| `DATABASE_URL` or `PG_HOST`, `PG_PORT`, `PG_USER`, `PG_PASSWORD`, `PG_DBNAME`, `PG_SSLMODE` | `pgcommon.ConfigFromEnv` | PgBouncer address |
| `PG_BOUNCER_MODE` | `pgcommon.ConfigFromEnv` | `true` behind PgBouncer |
| `PG_STATEMENT_TIMEOUT`, `PG_LOCK_TIMEOUT` | `pgcommon.ConfigFromEnv` | For example `5s` and `2s`. They bound **every** store call; a timeout is a transient error. |
| `PG_MAX_CONNS`, `PG_MIN_CONNS`, `PG_POOL_NAME` | `pgcommon.ConfigFromEnv` | `PG_MAX_CONNS` at most the PgBouncer pool size; `PG_MIN_CONNS=0` behind PgBouncer |
| `MIGRATION_DATABASE_URL` | `pgcommon.MigrationDSNFromEnv` | Direct PostgreSQL, DDL role |
| Valkey address, user, password, TLS | The worker's go-redis client | The ACL user above |
| Document bucket name and region | The worker's `*s3.Client` | The platform bucket |
| Internal service token | `connectors.Config.InternalToken` | Required; `connectors.New` fails without it. Sent as `x-internal-token` on every `rest-call`. |
| Alias registry | `connectors.Config.Aliases` | `aliasconfig.Load(path)` (validated on load), or built in code and checked with `Validate()` |

**Tenant credentials are not configuration.** Fields the registry marks `secret_ref` (`registry.Field.IsSecretRef()`) are resolved per task from OpenBao for the job's own tenant (§5).

`.env-example` in this repository lists the same pgcommon variables for the local compose stack.

## 4. Startup

Do this once per process, and **exit on any error**: each check guards against silent loss of refs, emails or uploads.

1. **Pool.** `pgcommon.ConfigFromEnv()` (log its warnings), then `pgcommon.NewPool`.
2. **Migrations.** `documentsql.ApplySchema(ctx, runner)` and `intentsql.ApplySchema(ctx, runner)` with `runner := &migrate.Runner{DSN: pgcommon.MigrationDSNFromEnv()}`. Safe to run from every replica at once: they serialise on the advisory lock and apply each version once. Run them in the worker's migration step or at startup, before step 5.
3. **Durability checks.** `valkeystore.CheckDurability(ctx, valkey)` and `s3content.New(s3Client, bucket).Check(ctx, "docrefs/")`. `valkeystore.ErrConfigUnavailable` means a managed Valkey blocks `CONFIG` but the `WAITAOF` probe passed: verify the settings in the provider console, then decide whether to start.
4. **Document refs.** `docref.NewService(valkeystore.New(valkey, valkeystore.Options{}), content, docref.WithKeyPrefix("docrefs/"))`.
   - `Options.WaitReplicas`: 0 (default) confirms each write in the primary's AOF only; a ref can be lost if the primary fails over. Set it to the number of replicas per primary (at most) to survive a failover, at the cost of writes failing while too few replicas are up.
5. **Connectors.** `connectors.New(connectors.Config{…})` with **every** provider the registry advertises:

   | Registry provider | Constructor |
   |---|---|
   | `storage`: `aws-s3`, `azure-blob`, `gcp-gcs` | `gocloud.NewProvider` |
   | `storage`: `google-drive` | `googledrive.NewProvider(documentsql.New(pool))` |
   | `send-email`: `sendgrid` | `sendgrid.NewProvider` |
   | `send-email`: `aws-ses` | `ses.NewProvider` |
   | `send-email`: `microsoft-365` | `msgraph.NewProvider` |
   | `send-email`: `google-workspace` | `gmail.NewProvider` |

   - `DocRefs`: the service from step 4. nil disables document refs (`createDocument`, ref content and email attachments become validation errors).
   - `SendIntents`: `intentsql.New(pool)`. nil makes any `messageKey` a validation error. Wire it: it is what stops a redelivered job from sending an email twice.
   - `HTTPClient`: leave nil unless you need a custom transport. nil gives each internal call its alias timeout (else 30 s), no redirects and HTTP/1.1 only. A caller's `*http.Transport` is cloned with HTTP/2 off; any other `RoundTripper` is used as-is and must not enable HTTP/2.
   - `ChatNotifyClient`: nil until a real provider ships (LLD OQ-8); the connector then fails every call with a validation error.
   - Never register `MockStorageClient` or `MockSendEmailClient` in production.
6. **Keep the returned map** for the life of the process. Each `connectors.New` call builds new client caches.

## 5. Per task

```go
ctx = connectors.WithTenant(ctx, task.TenantID)            // required: refs, Drive registry, send intents
ctx = connectors.WithDepartments(ctx, task.Departments)   // required for rest-call
ctx, cancel := context.WithTimeout(ctx, taskTimeout)      // below documents.DefaultLease (15 min)
defer cancel()
out, err := byType[task.Type].Execute(ctx, task.Input)
```

- **Tenant.** Always the job's own tenant. Document refs, the Drive registry and send intents are scoped by it; a ref created for one tenant does not resolve for another.
- **Departments.** `rest-call` sends them as `x-departments`. A missing, empty or malformed list (an entry that is empty or contains `,`, CR or LF) fails the call with `ErrMissingInternalAuth` before anything is sent.
- **Secrets.** Resolve every `secret_ref` field for the job's own tenant immediately before `Execute` and put the values in `Input`. Keep them in memory only; never write them to `context_json`, history or logs. The library never sees an OpenBao path.
- **Timeout.** Keep the per-call timeout below `documents.DefaultLease` (15 minutes), so a live Drive upload never loses its lease (LLD DOC-6, OQ-13).
- **Outputs.** Forward the output map unchanged. It is returned with failures too: `send-email` always carries `deliveryOutcome`, and a `rest-call` status `>= 300` carries `status`, `headers` and `body`.
- **Concurrency.** `Execute` is safe for concurrent use. Bound concurrency per connector type with the worker's own pools (LLD §S6.5); the library has no rate limiter.

## 6. Failures and retries

The library never retries. **Every** automatic retry — the worker's loop, a workflow activity retry policy, queue redelivery — must ask `connectors.DecideRetry` first:

```go
d := connectors.DecideRetry(task.Type, err, aliasMethod) // aliasMethod: rest-call only
```

- `aliasMethod` is the `rest-call` alias's HTTP method: `aliasconfig.ResolveEndpoint(aliases, input["endpointAlias"]).Method`. Pass `""` for other types.
- Only a **transient** error may be retried, and only when the type's policy allows it: `storage` always; `rest-call` only for an idempotent method; `send-email` only when the provider provably never accepted the message; `chat-notify` never.
- A **permanent** or **unknown** error is never retried automatically. Unknown includes an email whose delivery is uncertain.
- Bound transient retries: exponential backoff with jitter and an attempt limit. A transient failure can persist.
- Queue redelivery counts as a retry. If the worker re-runs a job after a crash, `send-email` with a `messageKey` is safe: a duplicate of an accepted email succeeds with `duplicate: true` and sends nothing.

**Log and count every decision:**

- Log `d.LogAttrs()` (`retry`, `error_class`, `error_reason`, `retry_policy`, `retry_rule`) with the type and attempt.
- Count `connector_task_failures_total{type, provider, class, retried}` with `class = d.Class.String()` and `retried = d.Retry`. Alert on a rising `class="unknown"` rate.
- For `send-email`, also log and count `deliveryOutcome`; warn on `unknown`.

**Errors to recognise** (`errors.Is` / `errors.As`):

| Error | Meaning |
|---|---|
| `connectors.ErrValidation` | Bad input or configuration; permanent. |
| `connectors.ErrMissingInternalAuth`, `connectors.ErrMissingTenant` | The worker did not set departments or the tenant; a worker bug. |
| `connectors.ErrUpstream` | A provider or internal service failed; read the class with `connectors.ClassOf`. |
| `connectors.ErrNotDelivered`, `connectors.ErrDeliveryUnknown`; `*sendemail.SendError` | `send-email` outcome; the error carries provider, HTTP status and outcome. |
| `*sendintent.DuplicateRequestError` | A duplicate of a pending or unknown email; permanent. Its `Attempts` is what an explicit resend must name. |
| `documents.ErrUploadInProgress` (`*documents.InProgressError`) | Another call holds the Drive document; transient. |

Classify only with `connectors.ClassOf`, `IsTransient`, `IsPermanent` or `DecideRetry`. There are no `ErrTransient`/`ErrPermanent` sentinels in v2.

**Resending an email** is never automatic. An operator or caller resends with the same `messageKey`, `resend: true` and `resendAttempt: <attempts>` after checking with the provider; see [`docs/runbooks/email-delivery.md`](../runbooks/email-delivery.md). Mappings for every connector: [`docs/runbooks/retry-semantics.md`](../runbooks/retry-semantics.md).

## 7. Operations

| Task | How | When |
|---|---|---|
| Credential rotation | Call `ResetClients()` on the `storage` and `send-email` connectors (type-assert `interface{ ResetClients() }` on the map's values). Calls in flight keep their client until they finish. | After a tenant's provider credentials change |
| Drive audit pruning | `documentsql.Store.PruneAttempts(ctx, cutoff, batch)`, repeated while it returns `batch` | Daily, with the retention your audit policy requires |
| Send intents | Rows are kept for duplicate protection; there is no pruning API. Archive or delete by `created_at` only beyond the longest window in which a job can be redelivered or resent. | Per data-retention policy |
| Document refs | Nothing to run: refs expire in Valkey after 24 h and objects by the bucket lifecycle rule | — |
| Shutdown | Stop taking tasks, wait for running calls, then `pool.DrainAndClose(ctx)` and close the Valkey client | On SIGTERM |
| Valkey failover | With `WaitReplicas` 0, a ref acknowledged just before a primary failover can be lost; the consuming step then fails permanently with DocumentReferenceNotFound. Re-run the affected workflows from the producing step. | Incident |

## 8. Verification checklist

Before go-live, and after every library upgrade (read the `CHANGELOG.md` sections crossed):

- [ ] Worker `go.mod`: `workflow-connectors/v2` at the release tag, `platform-pgcommon/v2` v2.0.1+, `go 1.26.9`+.
- [ ] Migrations ran over a direct connection; the three tables and both `*_migrations` tables exist.
- [ ] Runtime pool goes through PgBouncer with `PG_BOUNCER_MODE=true`, `PG_STATEMENT_TIMEOUT` and `PG_LOCK_TIMEOUT` set.
- [ ] Startup fails when Valkey has `appendonly no`, and when the S3 role lacks `s3:ListBucket` (try both in staging).
- [ ] The worker's Valkey user is the runbook ACL line, not `default`.
- [ ] Every registry provider is registered; no mock clients.
- [ ] `SendIntents` and `DocRefs` are set.
- [ ] Every task sets `WithTenant`; `rest-call` tasks set `WithDepartments`.
- [ ] Per-call timeout below 15 minutes.
- [ ] Every retry path (loop, activity retry policy, redelivery) calls `DecideRetry`; retries are bounded with backoff.
- [ ] Failures are logged with `d.LogAttrs()` and counted by class; `deliveryOutcome` is logged for `send-email`.
- [ ] `PruneAttempts` is scheduled.
- [ ] Staging smoke test: a workflow that stores a document (`createDocument`), emails it as an attachment with a `messageKey`, and is then redelivered — exactly one email arrives and the second run reports `duplicate: true`.

---

## Appendix: worker skeleton

A minimal worker package covering startup, per-task execution with the retry gate, credential rotation, audit pruning and shutdown. It compiles against this version of the library; adapt the logging, metrics and task source to the service.

```go
// Package worker shows how a connector worker wires workflow-connectors.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/redis/go-redis/v9"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
	documentsql "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail/gmail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail/msgraph"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail/sendgrid"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail/ses"
	intentsql "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage/gocloud"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage/googledrive"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// Deps are built by the worker from its own configuration.
type Deps struct {
	Valkey        redis.UniversalClient // appendonly yes, appendfsync always, noeviction; ACL user from the runbook
	S3            *s3.Client            // the worker's own IAM role, never a tenant's
	DocsBucket    string                // platform-owned bucket, lifecycle rule on docrefs/
	Aliases       aliasconfig.Config    // the worker's internal-service alias registry
	InternalToken string                // service-to-service token for rest-call
	Log           *slog.Logger
}

type Worker struct {
	byType  map[string]connectors.Connector
	pool    *pgcommon.Pool
	docs    *documentsql.Store
	aliases aliasconfig.Config
	log     *slog.Logger
}

// docrefPrefix is the S3 key prefix of document refs; the bucket's
// lifecycle rule and the worker's IAM policy target exactly it.
const docrefPrefix = "docrefs/"

// Start wires the library once per process. Any error here must stop the
// process: running without it loses refs, emails or uploads silently.
func Start(ctx context.Context, d Deps) (*Worker, error) {
	// 1. PostgreSQL through platform-pgcommon: PG_* / DATABASE_URL,
	//    PG_BOUNCER_MODE, PG_STATEMENT_TIMEOUT, PG_LOCK_TIMEOUT.
	pgCfg, warnings := pgcommon.ConfigFromEnv()
	for _, w := range warnings {
		d.Log.Warn("pgcommon config", "warning", w)
	}
	pool, err := pgcommon.NewPool(ctx, pgCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}

	// 2. Migrations: direct to PostgreSQL (MIGRATION_DATABASE_URL), never
	//    through PgBouncer — the runner holds a session advisory lock.
	runner := &migrate.Runner{DSN: pgcommon.MigrationDSNFromEnv()}
	if err := errors.Join(documentsql.ApplySchema(ctx, runner), intentsql.ApplySchema(ctx, runner)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connector migrations: %w", err)
	}

	// 3. Document refs: content in S3, metadata in Valkey. Refuse to start
	//    when either could lose, evict or misread a ref.
	content := s3content.New(d.S3, d.DocsBucket)
	if err := errors.Join(valkeystore.CheckDurability(ctx, d.Valkey), content.Check(ctx, docrefPrefix)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("document refs: %w", err)
	}
	refs := valkeystore.New(d.Valkey, valkeystore.Options{
		// WaitReplicas: 1, // optional: survive a primary failover (see runbook)
	})
	docRefs := docref.NewService(refs, content, docref.WithKeyPrefix(docrefPrefix))

	// 4. Providers: register every one the registry advertises.
	docs := documentsql.New(pool)
	byType, err := connectors.New(connectors.Config{
		Aliases:       d.Aliases,
		InternalToken: d.InternalToken,
		// HTTPClient: nil is fine — each internal call is bounded by its
		// alias timeout (else 30 s), never follows redirects, HTTP/1.1 only.
		StorageProviders: map[string]storage.ProviderConstructor{
			"aws-s3":       gocloud.NewProvider,
			"azure-blob":   gocloud.NewProvider,
			"gcp-gcs":      gocloud.NewProvider,
			"google-drive": googledrive.NewProvider(docs),
		},
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid":         sendgrid.NewProvider,
			"aws-ses":          ses.NewProvider,
			"microsoft-365":    msgraph.NewProvider,
			"google-workspace": gmail.NewProvider,
		},
		DocRefs:     docRefs,
		SendIntents: intentsql.New(pool),
	})
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Worker{byType: byType, pool: pool, docs: docs, aliases: d.Aliases, log: d.Log}, nil
}

// Task is one connector step of a workflow instance.
type Task struct {
	Type        string         // registry.TypeStorage, TypeSendEmail, …
	TenantID    string         // the job's own tenant
	Departments []string       // dept_uuid:role pairs (rest-call)
	Input       map[string]any // secret_ref fields already resolved for TenantID
}

const (
	maxAttempts = 5
	taskTimeout = 5 * time.Minute // below documents.DefaultLease (15 min)
)

// Run executes a task, retrying only what connectors.DecideRetry allows.
func (w *Worker) Run(ctx context.Context, t Task) (map[string]any, error) {
	ctx = connectors.WithTenant(ctx, t.TenantID)
	if len(t.Departments) > 0 {
		ctx = connectors.WithDepartments(ctx, t.Departments)
	}
	c, ok := w.byType[t.Type]
	if !ok {
		return nil, fmt.Errorf("unknown connector type %q", t.Type)
	}
	method := w.httpMethod(t)

	for attempt := 1; ; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, taskTimeout)
		out, err := c.Execute(callCtx, t.Input)
		cancel()
		if err == nil {
			return out, nil
		}

		d := connectors.DecideRetry(t.Type, err, method)
		attrs := append(d.LogAttrs(), slog.String("type", t.Type), slog.Int("attempt", attempt))
		if outcome, ok := out["deliveryOutcome"]; ok {
			attrs = append(attrs, slog.Any("delivery_outcome", outcome))
		}
		w.log.LogAttrs(ctx, slog.LevelWarn, "connector call failed", attrs...)
		// metrics: connector_task_failures_total{type, provider, class=d.Class.String(), retried=d.Retry}

		if !d.Retry || attempt >= maxAttempts {
			return out, err // forward out with the failure: it carries deliveryOutcome / status
		}
		backoff := time.Duration(1<<attempt)*time.Second + time.Duration(rand.Int64N(int64(time.Second)))
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(backoff):
		}
	}
}

// httpMethod is what DecideRetry needs for rest-call: the alias's method.
func (w *Worker) httpMethod(t Task) string {
	if t.Type != registry.TypeRestCall {
		return ""
	}
	alias, _ := t.Input["endpointAlias"].(string)
	ep, err := aliasconfig.ResolveEndpoint(w.aliases, alias)
	if err != nil {
		return "" // the call itself fails with ErrValidation
	}
	return ep.Method
}

// RotateCredentials drops cached provider clients after tenant credentials
// change; calls in flight keep their client until they finish.
func (w *Worker) RotateCredentials() {
	for _, c := range w.byType {
		if r, ok := c.(interface{ ResetClients() }); ok {
			r.ResetClients()
		}
	}
}

// PruneAudit trims the Drive upload audit table; run it daily.
func (w *Worker) PruneAudit(ctx context.Context, retention time.Duration) error {
	for {
		n, err := w.docs.PruneAttempts(ctx, time.Now().Add(-retention), 1000)
		if err != nil || n < 1000 {
			return err
		}
	}
}

// Close drains the pool on shutdown.
func (w *Worker) Close(ctx context.Context) error { return w.pool.DrainAndClose(ctx) }
```
