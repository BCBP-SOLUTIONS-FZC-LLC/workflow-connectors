# Runbook — document refs (S3 content, Valkey metadata)

Document refs (`docref:<uuid>`) let one connector's output be another's input. A `storage` fetch or upload with `createDocument: true` stores the document and returns a ref; a later `send-email` attachment or `storage` upload resolves it — on any worker replica, after any restart or deployment.

## The model

| | Store | Holds |
|---|---|---|
| **Content** | **S3** — the platform's document bucket (`docref/s3content`) | The document bytes: one object per ref at `<prefix><tenant_id>/<uuid>`. S3 is the system of record for content |
| **Metadata** | **Valkey** (`docref/valkeystore`) | One small hash per ref at `docref:{uuid}`: `reference_id`, `tenant_id`, `bucket`, `object_key`, `content_type`, `size`, `sha256`, `created_at`, `updated_at`. **Never content** |

A ref is a pointer to an S3 object, not a snapshot. Consumers never read content from Valkey: they resolve the ref through `docref.Service`, which reads the metadata, checks the tenant, fetches the object from S3 and verifies it.

**Write** (`Service.Create`): upload the object to S3 → compute SHA-256 → write the metadata to Valkey (create-only and idempotent — a client retry after a lost reply finds its own identical write and succeeds, any other existing hash is `ErrExists`; confirmed in the AOF with `WAITAOF`) → return the ref. The object is written first, so a returned ref always points at an existing object. If the metadata write fails, the object is deleted (best effort; the lifecycle rule removes any leftover).

**Resolve** (`Service.Open` streams, `Service.Read` returns bytes up to a limit):

1. Read the metadata from Valkey. None, expired, or another tenant's → `ErrNotFound`.
2. Check the tenant: the record's `tenant_id` must be the caller's, its `bucket` the configured bucket, and its `object_key` exactly `<prefix><tenant_id>/<uuid>` for the ref's own ID — not merely somewhere under the tenant's prefix. Otherwise `ErrIntegrityViolation`, and S3 is never called.
3. Fetch the object from S3. Missing → `ErrSourceMissing`.
4. Verify: the object's length must equal `size` (checked before reading), and the streamed bytes must hash to `sha256` (checked at the end of the stream). Otherwise `ErrIntegrityViolation`.

| Spec name | Go error | Connector outcome |
|---|---|---|
| DocumentReferenceNotFound | `docref.ErrNotFound` | `ErrValidation` — the task fails, not retried |
| DocumentSourceMissing | `docref.ErrSourceMissing` | `ErrValidation` |
| DocumentIntegrityViolation | `docref.ErrIntegrityViolation` | `ErrValidation` |
| Valkey or S3 unavailable | (wrapped store error) | `storage`: `ErrUpstream` (retryable); `send-email`: `ErrNotDelivered` (nothing sent) |

`errors.Is(err, docref.ErrSourceMissing)` (and the others) work on the connector error. `send-email` and `storage` upload read and verify the whole document **before** sending or uploading anything.

**Streaming.** `Service.Open` returns a reader that hashes as it streams; a large document never sits in memory. Because the checksum is known only at the end, a consumer of `Open` must treat the content as valid only after reading to `io.EOF` without error — the reader returns `ErrIntegrityViolation` in place of `io.EOF` on a mismatch. `send-email` (25 MiB total) and `storage` upload (50 MiB) use `Service.Read`, which refuses an oversize document from its metadata before downloading it.

## S3 document bucket

| Setting | Value | Why |
|---|---|---|
| Ownership | Platform-owned bucket, one per environment | Refs resolve with the **worker's** credentials (IAM role / credential chain), never a tenant's — `send-email` has no storage credentials, and tenant sources may be Azure, GCS or Drive |
| Worker IAM | `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject` on `arn:aws:s3:::<bucket>/<prefix>*`; `s3:ListBucket` on the bucket (makes a missing object a 404, not a 403); nothing else | Least privilege |
| Encryption | SSE-KMS with a platform key; bucket policy denying non-TLS requests | Objects are customer documents |
| Public access | Block all | |
| Lifecycle rule | Expire objects under `<prefix>` after **2 days** (ref TTL 24 h + margin; S3 lifecycle has day granularity) | An object must outlive its ref; orphans from failed writes are removed |
| Versioning | Optional. With versioning on, also expire noncurrent versions after 2 days | An overwrite is detected by the checksum either way |
| Object Lock | Off | Refs must be deletable |

The library takes an `s3.Client` from the worker: `s3content.New(client, bucket)`, and `docref.WithKeyPrefix("docrefs/")` on `docref.NewService`. Call `s3content.Store.Check(ctx, prefix)` at startup: it checks the bucket is reachable (HeadBucket) and that a missing object reads as `NoSuchKey`. Without `s3:ListBucket` S3 answers 403 for a missing object, which would turn a deleted document into a retried access error; `Check` fails instead. Only `NoSuchKey` counts as a missing document (`ErrSourceMissing`); a missing bucket is an infrastructure fault.

**Integrity.** Anyone who can write to the bucket can replace an object; the stored SHA-256 makes that detectable — every resolution of a replaced object fails with `ErrIntegrityViolation`, it is never served. Restrict write access to the workers.

## Valkey (metadata)

| Setting | Required value | Why |
|---|---|---|
| `appendonly` | `yes` | Losing the metadata loses the ref (the object becomes unreachable) |
| `appendfsync` | `always` (recommended) or `everysec` | |
| `maxmemory-policy` | `noeviction` | Ref keys carry a TTL, so any `volatile-*` or `allkeys-*` policy could evict a live ref; with `noeviction` a full Valkey refuses new refs (retryable) instead |
| `maxmemory` | refs per 24 h × ~500 bytes, with headroom | Memory is independent of document size |

`valkeystore.CheckDurability(ctx, client)` runs at worker startup on the server, or on every master of a cluster: first a real `WAITAOF` probe — which fails when the append-only file is off even where `CONFIG` is blocked — then `appendonly`, `appendfsync` and `maxmemory-policy`. On a cluster it also checks those three settings on every replica (a replica is promoted on failover); a standalone or Sentinel client cannot enumerate replicas, so check theirs by hand (`CONFIG GET` on each replica, or the provider console). Fail to start on an error; `ErrConfigUnavailable` (managed Valkey that blocks `CONFIG`, probe passed) means verify the settings in the provider console. `Put` sends the create script and `WAITAOF` as one pipeline on the key's node — `WAITAOF` confirms only its own connection's writes — and returns `valkeystore.ErrNotPersisted` if the write did not reach the AOF. Supported clients: `*redis.Client` (standalone, Sentinel) and `*redis.ClusterClient`. A hash the store could not have written is `ErrIntegrityViolation`.

**Sizing.** Each ref hash is a few hundred bytes (`MEMORY USAGE docref:<uuid>`; the test suite asserts < 1 KiB). 10,000 refs ≈ 5 MB; 1,000,000 refs per day ≈ 500 MB. Document size does not matter.

**Worker ACL.** Give the workers' user exactly these rules (replace `<password>`; the integration test `TestACL_DocumentedRulesSuffice` creates a user from this line and runs `Put`, `Get`, `Delete` and `CheckDurability` as it):

```
ACL SETUSER docref-worker on ><password> resetkeys ~docref:* resetchannels -@all +eval +evalsha +exists +hmget +hset +time +pexpireat +hgetall +hget +del +waitaof +config|get +ping
```

Why each command: `EVAL`/`EVALSHA` run the create and delete scripts, and Valkey checks the commands a script calls against the same user — the create script calls `EXISTS`, `HMGET` (idempotent retry), `HSET`, `TIME`, `PEXPIREAT`; the delete script `HGET`, `DEL`. `Get` is `HGETALL`; `Put` confirms with `WAITAOF`; `CheckDurability` runs `WAITAOF` and `CONFIG GET` at startup; `PING` is the health check. `HELLO` and `AUTH` need no grant; go-redis ignores a refused `CLIENT SETINFO` (add `+client|setinfo` to see the library name in `CLIENT LIST`). `-@all` leaves `FLUSHALL`/`FLUSHDB`, `KEYS` and every other command denied, and `~docref:*` confines the user to ref keys.

On Valkey Cluster add `+cluster|slots +command` (the client's slot map and command table) — derived from the go-redis cluster client, not exercised by the test suite, which runs a standalone server. Refs are written and read on the key's master. A redirect during resharding (`MOVED`, `ASK`, `TRYAGAIN`, `CLUSTERDOWN`), a node still `LOADING`, a demoted primary (`READONLY`), a replica cut off from its primary (`MASTERDOWN`), a full Valkey (`OOM`) and `NOREPLICAS` (`min-replicas-to-write` not met) are all reported as `docref.ErrUnavailable` (transient — the command was refused, not applied); on a cluster the topology replies also reload the slot map.

## Retention

- Refs expire 24 h after creation (`valkeystore.Options.TTL`), by Valkey key expiry; no prune job.
- **Keep the bucket's lifecycle rule at least TTL + 1 day** (2 days for the default 24 h): an object removed before its ref expires turns a live ref into `ErrSourceMissing`.
- `WaitAOFTimeout` must stay below the Valkey client's read timeout (go-redis default 3 s); `valkeystore.New` clamps it to 250 ms under it, since a `WAITAOF` still blocking when the client stops reading would fail every write.
- Objects expire by the bucket lifecycle rule (2 days). Between the two, an object exists without a ref; nothing can resolve it.
- `Service.Delete(tenant, id)` removes the ref first (it stops resolving at once), then the object. If the object delete fails, the lifecycle rule removes it.
- Tenant offboarding: delete the `<prefix><tenant_id>/` prefix in S3; the tenant's refs then resolve to `ErrSourceMissing` until they expire.

## Backup and restore

Refs live at most 24 h, so backups protect against loss of a store, not long-term retention.

- **S3**: S3's own durability covers object loss; cross-region replication only if the environment needs regional failover. No separate backup.
- **Valkey**: keep the AOF on a persistent volume; take RDB snapshots (`save` or the managed schedule); copy `appendonlydir/` and `dump.rdb` off-host hourly, encrypted, kept 48 h. The data is small (metadata only).

Restore Valkey: stop the workers; stop Valkey; restore `appendonlydir/` (preferred) or `dump.rdb` (then `CONFIG SET appendonly yes` and wait for the rewrite); start Valkey and check `INFO persistence`; start the workers (`CheckDurability` must pass). Refs whose objects the lifecycle rule has since removed resolve to `ErrSourceMissing`; refs created after the backup resolve to `ErrNotFound` — re-run the producing step.

## Failover

**By default a ref acknowledged to the workflow can be lost on a primary failover.** `Put` confirms each write with `WAITAOF 1 0`: the write is in the **primary's own** append-only file, and nothing is required of any replica. If the primary fails and a replica that had not yet received the write is promoted, the ref is gone even though `Put` (and the producing step) succeeded. Replication is asynchronous; a primary restart on its own disk loses nothing, a failover can.

What happens then: the consuming step resolves the ref and gets `ErrNotFound` — **DocumentReferenceNotFound**, a permanent failure (`ErrValidation`, not retried). There is no automatic re-run of the producing step. The S3 object is not lost but is unreachable (no ref points at it) until the lifecycle rule removes it.

Operator action after a failover:

1. Note the failover time (Sentinel/cluster logs, or the provider's event) and the replication lag reported before it.
2. Find workflow instances whose consuming step failed with DocumentReferenceNotFound on a ref created shortly before the failover (worker logs carry the ref ID and tenant).
3. Re-run those instances from the **producing** step (the storage fetch or upload with `createDocument: true`), which creates a new ref; re-running only the consuming step fails again.

To close the gap, require replica acknowledgement: `valkeystore.Options{WaitReplicas: n}` makes `Put` send `WAITAOF 1 n` and fail with `ErrNotPersisted` (transient) unless the primary **and** `n` replicas have the write in their AOF. Use it with `appendonly yes` on the replicas (`CheckDurability` checks this on a cluster) and `n` at most the replicas per primary — with fewer healthy replicas than `n` every `Put` fails until one is back, so `n = 1` with two replicas per primary keeps writes available through a single replica loss. With `n` below the replica count a ref can still be lost if the failover promotes a replica that did not acknowledge it; with `n` equal to the replica count every promotable replica has it.

## Monitoring

| Signal | Source | Alert |
|---|---|---|
| Valkey memory | `used_memory / maxmemory` | > 75 % |
| Evictions | `evicted_keys` | > 0 |
| AOF health | `aof_last_write_status` | not `ok` |
| Not persisted | `valkeystore.ErrNotPersisted` in worker logs | any |
| Integrity | `ErrIntegrityViolation` | any — an object was altered, or a record was forged |
| Missing sources | `ErrSourceMissing` | above baseline (lifecycle rule too short, or manual deletes) |
| Unresolvable refs | `ErrNotFound` | above baseline |
| S3 errors | 5xx / throttling on the bucket | above baseline |

## Verifying

`make test-integration` (and CI) runs the store and multi-replica suites against real PostgreSQL, Valkey (with the settings above) and floci S3 (`docker-compose.yml`); `make test-unit` runs the in-memory `docref` and `test/unit/connectors` cases:

- `docref/s3content`: create and resolve; missing object → `ErrSourceMissing`; same-size overwrite → `ErrIntegrityViolation`; overwrite with a different size → `ErrIntegrityViolation` before the body is read; a 48 MiB object streamed without buffering; 24 concurrent resolutions; delete.
- `docref/valkeystore`: each hash has exactly the nine metadata fields and no content, under 1 KiB; create-only; a create whose reply is lost (proxy drops the connection after Valkey applied it) succeeds on the client's retry; concurrent creates of one ID; a user with exactly the runbook's ACL runs every operation; tenant isolation; TTL; Valkey restart recovering metadata from the AOF; **Valkey restart with documents still resolving from S3**; a non-durable server refused.
- `docref` (in memory): forged metadata pointing at another tenant's object, or at any key other than the ref's own `<prefix><tenant>/<uuid>`, is refused before S3; a 256 MiB document streams with < 8 MiB allocated; a failed metadata write leaves no object.
- `test/integration/multireplica` `TestMultiReplica_*`: replicas with their own Valkey and S3 clients — write on A/read on B, writer restart, rolling deployment, concurrent writes, delete and recreate, missing ref, tenant isolation; `TestDocRefs_UnresolvableSource_FailsBeforeAnySideEffect` (`test/unit/connectors`) — nothing is sent or uploaded from a missing or altered source.
