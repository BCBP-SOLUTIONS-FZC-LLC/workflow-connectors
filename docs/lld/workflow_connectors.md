# Automatic Connector Tasks & Connector Workers; Low-Level Design

> This is the in-repo copy of the LLD (`docs/lld/`), kept content-identical to the design repo's published copy. The two are intentionally allowed to differ only on embed-vs-link mechanics — never on content.

## 1. Overview & Scope

This document specifies a small, fixed, extensible catalogue of automatic workflow tasks; steps that fetch, check, or send something without a human acting on them; and how they're declared, compiled, run, and executed across `workflow-models`, `definition_service`, `execution_service`, and a new shared connector library and worker runtime (`workflow-connectors`, §6) that runs inside `execution_service` itself.

**Explicitly not in scope.** This is not a mechanism for arbitrary or dynamically-targeted business logic. Every connector type is a small, fixed, developer-built piece of code, known ahead of time and registered explicitly; never something a workflow author points at an arbitrary URL or database at authoring time. Real business logic; what a domain service does with its own data; continues to live entirely in that domain service, unchanged from today's model (`execution_service.md` §1.3/§8.1). This document only adds a way for a workflow to react automatically to a task instead of waiting on a person, using the exact same completion path a person already uses.

**Build order.** The pieces here have a real dependency order, not an arbitrary one:

1. `workflow-models`; the connector-task DSL shape, defined once, since both compiling and running services already depend on this module for every other DSL shape.
2. `definition_service`; stores and serves the authoring-side templates a workflow author sees (including every field a connector needs, credentials included — §6.2), kept in sync with `workflow-models`, and compiles against what `workflow-models` already defines.
3. `execution_service`; runs against what `workflow-models`/`definition_service` already define.
4. The shared connector library and worker runtime (`cmd/connector-worker`, inside `execution_service`'s own repo); the actual execution, decided last since it depends on nothing upstream of it changing.

**Field ownership across the boundary.** `workflow_models_lib.md` §2.3/§4.1 owns the concrete `StageDef.ConnectorType`/`StageDef.IOMapping` fields §3 below defines the shape of. `definition_service.md` §4.1.2/§4.1.3.3 and `execution_service.md` §2.4/§3.1/§4.3/§6.4 carry the small footprint changes §4/§5 below describe — neither service needed a structural change beyond that footprint; both were written placement-agnostic from the start, which is exactly what made resolving §6.1's placement question a documentation-only change with no ripple into either.

## 2. Architecture

```text
 author (definition_service templates, sourced from workflow-models —
 every field the connector needs, including credentials, §6.2)
        |
        v
 definition_service compiles (§4)
        |
        v
 execution_service creates the task exactly as it does today (§5)
        |
        v
 WorkflowTaskCreated fires (connector_type set; already exists for every task)
        |
        v
 cmd/server's existing /internal/events
 handler additionally pushes onto a
 Valkey Stream for connector-typed
 tasks (§6.5 step 0) — the only inbound
 listener stays cmd/server's; cmd/
 connector-worker has none (§6.1)
        |
        v
 cmd/connector-worker consumes via
 XREADGROUP/XACK, a pure Stream
 consumer (§6.5)
        |
        v
 dispatch to the registered Connector (§6.3),
 per-connector-type bounded worker pool
        |
        v
 Connector.Execute(ctx, input) — input already carries
 everything needed: author-supplied config, with any
 provider credential resolved from its OpenBao secret
 path (§6.2) for storage/send-email/document-extract/
 chat-notify, or a forwarded internal-auth context for
 rest-call/sql-query
        |
        v
   success                        failure / unregistered type
        |                                    |
        v                                    v
 execution_service's new           the new fail-signal endpoint (§6.5) —
 completion endpoint (§5.5) —      routes to the same FAILED/DEGRADED
 a sibling of the human path,      outcome a non-retryable Activity
 not a re-route through it         error already produces
        |                                    |
        v                                    v
 workflow continues, exactly       workflow fails, exactly as any
 as if a human had completed it    other non-retryable failure would
```

**Connector tasks are automation-only — there is no human fallback path.** If a connector type isn't registered, or a connector call ultimately fails (retries and the internal execution timeout both exhausted, §6.5), the task does not sit open waiting for a person. It fails the workflow through the interpreter's existing `FAILED`/`DEGRADED` machinery — a main/Sequential-path task produces `FAILED`, a task inside a `Parallel` branch contributes to `DEGRADED` — via a new signal `cmd/connector-worker` calls, the same outcome a non-retryable Activity error already produces today (§10 Decision #9).

## 3. DSL Shape (`workflow-models`)

A connector-typed stage is `enums.StageTypeConnector`, a new value alongside `prep`/`review`/`approve`/`send_task`/`receive_task` (`pkg/enums.StageType`), carrying:

- `StageDef.ConnectorType` — the connector type name (a plain string, not a fixed enum member; see §4.2 on why), parsed once at compile time from the BPMN `connector:` prefix.
- `StageDef.IOMapping` — a structured input/output mapping, reusing the exact same `IOMapping`/`IOVar` shape `CallPoolStep` already uses; not reinvented.

These two fields (`workflow_models_lib.md` §2.3/§4.1) are the single place either service looks to know what a valid connector-task node looks like. Neither service derives it independently.

## 4. Compiler Behavior (`definition_service`)

### 4.1 The element and its scoped exception

A `<bpmn:serviceTask>` carrying `<zeebe:taskDefinition type="connector:<name>"/>` compiles like a task-producing node. This is a narrow, explicit exception to `definition_service.md`'s Tier-3 permanent rejection of `bpmn:serviceTask` (§4.1.2): only a `serviceTask` whose `type` carries the `connector:` prefix is accepted. A `serviceTask` with any other `type`; or none; is still rejected exactly as today; arbitrary or custom service-task scripting remains permanently out of scope. This mirrors how `userTask`'s own type handling already has two branches (a registered type compiles fully; an unrecognized one still compiles, as a passthrough); `serviceTask` gets an analogous, but stricter, two-branch treatment: `connector:`-prefixed compiles, anything else is rejected, never silently passed through, since an arbitrary service task is exactly what Tier 3 exists to keep out.

Its input mapping reuses `ZeebeIOMapping`; the same type `callActivity` already populates from `<zeebe:ioMapping>`; so no new XML-parsing plumbing is needed, only a new element handler that reads the already-parsed structure into the DSL shape from §3.

### 4.2 Validation: lenient by design, matching an existing convention

The known connector-type list comes from a compile-time Go dependency — `workflow-connectors`' lightweight `pkg/registry` sub-package (§6.3), not the heavier `pkg/connectors` sub-package holding the actual runtime implementations — the same shape of dependency `workflow-models` already is for both services; no new database table, no new runtime call to keep something in sync.

An unrecognized connector type at publish time warns; it does not fail the publish. This matches the existing, already-shipped behavior for an unrecognized `StageDef.Type` (`definition_service.md` §4.1.3: "compiles as a passthrough stage... it is not rejected"). Treating a connector type the same way is a deliberate consistency choice, not an oversight; the alternative (hard-failing an unknown connector type) would make this the one place in the compiler where an unrecognized identifier behaves differently from everywhere else, for no benefit: the connector type named here might still be registered before the workflow is ever instantiated, so failing the publish over it would only block authors prematurely. Whether it's actually registered is a question runtime settles deliberately and visibly (§2, §6.5) — as a real workflow failure now, not a silent degrade — never something the compiler needs to guess at in advance. The same lenient treatment applies to an `endpointAlias`/`queryAlias` (§6.4.4/§6.4.5) that doesn't resolve at runtime — the compiler doesn't validate aliases at all, since only `cmd/connector-worker`'s own internal-API registry can.

### 4.3 Authoring experience (`definition_service`)

The Modeler-facing configuration form for a given connector type (what fields it asks for) is **not** resolved dynamically off BPMN/Zeebe XML namespaces. `definition_service` stores these forms and serves them to whoever authors a workflow, generated from `pkg/registry`'s own connector definitions (§6.3); one generated source of truth, not a hand-maintained list that can drift from what's actually implemented. This form is also where every field a connector needs — including, for `storage`/`send-email`/`document-extract`/`chat-notify`, the provider credential itself — gets collected (§6.2). This is a natural extension of `definition_service`'s own design-time authoring/compilation charter (`execution_service.md` §1.3), not a separate UI-facing service's job — the connector-type catalogue is fixed, code-generated metadata, not tenant-authored stored content (contrast with BE-for-UI's "custom BPMN module"/"reusable authoring component" library, which *is* new stored data and stays out of this service's schema, `execution_service.md` §1.3/Appendix A.2 #31).

## 5. Runtime Behavior (`execution_service`)

### 5.1 Task creation; no new mechanism

`CreateTaskActivity`'s dispatch table (`execution_service.md` §3.1) extends to cover connector-typed nodes, alongside `prep`/`review`/`approve`, unrecognized-`Type` passthrough, and the `call_pool` admin-stub task. No new Activity, no new retry-policy class; the same DB-write activity class, the same signal-wait mechanics as every other task type. This is the load-bearing design choice in this whole document: a connector-typed task is created exactly like a human task, and completed through exactly the same path, so the Temporal interpreter itself needs no new dispatch branch, no new Activity, and no new retry policy. Every connector-typed task dispatches automatically the instant it's created — no decision, no preference lookup, nothing gates it.

### 5.2 Data model

`workflow_task` (`execution_service.md` §4.3) gains a nullable `connector_type` column; not in `extras_json`; because `cmd/connector-worker` needs to filter and query tasks by connector type efficiently, the same reason `department_id` is already a real column rather than JSON-only.

### 5.3 No decision mechanism in v1

Every connector-typed task in the v1 catalogue is unconditionally automatic. No v1 connector type needs an auto/manual decision mechanism (a per-user default plus a task-level override) — that story only ever applies to a connector replacing a pre-existing human task, where a tenant's own staff might want the option to keep doing the check themselves. `llm-verify` is the one connector design that had that story, and it is not part of the v1 catalogue (§6.4.7, §10 Decision #3); with no v1 connector needing it, the decision mechanism has no consumer.

This is intentionally not rebuilt as unused scaffolding. If a future connector type ever has the same story, the mechanism (a `SupportsManualOverride`-shaped registry flag, gating a stored default plus a task-level override) is straightforward to reintroduce — but nothing in v1 needs it, so nothing speculative is carried forward.

"Automatic" means no human involvement at any point in a connector task's life, including on failure — there is no manual-override decision and no manual fallback (§2, §6.5 Decision #9). A future connector type that genuinely needs a human in the loop on failure would need its own design, not an implicit extension of this one.

### 5.4 Events

`WorkflowTaskCreated`'s payload (`execution_service.md` §6.4, `exec_eventschema/workflow_task_created.json`) gains an optional `connector_type` field. `cmd/connector-worker` (§6) is a new named consumer of this event specifically for connector-typed tasks, alongside the event's existing consumers (Notification, the LLM cache pre-warm, Dashboard).

### 5.5 Completion; a sibling endpoint, not the human path (see §10 Decision #16)

`cmd/connector-worker` (§6.5) calls a **new** `execution_service` endpoint once it has a result — `POST /internal/connector-tasks/:id/complete` (success) or `/:id/fail` (failure, §6.5 step 3) — not the human `/tasks/:id/complete` path. `execution_service`'s `TaskService.checkHumanActionable` rejects every human-facing task action (`Claim`/`Complete`/`Defer`/`Reassign`) on a connector-typed task, since one has no human assignee to act on. `execution_service` *does* distinguish who or what is calling it — deliberately, as a safety property.

The new endpoint is a **sibling** of the human path, not a re-route through it: both ultimately call the same `TemporalClient.SignalWorkflow` (`stage-transition`/`stage-fail`) that resolves against the interpreter's own pending-signal machinery (§2), so §6.1's "never touches the Temporal SDK directly" constraint on `cmd/connector-worker` still holds — the new endpoint is what touches the SDK, on `execution_service`'s own side, exactly as `TaskService`'s existing methods already do. A connector task has exactly one legitimate resolver (the worker that dispatched it), never a concurrent human racing it, so the endpoint reads the task's own current `record_version` itself rather than trusting a client-supplied one. Idempotent under `cmd/connector-worker`'s own Stream-redelivery-driven retries via a terminal-status check (the long tail) plus a short-TTL dedup key (the narrow race between "signal delivered" and "the resulting DB write commits," which the interpreter's pending-map resolution does not absorb on its own).

## 6. The Shared Connector Library & Worker Runtime

### 6.1 Worker Placement — decided

**Decided: Option B.** A new `cmd/connector-worker` binary lives inside `execution_service`'s own repo, sharing its CI/deploy pipeline with the existing `cmd/server`/`cmd/worker` — a distinct process, not folded into either. The workflow team owns and runs the connector runtime directly, as trusted platform infrastructure, rather than delegating it to a domain service or a separate standalone repo.

**Still true regardless:** a connector worker is not a Temporal Worker — `cmd/connector-worker` never registers against `execution_service`'s Temporal task queues or touches the Temporal SDK (`execution_service.md` §3.7/§4.6 are unrelated). Different node types needing different handling never meant different Temporal Workers per BPMN node type; it means one dispatch-table entry per connector type, inside this one process. `cmd/connector-worker` has **no inbound HTTP surface at all** — it consumes `WorkflowTaskCreated` from a Valkey Stream that `cmd/server`'s existing `/internal/events` handler pushes connector-typed events onto (§6.5 step 0), not by running a listener of its own; it still dispatches to a per-connector-type bounded worker pool (§6.5), and still completes (or now fails, §10 Decision #9) via `execution_service`'s existing signal paths (§5.5) — the event-driven dispatch mechanism (§10 Decisions #1/#2) is independent of where the process runs or how the event physically reaches it.

**Why Temporal workflow replay cannot itself cause a duplicate connector execution.** This design was compared directly against making the connector a real Temporal Activity (which would give native replay-safety and retry/backoff) and the comparison came out in favor of keeping the design as-is (§10 Decision #1 rationale) — because the replay concern the alternative would solve isn't actually a risk here. Temporal's replay guarantee covers only the workflow *function's* own execution: every `ExecuteActivity`/signal-wait/timer call is recorded in history, and replay reconstructs the workflow's in-memory state from that history without re-running anything already recorded as complete. `cmd/connector-worker`'s code runs entirely *outside* that boundary — it's a separate process reacting to an event, exactly like a human reacting to a task appearing in their inbox — so replay has no path to re-invoke it; replaying the workflow just means the workflow function's deterministic code reconstructs "I am waiting on a `stage-transition` signal for node X," it does not re-run or re-trigger anything external. The real, different risk — the same event being delivered to `cmd/connector-worker` twice, whether over SNS/SQS's at-least-once guarantee upstream or a redelivered, unacked Valkey Stream entry downstream — is closed by §6.5 step 0's dedup mechanism, not by anything Temporal-specific.

`workflow-connectors` (the Go module) still holds `pkg/registry` (imported by `definition_service` alone — both its compile-time check and its authoring-template generator, §4.3) and `pkg/connectors` (imported by `execution_service`'s `cmd/connector-worker` as an ordinary module dependency, same as `platform-events`) — no repo/module restructuring beyond this.

### 6.2 Configuration & Credentials — author-supplied, resolved by reference

**Every field a connector needs is supplied by whoever authors/administers the connector task in `definition_service`'s authoring form (§4.3).** Ordinary fields flow through the compiled task's `IOMapping`/`context_json` exactly like any other workflow variable — including `provider` (§6.4), an ordinary field like any other, never itself a secret. A provider credential — for `storage`/`send-email`/`document-extract`/`chat-notify` — does not: `definition_service` writes the raw value to **OpenBao** at the moment the author submits the form (the same mechanism `execution_service.md` §9.4 already uses for the platform's own infrastructure secrets, extended here to a new usage — a per-tenant, author-time-created secret rather than a static, ops-provisioned one) and puts only the resulting **OpenBao secret path** into the task's compiled `IOMapping` input. There is no runtime callback to a domain service and no platform-ambient credential lookup — the worker runs as trusted platform infrastructure (§6.1) and the UI collects every required field up front regardless.

**`cmd/connector-worker` resolves the path to the real value via OpenBao's KV-v2 read API, called only inside `Connector.Execute()`, in memory.** The raw credential is never written to `context_json`, never recorded in Temporal's workflow history, and never carried in the `WorkflowTaskCreated` event payload, or on the Valkey Stream `cmd/server` pushes connector-typed events onto (§6.5) — only the secret path is, in every one of those places. The authoring experience is unchanged from the author's point of view (they still type the credential into a form once); what changes is what happens to that value after submission.

**The worker reads only the job's own credential path.** `WriteCredential` stores a credential for tenant T, connector type C and field F at exactly `connectors/<T>/<C>/<F>`, and nowhere else. That path is therefore the only value a secret-reference field on a C task in tenant T can hold. `cmd/connector-worker` compares each secret-reference value against it before any OpenBao read. Any other value, including another tenant's path, fails the task with `error_class` `secret_ref_invalid`, and the connector does not run. The worker's OpenBao token can read every tenant's subtree, so this comparison is the tenant boundary at this hop.

**Rotation and revocation.** Rotating a credential is re-submitting the authoring form — `WriteCredential` overwrites the same OpenBao path, and KV-v2's own versioning keeps the history. Revoking one is a separate, explicit action: an admin-gated `DELETE /api/v1/connectors/credentials/:connector_type/:field_name` destroys every version at that path outright (OpenBao KV-v2's metadata-destroy endpoint, not the data-delete soft-delete, which stays recoverable) — a genuine revoke, for a compromised credential or a decommissioned connector task. TTL/expiry enforcement and notifying an in-flight workflow instance still holding a resolved value are both out of scope.

**"Document ref"** — used throughout §6.4's field tables (`storage.content`/`contentRef`, `document-extract.documentRef`, `send-email.attachments`) — is a plain opaque string, issued by the `storage` connector's own `fetch`/`upload` operations and consumed as-is by any other connector's document-ref-typed field. No separate Document Service exists or is proposed here; this is just enough shape to make those fields concrete. A connector receiving a document ref it didn't issue itself treats it as an opaque token to pass through, not something to parse.

**`rest-call`/`sql-query` are the exception — internal platform APIs only, never external ones.** Their `endpointAlias`/`queryAlias` (§6.4.4/§6.4.5) resolves, via a small config/registry `cmd/connector-worker` owns itself, to another microservice within this platform (e.g. Tender's own API, PMS's own API) — never an arbitrary external domain. Every call `cmd/connector-worker` makes through this path carries **two required pieces, both mandatory, neither optional**:

1. The platform's existing internal-service-to-service token (`execution_service.md` §5.7/§9.2's `x-internal-token`/`INTERNAL_API_TOKEN` — the same mechanism `execution_service`'s own `/internal/*` routes already require), proving the call originates from trusted platform infrastructure.
2. The acting user/tenant's forwarded `x-departments` header (`dept_uuid:role` pairs — see the cross-repo header-format note in `execution_service.md`/`definition_service.md`), so the target internal service can apply its own row-level/role-based authorization if it needs to.

No per-tenant *secret* is involved for these two connector types — but the auth itself is real, mandatory, and explicitly documented as such, never waved away as unnecessary.

**Net effect on domain services.** Tender/PMS supply nothing but ordinary workflow variables through the existing `IOMapping` mechanism now — the same way every other task type already receives its data. There is no callback contract, no credential ownership, no template ownership left on the domain-service side at all.

### 6.3 The Connector Interface

```text
Connector interface:
  Type() string                                        // the registered name, e.g. "storage"
  Execute(ctx, input) (output map[string]any, error)   // input carries everything: author-supplied
                                                        // config, with any credential already resolved
                                                        // from its OpenBao secret path (§6.2), or
                                                        // (rest-call/sql-query) ctx carries the forwarded
                                                        // internal-auth context. output's top-level keys
                                                        // are what IOMapping.Outputs.Source can address —
                                                        // no nested/dot-path access in v1 (§6.5)
```

`workflow-connectors` — a new, `platform-libs`-style Go module (alongside `platform-events`, `platform-gincommon`, `platform-pgcommon`), owned by the workflow team — splits into two packages so that a compile-time consumer that only needs the type list never pays for the runtime's dependencies:

- **`pkg/registry`** — lightweight: type names, display metadata, JSON-schema-shaped input/output descriptions. Zero heavy SDK dependencies. Imported by `definition_service`, for both §4.2's `UNKNOWN_CONNECTOR_TYPE` compile-time check and its authoring-template generator (§4.3) — the same service, one import.
- **`pkg/connectors`** — heavy: the actual `Execute()` implementations, pulling in the AWS SDK, the SendGrid client, DB drivers, etc. Imported only by `execution_service`'s `cmd/connector-worker` (§6.1).

A small generator tool introspects `pkg/registry`'s shapes to emit the element-template JSON `definition_service` serves (§4.3) — one generated source of truth, not a hand-maintained list that can drift from what `pkg/connectors` actually implements.

### 6.4 The Catalogue

Six v1 connector types, retry guidance stated per type below as final, ratified policy — not a recommended default. For each: the BPMN-author-supplied `IOMapping` inputs flow through the workflow's own `context_json`, unchanged mechanism; where a field is a provider credential, what actually travels is a secrets-store reference, not the raw value (§6.2). Output is written back via `execution_service`'s connector-task completion endpoint (§5.5, §6.5 step 3). Every design below is original, informed only by the general shape and full property set of Camunda's equivalent out-of-the-box connector (never its code, type identifiers, or template content — §7).

**Four of the six are designed as multi-provider (§10 Decision #18): `storage`, `send-email`, `document-extract`, `chat-notify`.** `rest-call`/`sql-query` have no `provider` field — they only ever target this platform's own internal services (§6.4.4/§6.4.5), where "provider" has no meaning. **A `provider` field ships only once real per-provider code exists for that type (§10 Decision #20)** — `storage` is the only one of the four that has it today; `send-email`/`document-extract`/`chat-notify` still run a single generic mock client each, so their tables below describe the eventual design, not the current schema. `provider` is always required, never defaulted (§10 Decision #20) — a task must name one explicitly. `cmd/connector-worker` holds one client per `(connector type, provider)` pair rather than one per type; `Execute()` (§6.3) looks up the pair the `provider` field names. Every provider implements the same `*ProviderClient` interface already defined for that connector type — the interface itself didn't need to change, only the number of implementations behind it.

#### 6.4.1 `storage` (fetch / upload / delete)

Camunda reference shape: S3/Azure Blob/GCS/Box connectors. Four providers (§10 Decision #18): `aws-s3`, `azure-blob`, `gcp-gcs`, `google-drive`.

`content`/`contentRef` below use the "document ref" shape defined in §6.2 — an opaque string, issued by this connector's own `fetch`/`upload` output for other connectors to consume as-is. Operational fields are shared across every provider; only the credential fields and the name of the container-like field differ.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`aws-s3`\|`azure-blob`\|`gcp-gcs`\|`google-drive`) | Required, no default (§10 Decision #20). |
| IOMapping input | `operation` | enum (`fetch`\|`upload`\|`delete`) | |
| IOMapping input | `bucket` | string | The container-like field — a bucket (`aws-s3`/`gcp-gcs`), a container (`azure-blob`), or a folder ID (`google-drive`). One field name across all four; the value's meaning is provider-specific. |
| IOMapping input | `key` | string (dynamic, e.g. sourced from a workflow variable) | The document path/identifier. |
| IOMapping input | `content` | document ref | Only for `upload`. |
| IOMapping input | `createDocument` | bool | Only for `fetch` — `true` creates a document reference, `false` returns content inline (same distinction Camunda's own S3 connector draws). |
| Output | `contentRef` or `content` | document ref / inline | `contentRef` for large objects, inline `content` for small ones. |
| Output | `contentType`, `sizeBytes`, `fetchedAt` | string / int / timestamp | |

Provider-specific credential fields (all secrets-store references, §6.2). Field names are globally unique across all four providers, not just within one provider's own row — this is load-bearing, not a style choice; see §9 phase 2's note on why:

| Provider | Credential fields |
| --- | --- |
| `aws-s3` | `accessKey`, `secretKey`, `region` |
| `azure-blob` | `azureAccountName`, `azureAccountKey` |
| `gcp-gcs` | `gcpServiceAccountKey`, `projectId` |
| `google-drive` | `driveServiceAccountKey` (domain-wide delegation, or the target folder shared directly with the service account) |

Retry guidance: safe to retry (read for `fetch`, idempotent overwrite for `upload` when `key` is caller-supplied and stable) for `aws-s3`/`azure-blob`/`gcp-gcs`. `google-drive` has no path-keyed upsert the way the other three do — its client implements `upload` as search-by-name-in-folder, then update-if-found/create-otherwise, which is what actually keeps this type-level `RetryPolicySafe` honest for that provider too, not a separate provider-level retry policy.

#### 6.4.2 `send-email`

Camunda reference shape: SendGrid connector. Four providers (§10 Decision #18): `sendgrid`, `aws-ses`, `microsoft-365`, `google-workspace`.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`sendgrid`\|`aws-ses`\|`microsoft-365`\|`google-workspace`) | Required, no default (§10 Decision #20). |
| IOMapping input | `senderName`, `senderEmail` | string | |
| IOMapping input | `receiverName`, `receiverEmail` | string | Workflow-instance data (e.g. an applicant's address) — sourced from workflow variables. |
| IOMapping input | `subject` | string | |
| IOMapping input | `contentType` | enum (`text/plain`\|`text/html`) | |
| IOMapping input | `body` | string | Required for `microsoft-365`/`google-workspace` regardless of `templateId` (§10 Decision #22) — those two have no template mechanism to fall back on. |
| IOMapping input | `attachments` | list of document refs | Resolved against the same `docRefStore` `storage` writes to (§10 Decision #22) — a doc ref minted by a `storage` fetch is attachable here. No per-attachment filename field exists; one is synthesized from the resolved content type. |
| IOMapping input | `templateId` | string, optional | Template variant — if set, `subject`/`body` are ignored in favor of the provider's own template rendering. `microsoft-365`/`google-workspace` have no equivalent server-side template mechanism; ignored for those two. |
| Output | `sent` | bool | |
| Output | `messageId`, `sentAt` | string / timestamp | `microsoft-365`'s `sendMail` returns no message ID synchronously (202 Accepted, empty body) — `messageId` is empty for that provider. |

Provider-specific credential fields:

| Provider | Credential fields |
| --- | --- |
| `sendgrid` | `apiKey` (secrets-store reference) |
| `aws-ses` | `accessKey`, `secretKey` (secrets-store references), `region` (plain config value) |
| `microsoft-365` | `tenantId`, `clientId` (plain config values — identifiers, not credentials), `clientSecret` (secrets-store reference; Graph API app credentials, `Mail.Send` application permission) |
| `google-workspace` | `serviceAccountKey` (secrets-store reference; domain-wide delegation, `gmail.send` scope) |

Retry guidance: **not** safely retryable (duplicate-send risk) — single attempt, no automatic retry, for every provider.

#### 6.4.3 `document-extract` (OCR / structured extraction)

Camunda reference shape: Amazon Textract connector. Four providers (§10 Decision #18): `aws-textract`, `azure-document-intelligence`, `gcp-document-ai`, `abbyy-vantage` — the four engines Camunda's own Intelligent Document Processing feature itself supports, confirmed directly against Camunda's docs rather than assumed.

**Real-time only in v1, for every provider.** Textract's own async mode (submit a job, then poll or get an SNS callback on completion) doesn't fit `Connector.Execute`'s single synchronous call/return (§6.3), and an SNS callback is the same inbound, correlated-response shape §6.4.7 already defers. `executionType`, `outputS3Bucket`, and the notification-channel fields are dropped from v1 entirely rather than carried as dead properties nothing consumes — async/polling revisits once §6.4.7's inbound mechanism actually exists (§10 Decision #11).

The output shape below is normalized across all four providers — each has its own native response format, but `Connector.Execute` maps every provider's result onto this one shape so a workflow author's `IOMapping.Outputs` doesn't need to branch on which provider ran.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`aws-textract`\|`azure-document-intelligence`\|`gcp-document-ai`\|`abbyy-vantage`) | Required, no default (§10 Decision #20) — design only, no real implementation yet. |
| IOMapping input | `documentLocation` | enum (`s3`\|`inline`) | `s3` is meaningful only for `aws-textract`; every other provider always receives the document inline via `documentRef`, regardless of this field. |
| IOMapping input | `documentBucket`, `documentName`, `documentVersion` | string | Only if `documentLocation = s3` (`aws-textract` only). |
| IOMapping input | `documentRef` | document ref | Only if `documentLocation = inline`. |
| IOMapping input | `analyzeForm`, `analyzeSignatures`, `analyzeLayout`, `analyzeQueries` | bool | Same four analyze-flags across all providers; a provider without a native equivalent for one flag (e.g. `abbyy-vantage` has no signature-specific mode) folds it into its closest existing extraction pass rather than erroring. |
| IOMapping input | `query` | string | Only if `analyzeQueries`. |
| IOMapping input | `clientRequestToken`, `jobTag`, `kmsKeyId` | string, all optional | `aws-textract`-specific; ignored by the other three providers. |
| Output | `fields` | map | For `analyzeForm`. |
| Output | `rawText` | string | Full-text extraction. |
| Output | `signaturesDetected` | list | For `analyzeSignatures`. |
| Output | `answers` | list | For `analyzeQueries`. |
| Output | `confidence` | map | Keyed by field name — one confidence score per extracted field, not a single scalar. |

Provider-specific credential fields (all secrets-store references, §6.2):

| Provider | Credential fields |
| --- | --- |
| `aws-textract` | `accessKey`, `secretKey`, `region` |
| `azure-document-intelligence` | `endpoint`, `apiKey` |
| `gcp-document-ai` | `serviceAccountKey`, `projectId`, `location`, `processorId` |
| `abbyy-vantage` | `endpoint`, `apiKey` (Vantage is cross-cloud — no cloud IAM credential shape) |

Retry guidance: safe to retry (read-only call against the source document) for every provider — unambiguous now that there's no async-mode job-submission step to worry about double-triggering.

#### 6.4.4 `rest-call`

Camunda reference shape: the generic REST connector — scoped down significantly, see below.

**Internal platform APIs only, alias-based, never a raw URL** (§10 Decision #4) — the workflow never supplies a real URL, and the target is never an external domain. This closes the SSRF/unknown-idempotency risk flagged against a generic "call whatever the plan names" mechanism in this feature's own originating investigation, while still allowing a general-purpose internal-API connector to exist.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `endpointAlias` | string | A symbolic name resolved against `cmd/connector-worker`'s own internal-service registry — a static config file it loads at startup, redeployed to add/change an alias (§10 Decision #12) — never a raw URL. |
| IOMapping input | `pathParams`, `queryParams`, `body` | map / any | |
| Output | `status` | int | |
| Output | `headers` | map | |
| Output | `body` | any | |

Auth (§6.2): every call carries the platform's `x-internal-token` and the acting user/tenant's forwarded `x-departments` header — both mandatory, no per-call configuration needed.

Retry guidance: retryable only if the resolved internal endpoint's method is idempotent (e.g. a `GET` lookup) — a `POST`/non-idempotent endpoint gets no automatic retry.

#### 6.4.5 `sql-query`

Camunda reference shape: the generic SQL connector — scoped down significantly, see below.

**Internal platform databases only, alias-based, read-only, never raw SQL** — same rationale as `rest-call`: a raw arbitrary query would reopen the "never an arbitrary/dynamically-targeted call" boundary §1 establishes. v1 scopes this connector to pre-registered, read-only named queries against this platform's own databases only; write queries and external databases are both out of scope for v1.

**Data-access model, confirmed (see §10 Decision #17): an internal HTTP proxy, structurally identical to `rest-call`, never a direct database connection.** `queryAlias` resolves to the *owning service's own* internal query-execution endpoint plus a `queryId` that service runs on our behalf against its own pre-registered, read-only statement — `cmd/connector-worker` never holds credentials to, or opens a connection against, any platform database directly, and never sees or constructs SQL text. This reading is what makes this section's own "both required, both mandatory" auth statement below literally true: `x-internal-token`/`x-departments` are real HTTP headers on that request, which have no meaning on a raw database connection.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `queryAlias` | string | A pre-registered named read-only query, resolved against `cmd/connector-worker`'s own internal registry — the same static config file `rest-call`'s `endpointAlias` uses (§10 Decision #12) — to the owning service's own query-execution endpoint plus a `queryId`, never raw SQL and never a direct DB connection (Decision #17). |
| IOMapping input | `params` | list | Bound as query parameters by the owning service itself, never string-interpolated by this connector. |
| Output | `resultSet` | list of rows | Row count bounded (a fixed cap, e.g. a few hundred rows) to keep `context_json` small — a large result set is a design smell for a workflow variable, not this connector's job to paginate. |

Auth (§6.2): same as `rest-call` — `x-internal-token` plus forwarded `x-departments`, both mandatory.

Retry guidance: retryable, because scoping to read-only aliases makes every v1 query naturally idempotent.

#### 6.4.6 `chat-notify` (Slack/Teams)

Camunda reference shape: the Slack connector. Two providers (§10 Decision #18): `slack`, `teams` — both named explicitly, not "Slack or Teams, pick one."

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`slack`\|`teams`) | Required, no default (§10 Decision #20) — design only, no real implementation yet. |
| IOMapping input | `method` | enum (`create-channel`\|`invite-to-channel`\|`post-message`) | Same three methods for both providers — Teams' Graph API has a direct equivalent for each (create channel, add member, post message). |
| IOMapping input | `channelName` | string | Only for `create-channel`. |
| IOMapping input | `visibility` | enum (`public`\|`private`) | Only for `create-channel`. |
| IOMapping input | `inviteBy`, `channelNameOrId`, `users` | string / list | Only for `invite-to-channel`. |
| IOMapping input | `channelOrUser`, `thread`, `messageType`, `message` or `messageBlock`, `attachments` | string / list | Only for `post-message`. |
| Output | `sent` | bool | |
| Output | `messageId` | string | |

Provider-specific credential fields (all secrets-store references, §6.2):

| Provider | Credential fields |
| --- | --- |
| `slack` | `authToken` (bot token, `channels:manage`/`chat:write` scopes) |
| `teams` | `tenantId`, `clientId`, `clientSecret` (Graph API app credentials, `Channel.Create`/`ChannelMessage.Send` scopes) |

Retry guidance: **not** safely retryable (duplicate-message risk), same as `send-email` — single attempt, for both providers.

#### 6.4.7 Deferred from v1

**Inbound webhook** (Camunda reference: HTTP Webhook connector) was explicitly deferred — a structurally different, inbound (not outbound) connector shape: an external system starts or advances a task by calling in, rather than a connector calling out. Revisit once there's a concrete use case; its design would need its own correlation/auth model (Camunda's own shape uses HMAC/API-key/JWT auth plus a FEEL correlation-key expression, per this feature's own research trail), not a straightforward extension of the outbound catalogue above.

**`llm-verify`** (AI document verification) was designed, then removed from the v1 catalogue entirely (§10 Decision #3) — see §5.3 for what its removal took with it (the auto/manual decision mechanism).

### 6.5 Runtime Loop

Placement-agnostic in principle (§6.1 settled on one specific process, `cmd/connector-worker`, but nothing below depended on that):

0. **Delivery and dedup — a two-hop concern** (event redelivery, not workflow replay; §10 Decision #10). Upstream, `WorkflowTaskCreated` arrives at `cmd/server`'s existing `/internal/events` handler over SNS/SQS (at-least-once) exactly as today — that hop dedups via the existing `processed_event(event_id, consumer, event_type, processed_at)` table and pattern (`execution_service.md` §6.3) before anything is pushed onward, unchanged. For connector-typed tasks specifically, that handler additionally pushes the event onto a **Valkey Stream** — a Stream, not a plain list, because a list's `BRPOP` gives no redelivery safety net if a consumer crashes mid-item, while a Stream's consumer-group semantics do. `cmd/connector-worker` reads via `XREADGROUP` and only `XACK`s an entry once step 3 below has actually completed (success or fail-signal) — an un-acked entry (a crashed or slow consumer) becomes eligible for another consumer to reclaim (`XCLAIM`/`XAUTOCLAIM`), the Stream's own redelivery mechanism; no `processed_event` row is needed at this second hop, since the Stream's own ack-tracking already gives at-least-once-plus-idempotent-handler semantics for it. (Separately: Temporal workflow *replay* cannot itself cause a duplicate connector execution in this design at all, regardless of either hop's dedup — see §6.1's closing note.)

   **A task's connector runs once, however often its entry is delivered.** Redelivery covers a failed callback as well as a dead consumer, and a connector's side effect (an email, a non-idempotent `rest-call`) must not repeat with it. For each entry the worker, in order:

   - dead-letters an entry delivered more than `CONNECTOR_STREAM_MAX_DELIVERIES` times (default 10): it copies the entry, with its reason, onto the stream `CONNECTOR_STREAM_DEAD_LETTER_KEY` and acks it. The task stays open for an operator to force-forward it or re-drive the entry. A re-drive within the outcome's 24 hours re-sends the recorded outcome; a later one runs the connector again. `connector_dispatch_dead_lettered_total` counts these.
   - resolves the tenant's automation actor. Without one, the entry stays pending and the connector does not run.
   - re-sends the recorded outcome, if the task has one, and acks. The connector does not run again.
   - takes an execution lease on the task once a pool slot is free, lasting the connector type's timeout plus 10 seconds. While another consumer holds the lease, or the cache cannot be reached, the entry stays pending.
   - runs the connector, records its outcome (the output, or the error class) for 24 hours, then calls back and acks.

   **Only a dead consumer's entries are reclaimed.** An entry's idle time starts at delivery, and would otherwise keep running while the entry waits for a pool slot and while it runs. The worker touches every entry it has read and not yet finished with (`XCLAIM … JUSTID` to itself, which resets the idle time without counting a delivery) three times per `CONNECTOR_STREAM_CLAIM_MIN_IDLE`. An entry becomes reclaimable only once its consumer stops touching it: the consumer died, left the entry pending on purpose, or has a run that outlived its lease and is treated as hung. A consumer that is handed a second copy of an entry it already holds leaves it to the first. A reclaimed entry whose delivery count cannot be read is still dispatched, uncapped for that delivery.

   **A run that has started finishes.** Shutdown stops reading entries. A run already under way keeps its own context, records its outcome and reports it. The process waits for the longest connector timeout plus the callback timeout plus 10 seconds, and the pod's termination grace period exceeds that wait.

   Reclaim runs every `CONNECTOR_STREAM_CLAIM_MIN_IDLE`, so redeliveries of an abandoned entry land up to twice that apart. The outcome's 24 hours exceed twice the cap times that wait, and the worker refuses to start otherwise. The completion endpoints treat a callback for a resolved task as a no-op (§5.5), which is what makes a re-send safe. One window remains: when the side effect has happened but the outcome was not recorded (the process was killed, or Valkey was unreachable) and the entry was not acked, the connector runs again once its lease expires. Closing it needs the external system's own idempotency key.

1. `WorkflowTaskCreated` arrives with `connector_type` set. If the handler doesn't recognize that type, it calls the fail-signal path (step 3) immediately instead of ignoring the event — an unregistered connector type is a real workflow failure now, not a silent no-op (§2, §10 Decision #9).
2. Dispatch to that connector type's own bounded worker pool (sized to the external system's own rate limits, not a single shared pool — a storage fetch and an email send have very different latency/rate-limit profiles), then call `Connector.Execute(ctx, input)` under a per-connector-type internal execution timeout — a ceiling on total time including retries/backoff. `input` already carries everything needed (§6.2): author-supplied config for `storage`/`send-email`/`document-extract`/`chat-notify`, with any provider credential resolved from its OpenBao secret path just before the call, or `endpointAlias`/`queryAlias` (resolved against the static alias-registry config file, §10 Decision #12) plus a forwarded internal-auth context for `rest-call`/`sql-query`.
3. On success, call `execution_service`'s new `POST /internal/connector-tasks/:id/complete` endpoint (§5.5, §10 Decision #16) with `Connector.Execute`'s raw `output` map, unmodified. The rename step — `IOMapping.Outputs` mapping each of `output`'s top-level keys (§6.3) to a workflow variable, no nested/dot-path access in v1 (§10 Decision #13) — happens on `execution_service`'s own side, inside that endpoint, not in `cmd/connector-worker`: `CreateTaskActivity` already persisted the compiled stage's `IOMapping.Outputs` alongside its resolved inputs at task-creation time, so `execution_service` is where that mapping already lives, and the connector-worker→execution_service contract stays "send whatever `Execute()` returned, verbatim." On failure, the connector's own retry/backoff (§6.4's per-type policy) runs inside this same handler; once exhausted, **or once this step's own internal execution timeout elapses first**, the handler calls `POST /internal/connector-tasks/:id/fail` instead of leaving the task open — the task never sits open waiting for anyone (§10 Decision #9). Both endpoints route through the interpreter's existing `stage-transition`/`stage-fail` signal resolution and its `FAILED`/`DEGRADED` machinery, the same outcome a non-retryable Activity error already produces on the main/Sequential path or inside a `Parallel` branch, respectively — no new interpreter state, only a new way to reach the existing ones. The task's `DueDate`/`FollowUpDate` SLA timer becomes functionally moot for a connector task under this model: with no human wait ever left open, it simply never fires before the task resolves one way or the other.

## 7. External Systems & Licensing

The v1 catalogue (§6.4 — six connector types) calls external services (a storage provider, an email provider, an OCR provider, a chat provider) directly from `cmd/connector-worker` (§6.1); `rest-call`/`sql-query` call only this platform's own internal APIs/databases (§6.2), never an external domain.

**Camunda's own connectors and engine are not reused, and this is a licensing decision, not a preference.** Checked directly against Camunda's published licensing terms:

- The core Zeebe/Camunda 8 engine's source is under the Camunda License v1 (source-available, not open source); its compiled runtime requires purchasing the Enterprise Edition for production use. This was already moot here since this platform runs its own Temporal-based engine, not real Zeebe; it only confirms that door was never open.
- Camunda's out-of-the-box connectors (S3, SendGrid, Textract, Slack, and every other one in its 40+ catalogue) are the same story one level down: only the Connector SDK, the plain REST connector, the Connector Runtime Docker image, and the Connectors Bundle Docker image are Apache 2.0. Every other out-of-the-box connector's source is under that same Camunda License v1, gated the same non-production/Enterprise way when compiled and distributed.

Consequence: this design does not adopt Camunda's actual connector type identifiers, nor copies their element-template JSON files. What is used: the element-template JSON *format* itself (a documented, open mechanism Camunda Modeler reads, not Camunda's proprietary content) and the full field/property shape of what each connector type in §6.4 needs; informed by public documentation, not copied from licensed source. The `connector:` type-naming convention (§4.1) and every template this design produces are original.

## 8. Security & Data Handling

- **Provider credentials for `storage`/`send-email`/`document-extract`/`chat-notify` never reach `context_json`, Temporal's workflow history, the `WorkflowTaskCreated` event payload, or the Valkey Stream that payload is additionally pushed onto for connector-typed tasks (§6.5).** `definition_service` still collects the raw value once, at author time, but writes it to OpenBao and passes only the resulting secret path through `IOMapping`/`context_json` (§6.2); `cmd/connector-worker` resolves that path to the real value in memory, only inside `Connector.Execute()`. This is mandated design, not a UI-side convention.
- **A task reads only its own tenant's credentials.** The worker refuses any secret reference other than `connectors/<task tenant>/<connector type>/<field>` before reading it (§6.2), because its OpenBao token is not scoped to a tenant.
- **`rest-call`/`sql-query` calls always carry both required auth pieces** — the platform's `x-internal-token` and the acting user/tenant's forwarded `x-departments` header (comma-separated `dept_uuid:role` pairs, e.g. `018e1f2a-...:reviewer`) — never one without the other (§6.2). No per-tenant secret is involved for these two connector types, but the auth itself is mandatory and explicit.
- **No v1 connector automatically forwards a received document to a third-party service with no person in the loop.** `llm-verify` — the connector design that would have done exactly that, forwarding to an external LLM provider — is not part of the v1 catalogue (§6.4.7, §10 Decision #3); nothing in v1 raises that specific trust-boundary question.

## 9. Follow-Ups (Non-Blocking)

None of the items below is an open design question on this side — each is a narrower, operational item tracked separately, and none blocks or contradicts anything in §1–§8.

- **`definition_service`'s alias-discovery UX for `rest-call`/`sql-query`.** §6.4.4/§6.4.5's `endpointAlias`/`queryAlias` are typed as free-text strings against the alias registry (now owned by `definition_service` — §10 Decision #21); an author currently has no way to see which aliases actually exist while filling in the authoring form. `GET /internal/connector-aliases` (Decision #21) makes this addressable but no UI consumes it yet. The compiler already degrades a dangling alias the same lenient way as any other unrecognized identifier (§4.2), so this doesn't block v1 — but a blind-typed, security-load-bearing field is a real authoring-UX gap worth closing.
- **Credential orphan cleanup and TTL.** *Rotation* needs no mechanism — OpenBao KV-v2 versions on overwrite, so re-submitting the authoring form rotates the value, and the read side always resolves the latest version. *Revocation* is explicit and admin-gated (§6.2). What is missing is a sweep: nothing deletes the secret at a path whose owning connector task is removed or whose workflow version is retired, so it is leaked until someone revokes it by hand. Credentials are never rowed in Postgres — there is no `created_at`/`rotated_at`/`expires_at` anywhere to drive a sweep from — so automatic cleanup would have to hook the archive path directly into revoke. TTL/expiry is deliberately undesigned. Doesn't block the OpenBao architecture in §6.2.
- **`rest-call`'s alias `BaseURL` is bounded by an internal-host allowlist, enforced at alias-write time** (`definition_service.md` §10.15). "Never a raw URL, always an internal service" (§6.4.4) is a rule the write path enforces rather than a convention asserted in comments: a write is rejected unless the host is on `CONNECTOR_ALIAS_ALLOWED_HOSTS` — exact hosts, or a leading-dot suffix rule — and unless the scheme is `http`/`https` with no embedded userinfo. It fails closed, so an unconfigured deployment rejects every alias write rather than accepting any host. Enforcement is on that write path and not in `restcall.go`: the allowlist is deployment configuration and this library takes none.
- **`sql-query`'s data-access model needs a fuller review before any more is built on it.** Deliberately not scoped or designed further right now.
- **Real SDK implementation for §6.4's 14 (4 types × 4 providers, minus chat-notify's 2) provider clients.** §10 Decision #18 designed the shape. **`storage` and `send-email` are both done as of 2026-08-25** (§10 Decision #19/#20 for `storage`, #22 for `send-email`) — 8 of the 14 provider clients are real in `workflow-connectors`. **Neither is yet wired into `cmd/connector-worker/deps.go`** — blocked on `workflow-connectors` actually being pushed (nothing is pushed anywhere yet) and `execution_service`'s `go.mod`/vendored copy being bumped past the pre-`storage` commit it's still pinned to; a `TODO` in `deps.go` names exactly what to wire once unblocked. The remaining 2 types are still unstarted, running against their in-memory mocks — for each, the `provider` registry field ships *with* its real implementation, not ahead of it — advertising a provider choice with no code behind it is misleading (§10 Decision #20):
  1. **`document-extract`'s 4 providers** (`aws-textract`/`azure-document-intelligence`/`gcp-document-ai`/`abbyy-vantage`) — `storage`/`send-email`'s per-call-construction-with-cache pattern is the template; 4 independently-shippable slices.
  2. **`chat-notify`'s 2 providers** (`slack`/`teams`).
  3. **Hardening** — a shared error taxonomy (transient vs. permanent, mapped from each provider SDK's own errors) so Decision #6's per-type `Retry` policy stays meaningful once multiple providers sit behind one type; LLD rev bump + CHANGELOG.

  Each of these ships independently — a type/provider pair can go out without waiting on the rest.

## 10. Design Decision Log

| # | Date | Decision | Rationale |
| --- | --- | --- | --- |
| 1 | 2026-08-07 | A connector's result reaches the workflow via the *existing* task-completion path (an event-driven callback), not a new Temporal Activity dispatched to a dedicated task queue. | Needs zero changes to the Temporal interpreter for the success path — completion is just another signal-wait resolution. `cmd/connector-worker`'s code runs entirely outside the workflow function's own Temporal replay boundary, so replay cannot re-invoke it — the real risk is event redelivery (SNS/SQS upstream, a Valkey Stream downstream, §10 Decision #10), closed by §6.5 step 0's two-hop dedup, a different, already-solved problem on this platform. Failure detection itself is a separate, later decision — §10 Decision #9. |
| 2 | 2026-08-12 | Worker placement: a new `cmd/connector-worker` binary lives inside `execution_service`'s own repo, sharing its CI/deploy pipeline, a distinct process from `cmd/server`/`cmd/worker`. It is not a Temporal Worker — it never registers against `execution_service`'s Temporal task queues or touches the Temporal SDK. | The workflow team owns and runs the connector runtime as trusted platform infrastructure rather than delegating it to a domain service or a separate standalone repo. This is what makes §6.2's credential design safe without a runtime callback to anywhere else. |
| 3 | 2026-08-12 | v1 catalogue is six connector types: `storage`, `send-email`, `document-extract`, `rest-call`, `sql-query`, `chat-notify`. Every one is unconditionally automatic — no manual-override mechanism exists in v1. Inbound webhook is deferred (a structurally different, inbound-not-outbound shape). | `llm-verify` was designed, then removed — it was the only connector type that ever needed a manual-override decision (it replaced a pre-existing human check); with it gone, no v1 connector has that story, so no decision mechanism is carried forward as unused scaffolding. |
| 4 | 2026-08-12 | `rest-call`/`sql-query` are alias-based and internal-platform-only — a workflow only ever supplies a pre-registered symbolic name (`endpointAlias`/`queryAlias`), never a raw URL or SQL string, and the resolved target is always another service/database on this platform, never an external domain. Every call carries both the platform's `x-internal-token` and the caller's forwarded `x-departments` header (`dept_uuid:role` pairs), both mandatory. | Without aliasing, a generic REST/SQL connector reopens exactly the risk this feature is scoped to avoid — unbounded-target SSRF exposure and unknown retry-safety. No per-tenant secret is needed for either type, but the auth itself is real and mandatory, not waved away. |
| 5 | 2026-08-13 | Provider credentials for `storage`/`send-email`/`document-extract`/`chat-notify` travel as an **OpenBao secret path** (KV v2), never a raw value. `definition_service` writes the raw credential to OpenBao at author time; `IOMapping`/`context_json` carries only the path; `cmd/connector-worker` resolves it to the real value via OpenBao's KV-v2 read API, in memory, only inside `Connector.Execute()`. | Letting the raw credential flow through `context_json` like an ordinary workflow variable would be under-protected relative to how this platform already treats its own comparable secrets (execution_service.md §9.4), and would expose it more widely besides (workflow history, the task-created event payload). Reusing the platform's existing secrets-management mechanism (OpenBao), extended to per-tenant author-time secrets, closes that gap without inventing new infrastructure. |
| 15 | 2026-08-13 | Connector-authoring templates (§4.3) and credential custody (§6.2) are owned by `definition_service`, not BE-for-UI. | Fixed, code-generated catalogue metadata (from `pkg/registry`) with no tenant-stored content of its own is a better fit for `definition_service`, which already owns design-time authoring/compilation and already imports the same registry package (`definition_service.md` §10.14/§10.15) — not BE-for-UI, whose own database is reserved for genuinely new stored entities like its "custom BPMN module"/"reusable authoring component" library (`execution_service.md` §1.3/Appendix A.2 #31), a different kind of responsibility this document's connector catalogue never needed. |
| 6 | 2026-08-12 | Per-connector retry/idempotency policy (§6.4) is ratified as final: safe to retry — `storage`, `document-extract`, `sql-query`; not safely retryable — `send-email`, `chat-notify`; retryable only if the resolved method is idempotent — `rest-call`. | Fetching a document or running a read-only query is naturally safe to retry; sending an email or chat message is not (double-send risk). Stating this as policy rather than a recommendation gives `cmd/connector-worker`'s per-type retry/backoff a single source of truth. |
| 7 | 2026-08-12 | `cmd/connector-worker` bounds each connector call with a per-connector-type internal execution timeout (covering total time including retries/backoff). On timeout or retry exhaustion, the handler calls the fail-signal path (§10 Decision #9) instead of leaving the task open — no interpreter, DSL, or schema change beyond the fail-signal itself. | A connector task's `DueDate`/`FollowUpDate` SLA timer becomes functionally moot under this model — with no human wait ever left open, it never fires before the task resolves one way or the other. `DEGRADED` stays scoped to `Parallel`-branch failure generally, now including a connector task's own fail-signal if it happens to be inside one. |
| 8 | 2026-08-07 | Camunda's own connector type identifiers, element templates, and runtime are not reused. | Camunda's actual out-of-the-box connectors and engine require a paid Enterprise license for commercial/production use (§7), confirmed directly against Camunda's published licensing terms. The open template *format* and full field/property shape are used as reference; the specific licensed content is not. |
| 9 | 2026-08-12 | Connector tasks are fully automation-only: neither an unregistered connector type nor an exhausted/failed connector call falls back to a human. Both now route through a new signal `cmd/connector-worker` calls, which the interpreter resolves exactly like a non-retryable Activity error already does today — `FAILED` on the main/Sequential path, contributing to `DEGRADED` inside a `Parallel` branch. | There is no existing "fail this task" signal today — `FAILED`/`DEGRADED` have only ever originated from an Activity error raised inside the workflow function itself — so this is new interpreter surface, not a documentation change; flagged as the highest-risk item in this design. |
| 10 | 2026-08-12 | `WorkflowTaskCreated` reaches `cmd/connector-worker` via a Valkey Stream, not a listener of its own. `cmd/server`'s existing `/internal/events` handler additionally pushes connector-typed events onto the Stream; `cmd/connector-worker` is a pure `XREADGROUP`/`XACK` consumer with no inbound HTTP surface, no ingress `NetworkPolicy` grant, and no `INTERNAL_API_TOKEN` of its own. | Considered against giving `cmd/connector-worker` its own inbound listener (matching §6.1's "distinct process" framing more literally) — rejected for a materially larger attack surface with no offsetting benefit. Valkey is already deployed and used by `cmd/server` today (the compiled-plan cache, the idempotency store); this is a new usage of existing infrastructure, not a new platform-wide dependency. A Stream, not a plain list, is used specifically for its consumer-group ack/redelivery semantics — a list's `BRPOP` has none. |
| 11 | 2026-08-12 | `document-extract` is scoped to real-time execution only in v1 — `executionType`, `outputS3Bucket`, and the notification-channel fields are dropped from the catalogue entirely. | Textract's own async mode (submit-then-poll-or-notify) doesn't fit `Connector.Execute`'s single synchronous call/return, and an SNS callback is the same inbound, correlated shape §6.4.7 already defers — carrying those fields without a design for consuming them would be dead scaffolding, not forward-compatibility. |
| 12 | 2026-08-12 | `rest-call`'s `endpointAlias` and `sql-query`'s `queryAlias` resolve against a static config file `cmd/connector-worker` loads at startup — no new database table, no admin CRUD API. | Simplest mechanism that still satisfies the alias-based SSRF-prevention story (Decision #4); a DB-table alternative would add real scope (schema, endpoints, authz for who can register an alias) for a registry six connector types don't yet need to change at runtime. |
| 13 | 2026-08-12 | `Connector.Execute` returns `map[string]any`; `IOMapping.Outputs`' `Source` addresses only that map's top-level keys in v1 — no dot-path/nested access. | The alternative (nested path access, e.g. into `document-extract`'s own `fields` map) needs real path-parsing code with no existing precedent anywhere in this codebase to reuse; a flat top-level map is enough to make every v1 connector's output usable as a workflow variable today. |
| 14 | 2026-08-12 | "Document ref" (used across `storage`, `document-extract`, `send-email`'s field tables) is a plain opaque string, issued by the `storage` connector's own `fetch`/`upload` output and consumed as-is by any other connector's document-ref-typed field — no separate Document Service. | The term was used five times across the catalogue without ever being defined; a minimal, self-issued opaque reference is enough to make those fields concrete without inventing a new subsystem this feature doesn't otherwise need. |
| 16 | 2026-08-17 | A connector task's result reaches the workflow via a **new** `execution_service` endpoint (`POST /internal/connector-tasks/:id/{complete,fail}`), not the same completion endpoint a human uses. Both endpoints are thin siblings that ultimately call the same interpreter signal resolution (`stage-transition`/`stage-fail`) — Decision #1's substance ("an existing signal-wait resolution, not a new Temporal Activity") still holds; only the HTTP entry point differs. | `execution_service`'s `TaskService.checkHumanActionable` explicitly rejects a connector-typed task on every human-facing action, including `Complete`, since one has no human assignee. `cmd/connector-worker` never touches the Temporal SDK directly (§6.1) — the new endpoint is what touches it, on `execution_service`'s own side, same as every other `TaskService` method already does. The endpoint also applies `IOMapping.Outputs`' rename (Decision #13) itself, since `execution_service` is where that mapping is persisted (at task-creation time); `cmd/connector-worker` sends `Connector.Execute`'s raw output, unmodified. |
| 17 | 2026-08-17 | `sql-query`'s data-access model, confirmed: an internal HTTP proxy to the owning service's own query-execution endpoint (structurally identical to `rest-call`), never a direct database connection held by `cmd/connector-worker`. | §6.4.5's "internal platform databases only" phrasing, combined with §6.2's "both required, both mandatory" `x-internal-token`/`x-departments` header language, was ambiguous between this reading and a direct-DB-connection alternative (headers have no meaning on a raw DB connection, which was the deciding factor). Direct-DB access was rejected as a materially larger security surface — one process holding credentials to multiple services' own databases — for no offsetting benefit over each service running its own already-trusted, already-scoped query. |
| 18 | 2026-08-24 | `storage`, `send-email`, `document-extract`, `chat-notify` each become multi-provider: `aws-s3`/`azure-blob`/`gcp-gcs`/`google-drive`; `sendgrid`/`aws-ses`/`microsoft-365`/`google-workspace`; `aws-textract`/`azure-document-intelligence`/`gcp-document-ai`/`abbyy-vantage`; `slack`/`teams` (§6.4.1/6.4.2/6.4.3/6.4.6). A new `provider` input field selects which one, defaulting to the first-listed value when a task omits it. `rest-call`/`sql-query` are unaffected — internal-platform-only, no provider concept applies. | v1's catalogue committed to exactly one Camunda-reference provider per type; this org's actual environment spans multiple clouds and vendors per type, and hardcoding one would force every tenant onto whichever single vendor was picked first. Each provider still implements the same per-type interface (§6.3) already defined — additive, not a redesign. |
| 19 | 2026-08-25 | **`storage` is real for all 4 providers.** (1) No OpenBao path migration: every provider's credential fields are given globally-unique names (§6.4.1's table) instead — `resolveSecrets`/`Reader.Read` key purely off field name, never enumerate by provider, so a collision-free name avoids any path-format migration; this is pre-prod with zero real secrets ever written for `storage`. (2) `Config.StorageProviders` is a `map[string]StorageProviderConstructor` (one constructor per provider), not a map of pre-built clients — credentials are per-tenant and resolved fresh per call, so there's no fixed client to build once at `cmd/connector-worker` startup; a small bounded in-memory cache (keyed by provider+bucket+credential hash) avoids re-authenticating identical calls, real for `google-drive` specifically (its client construction does a JWT-bearer OAuth token exchange). (3) Doc-ref handling (`createDocument`/`content` round trip) is a store owned by `storageConnector` itself, constructed once — a per-call-constructed real client can't carry that state the way a single shared mock instance could. `aws-s3`/`azure-blob`/`gcp-gcs` are backed by `gocloud.dev/blob` (one dependency, three drivers) behind the unchanged `StorageProviderClient` interface; `google-drive` gets its own client since gocloud.dev has no Drive driver (a file/folder API, not bucket/key) — its `Upload` searches by name-in-folder and updates-if-found rather than blind-creating, which is what keeps this type's `RetryPolicySafe` (Decision #6) honest for Drive too. | The migration/pre-built-map/per-client-doc-ref shape doesn't fit the real code (`pkg/connectors/config.go`'s single-client-per-type fields, `resolveSecrets`'s field-name-only keying, `MockStorageClient`'s doc-ref map only ever working because tests reuse one instance). |
| 20 | 2026-08-25 | **Four `storage`/registry hardening rules.** (1) `storage` has no nil-client→mock fallback — an unconfigured or unwired provider returns `ErrValidation`, not a memory-only "success" that vanishes on restart. (2) An omitted `provider` field is `ErrValidation` too, never a default to `aws-s3` — a caller must always name a provider explicitly. (3) `send-email`/`document-extract`/`chat-notify` carry no `provider` field in the registry until each ships real per-provider code — none of the three has a real implementation behind it, so the field would offer a choice the platform couldn't fulfill; §6.4.2/6.4.3/6.4.6's tables reflect this until real work lands with each. (4) `google-drive`'s Drive API calls set `SupportsAllDrives`/`IncludeItemsFromAllDrives` — without them, a service account frequently can't see or write a folder shared via a Shared Drive (the realistic way an org grants folder access), even when explicitly shared with it. | A provider silently degrading to an in-memory mock, or a task silently defaulting to a provider it never asked for, are both failure modes that hide real misconfiguration instead of surfacing it — unacceptable once real credentials/real data are involved, not just mocks. Advertising a provider "choice" with zero code behind it is equally misleading to whoever reads `/connectors/registry`. |
| 21 | 2026-08-25 | **Alias-registry ownership is `definition_service`'s.** `definition_service` holds `connector_rest_alias`/`connector_sql_alias` tables (no `tenant_id`, no RLS — org-owned config, not tenant data, same class as `processed_event`) and serves `GET`/`POST`/`DELETE /internal/connector-aliases`, gated by the existing `x-internal-token` middleware (no new role/auth mechanism). `execution_service`'s `cmd/connector-worker` fetches the registry via HTTP at startup; `workflow-connectors`' `aliasconfig.Config`/`Endpoint`/`Query` types are the shared shape both `restcall.go`/`sqlquery.go` resolve against. **Hot-reload is not implemented**: `pkg/connectors`' `restCall`/`sqlQuery` connectors capture `Config.Aliases` by value at `New()` time, so a periodic re-fetch in connector-worker wouldn't reach already-built connector instances without also changing `Config.Aliases`'s type in `workflow-connectors` — deliberately not done; fetch-once-at-startup is today's actual static-per-process behavior. | Per-tenant credentials (§6.2) and now module/starter-template authoring both live in `definition_service`; the alias allowlist — which internal services are callable at all — is the same kind of platform-configuration concern, not something `execution_service`'s runtime should own or need to redeploy a binary to change. A static YAML file requiring a redeploy for a one-line alias edit was the wrong layer for org-owned, ops-managed configuration. |
| 22 | 2026-08-25 | **`send-email` is real for all 4 providers, following `storage`'s pattern (§10 Decision #19/#20).** (1) **Microsoft Graph uses a hand-rolled `sendMail` REST call plus `oauth2/clientcredentials`, not the official `msgraph-sdk-go` SDK** — that SDK is Kiota-generated and pulls in a large transitive dependency tree for what is here a single authenticated JSON POST, the same shape `rest-call`'s own dispatch already uses. (2) **`microsoft-365`'s `tenantId`/`clientId` are plain config values, not secrets-store references** — they're identifiers, not credentials, matching the existing `storage` precedent (`projectId` plain beside `gcpServiceAccountKey`). Only `clientSecret` is a real secret. (3) `docRefStore` is constructed once in `connectors.New()` and shared across `storage` and `send-email` — a doc ref minted by `storage`'s fetch must be resolvable by `send-email`, per the documented "attachments is a list of document refs" contract. (4) No per-attachment filename field exists in the registry — a filename is synthesized from the resolved content type instead. `google-workspace` reuses Drive's existing service-account/domain-wide-delegation pattern from `storage_drive.go`; Gmail's API only accepts a raw RFC 2822 message, so a small shared `buildRawMIME` helper (stdlib `mime`/`net/textproto` only) builds it — Amazon SES v2's own `SendEmail` API, by contrast, accepts structured `Attachments` directly on its `Simple` message type, so no raw-MIME path was needed there. | Same reasoning as Decision #19/#20: don't reach for a heavy dependency or invent new mechanisms when a lean one already fits, and don't ship a documented field ("attachments") that silently doesn't work end to end. |
| 23 | 2026-08-25 | **Two `send-email` invariants: cache-key completeness, and header-injection safety.** (1) **`emailCacheKey` includes `senderEmail`** for every provider — `google-workspace`'s client binds one impersonated mailbox (`jwtConfig.Subject`) at construction time, so the cache key must vary with `senderEmail`, not just provider+credentials: the same `serviceAccountKey` sending as two different `senderEmail` values must never reuse another sender's cached client. (2) **`buildRawMIME` strips embedded CR/LF** from every value feeding a raw RFC 2822 header line (Gmail's raw-message path only) — `senderName`/`receiverName`/`subject`, where `receiverName`/`receiverEmail` are explicitly documented as typically sourced from workflow-instance data (end-user-submitted form input per §6.4.2), so an unstripped embedded `\r\n` could otherwise inject arbitrary extra headers (e.g. `Bcc:`) into the outgoing message; `sendgrid`/`aws-ses`/`microsoft-365` build structured (JSON) requests, not raw header text, so this risk is specific to Gmail's raw-MIME path. Graph's `sendMail` URL path also `url.PathEscape`s `senderEmail` rather than concatenating it unescaped. | Both are exercised by regression tests covering the scenario the happy-path tests don't otherwise reach: two calls sharing credentials with different senders, and a name/subject value containing raw CRLF. |

## 11. Revision history

| Rev | Date | Change |
| --- | --- | --- |
| 1.0–4.2 | 2026-08-07 to 2026-08-12 | Initial design through four rounds of direction changes: worker placement proposed, reopened, and resolved twice; a credential/content callback API added, then removed entirely; `llm-verify` designed, scoped to a manual-override mechanism, then removed from the catalogue; the v1 catalogue narrowed from three connector types to seven to six; a companion document (`automatic_connector_tasks.md`) merged in and deleted. Superseded by the clean rewrite below. |
| 5.0 | 2026-08-12 | **Clean rewrite: settled design stated directly, no structural change left half-resolved.** Provider credentials for `storage`/`send-email`/`document-extract`/`chat-notify` now travel as a secrets-store reference, never a raw value (§6.2/§8) — closes the exposure gap in the previous raw-`context_json` design. §6.4's per-connector retry/idempotency guidance is ratified as final policy. A new per-connector-type internal execution timeout in `cmd/connector-worker` (§6.5) bounds a hung or failing call independently of the task's own SLA timer, resolving the former timeout/SLA/`DEGRADED` question with no interpreter, DSL, or schema change. §9 now carries only two narrow, non-blocking operational follow-ups (credential rotation/cleanup; confirming BE-for-UI's own implementation) instead of open design questions. §10's Decision Log and this Revision History are both trimmed to current-state-only, dropping the four-round churn narrative in favor of stating the settled design directly. Section numbers 1–11 are unchanged from rev 4.2, so every external cross-reference into this document (from `definition_service.md`, `execution_service.md`, `workflow_models_lib.md`) still resolves correctly. |
| 6.0 | 2026-08-12 | **Six gaps closed, surfaced by two independent implementation-readiness reviews (one with full project history, one deliberately blind) run before starting real implementation.** Credentials (§6.2/§6.4/§8/§10 Decision #5) concretized to AWS Secrets Manager + secret ARN, not just "a secrets-store reference." Event delivery to `cmd/connector-worker` (§2/§6.1/§6.5/§10 Decision #10) redesigned around a Valkey Stream `cmd/server`'s existing event handler pushes onto, rather than a new inbound listener — `cmd/connector-worker` now has no inbound HTTP surface at all. `document-extract` (§6.4.3/§10 Decision #11) scoped to real-time execution only, dropping async/polling fields that didn't fit `Connector.Execute`'s synchronous shape. `Connector.Execute`'s output (§6.3/§6.5/§10 Decision #13) is now concretely `map[string]any`, with `IOMapping.Outputs` addressing only top-level keys in v1. The `endpointAlias`/`queryAlias` registry (§6.4.4/§6.4.5/§9/§10 Decision #12) is now a static config file, with BE-for-UI's alias-discovery UX flagged as a non-blocking follow-up. "Document ref" (§6.2/§10 Decision #14), used five times with no definition, is now a minimal opaque string issued by `storage` and consumed as-is elsewhere. **The largest single change**: connector tasks are now fully automation-only (§2/§4.2/§5.3/§6.5/§10 Decision #9) — neither an unregistered connector type nor an exhausted/failed call falls back to a human anymore; both route through a new interpreter signal resolving to the existing `FAILED`/`DEGRADED` machinery. This is genuinely new interpreter surface (verified directly against `execution_service.md`: no signal for "this task failed" exists today — `FAILED`/`DEGRADED` have only ever originated from an Activity error inside the workflow function itself), not a documentation change, and is called out as the highest-risk item for implementation. Section numbers 1–11 stay unchanged from rev 5.0. |
| 7.0 | 2026-08-13 | **BE-for-UI/build-vs-absorb architecture review.** Connector-authoring templates (§1/§2/§4.3/§6.1/§6.3) and credential custody (§6.2/§8/§10 Decision #5, new Decision #15) reassigned from BE-for-UI to `definition_service` throughout this document — fixed, code-generated catalogue metadata with no tenant-stored content, a better fit for the service that already owns design-time authoring/compilation and already imports `pkg/registry`. BE-for-UI's own-implementation follow-up (§9) removed as moot — `definition_service` is a real, existing service, not a hypothetical one. `definition_service` inherits the alias-discovery-UX follow-up (§9) in its place. Secrets mechanism switched platform-wide from AWS Secrets Manager to **OpenBao** (§2, §6.2, §6.3, §6.5, §8, §10 Decision #5) — "secret ARN" replaced by "OpenBao secret path" (KV v2) everywhere; `secretsmanager:GetSecretValue` replaced by OpenBao's KV-v2 read API. No change to §3–§5, §7, or the catalogue itself (§6.4) — this pass only moves who owns the authoring/credential surface and what secrets backend it targets, not the connector execution model. |
| 7.1 | 2026-08-13 | Implementation begun: `workflow-models` (`StageTypeConnector`/`StageDef.ConnectorType`/`IOMapping`), `definition_service` (the `connector:` compiler exception, `/connectors/registry`, `/connectors/credentials`), and a new `workflow-connectors` module (`pkg/registry`, `pkg/connectors` stubs) are built and tested. Confirmed directly with the user: the authoring surface is a customized, restricted `bpmn.js` instance, not Camunda Desktop/Web Modeler — Camunda's own element-template JSON format (`$schema`/`appliesTo`/`properties[].binding`) doesn't apply here, and no such artifact is being built. Added the new Appendix below (a plain XML/API reference for whoever builds the `bpmn.js` palette entry and properties panel) in its place — a distillation of already-settled facts, not a new decision. |
| 7.2 | 2026-08-14 | **Two implementation-review passes (2 normal + 2 blind) found this table itself internally ambiguous in three places, closed here.** §6.4.6's `visibility` never stated its own enum values — now `public`/`private`, split onto its own row rather than sharing a "string / enum" row with `channelName`. §6.4.3's `signaturesDetected` ("bool / list") and §6.4.5's `params` ("list / map") each named two candidate types with no resolution — both settled to `list`, matching what `pkg/registry` already implemented (the code's choice was reasonable; the table just never confirmed it). §6.4.3's `confidence` output reworded from "float" to "map" (keyed by field name) to match its own "per-field confidence" language — a single scalar can't carry one score per extracted field. No change to the connector catalogue's actual scope or any other section. |
| 7.3 | 2026-08-17 | **Two contradictions found and fixed during real implementation of `cmd/connector-worker` and `workflow-connectors`' six real `Connector.Execute()` bodies, both now built.** §5.5/§10 Decision #1 corrected: completion goes through a **new** `execution_service` endpoint (`POST /internal/connector-tasks/:id/{complete,fail}`), not literally "the same endpoint a human uses" — `TaskService.checkHumanActionable` had already made that literal statement false (§10 Decision #16). §6.4.5 clarified: `sql-query` is an internal HTTP proxy to the owning service's own query-execution endpoint, never a direct database connection — the ambiguity between the two readings is resolved in code and here (§10 Decision #17). §6.5 step 3 also corrected on which side applies `IOMapping.Outputs`' rename: `execution_service`'s own new endpoint, not `cmd/connector-worker`, since that's where the mapping is already persisted at task-creation time. No change to the catalogue's scope, the six connector types, or any other decision in this document. |
| 8.0 | 2026-08-24 | **Real-provider gap found and designed: `storage`/`send-email`/`document-extract`/`chat-notify` had zero real SDK integration in `cmd/connector-worker` — every one ran against its in-memory mock in production, undocumented anywhere before this pass.** All four become multi-provider (§6.4.1/6.4.2/6.4.3/6.4.6, §10 Decision #18) rather than committing to one vendor per type: `storage` (S3/Azure Blob/GCS/Google Drive), `send-email` (SendGrid/SES/Microsoft 365/Google Workspace), `document-extract` (Textract/Azure AI Document Intelligence/GCP Document AI/ABBYY Vantage — the last three confirmed against Camunda's own Intelligent Document Processing feature, not assumed), `chat-notify` (Slack/Teams). §6.2 updated to note `provider` is an ordinary field, never itself a secret. Design only — no SDK code written this pass; `rest-call`/`sql-query` untouched. |
| 8.1 | 2026-08-25 | **§9's flat provider-implementation follow-up staged into 7 ordered phases**, found during a status-board review pass to need concrete sequencing before anyone could start it. Surfaced two real gotchas the flat list hid: the OpenBao credential path is a hardcoded 3-segment format duplicated in lockstep across `execution_service` and `definition_service` since a 2026-08-14 path-traversal fix, so adding `provider` as a 4th segment is a breaking two-repo change, not a `deps.go` wiring detail; and Decision #6's per-type "storage is safe to retry" policy doesn't hold for `google-drive`'s opaque-`fileId`-keyed creates once storage has multiple providers, flagged for revisiting when that phase starts. No change to the catalogue's scope or any other decision. |
| 8.2 | 2026-08-25 | **`storage` implemented real for all 4 providers (§10 Decision #19), same day as rev 8.1's staging** — three corrections to that staging's literal wording found doing the work: no OpenBao path migration (collision-free per-provider field names instead, §6.4.1's table updated); `Config.StorageProviders` is a per-call constructor map with a small credential-hash cache, not a map of pre-built clients (per-tenant secrets can't be baked in at `cmd/connector-worker` startup); doc-ref handling moved to a store owned by the connector itself, not the per-call client. `send-email`/`document-extract`/`chat-notify` are unaffected, still mock — §9 updated to reflect `storage`'s completion and drop the now-superseded migration/pre-built-map framing for the remaining 3 types' own follow-on work. |
| 8.3 | 2026-08-25 | **Review of rev 8.2's own implementation found four more corrections (§10 Decision #20), same day.** `storage`'s nil-client→mock fallback and the omitted-`provider`-defaults-to-`aws-s3` behavior are both removed — an unconfigured provider or a missing `provider` field are now `ErrValidation`, never a silent, memory-only "success." `send-email`/`document-extract`/`chat-notify`'s `provider` field (added in the original phase-1 pass, before any of the three had real code) is removed until each ships its actual implementation — §6.4/§6.4.2/§6.4.3/§6.4.6 updated to say so directly rather than describe a schema that isn't there. `google-drive`'s Drive calls now request Shared Drive access explicitly, since a service account otherwise can't reliably read/write a folder shared via a Shared Drive. |
| 8.4 | 2026-08-25 | **Alias-registry ownership moves to `definition_service` (§10 Decision #21), same day.** §9's alias-discovery-UX follow-up updated to reflect the new `GET /internal/connector-aliases` endpoint (still no UI consumer); two new follow-ups added and explicitly flagged, not fixed: `rest-call`'s alias `BaseURL` has no internal-host enforcement despite being documented as internal-only, and `sql-query`'s data-access model needs a fuller review before more is built on it. No change to §6.3's `Connector`/`*ProviderClient` interfaces or §6.4's catalogue scope. |
| 8.5 | 2026-08-25 | **`send-email` implemented real for all 4 providers (§10 Decision #22), same day.** §6.4.2's table drops its "design only" notes and corrects the `microsoft-365` credential table (`clientSecret` is the only secrets-store reference; `tenantId`/`clientId` are plain config values). §9's provider-implementation follow-up updated: 8 of 14 provider clients now real (`storage` + `send-email`), only `document-extract`/`chat-notify` remain. A real cross-connector bug found and fixed along the way: `docRefStore` is now shared across `storage`/`send-email` via `connectors.New()` instead of being constructed fresh per connector, so a `storage`-minted doc ref is actually resolvable as an email attachment. |
| 8.6 | 2026-08-25 | **Post-shipment review of rev 8.5's own `send-email` code found and fixed two real bugs (§10 Decision #23), same day.** A cache-key gap that could silently send email impersonating the wrong `google-workspace` mailbox once two different senders shared one service account; and an email header-injection gap in Gmail's raw-MIME builder (`senderName`/`receiverName`/`subject` reaching raw RFC 2822 header lines with no CR/LF stripping — `receiverName` is documented as typically workflow-instance/end-user data). Both fixed, both covered by regression tests, the cache one confirmed to fail pre-fix. No change to §6.4's catalogue or interfaces. |
| 8.7 | 2026-09-09 | **Credential rotation and cleanup (§9's own tracked gap) closed for the revocation half — rotation itself needed no new mechanism.** §6.2 now documents both directly: rotation is re-submitting the authoring form onto the same OpenBao path; revocation is a new admin-gated `DELETE /api/v1/connectors/credentials/:connector_type/:field_name`, backed by a real KV-v2 metadata-destroy on `port.SecretsClient`. §9's bullet for this removed now that it's settled design, not an open item. |
| 8.8 | 2026-09-10 | The 9 dotted-lowercase `workflow.task.*` citations renamed to PascalCase, following `execution_service.md` rev 1.45's reversal of Appendix A.4 decision 11. Documentation-only; no connector contract, alias, or auth detail changes. |
| 8.9 | 2026-09-15 | **`rest-call`'s alias `BaseURL` gap closed; §9's remaining credential gap narrowed to orphan cleanup and TTL.** The `BaseURL` bullet was a known, deliberately-deferred hole: `restcall.Execute` attaches the caller's real `x-internal-token`/`x-departments` headers to whatever host an alias names, and the alias registry is org-wide with no RLS, so an outward-pointing alias handed that host a valid internal credential. `definition_service` now enforces an internal-host allowlist at alias-write time (`CONNECTOR_ALIAS_ALLOWED_HOSTS`, exact hosts or a leading-dot suffix rule), rejects non-http(s) schemes and embedded userinfo, and fails closed on an empty allowlist — enforcement sits on the write path rather than in `restcall.go` because the allowlist is deployment configuration and this library takes none (`definition_service.md` §10.15). This had to land before any real alias row is seeded. The credential follow-up is re-stated at its true scope: rotation needs no mechanism and revocation shipped in rev 8.7, so what remains is that nothing sweeps a secret whose owning connector task or workflow version is gone, and that no TTL exists — neither of which has anything in Postgres to drive a sweep from. |
| 8.10 | 2026-09-24 | **The worker reads only the task's own credential path (§6.2, §8).** A secret-reference value is the only path an instance can influence, and the worker's token spans every tenant, so a task in one tenant could name another tenant's credential and have it read. The worker now requires exactly `connectors/<task tenant>/<connector type>/<field>` and fails the task with `secret_ref_invalid` otherwise. |
| 8.11 | 2026-09-24 | **A task's connector runs once, however often its entry is delivered (§6.5 step 0).** A failed callback left the entry unacked, and every redelivery re-ran the connector, roughly every 30 seconds with no limit. An entry still queued or running looked idle and was reclaimed, and shutdown cancelled a run mid-flight so that it reported nothing. The worker now resolves the actor first, records each outcome and re-sends it on redelivery, holds an execution lease per task, touches the entries it is still handling so only a dead consumer's are reclaimed, lets a started run finish across shutdown, and dead-letters an entry past its delivery cap. |

## Appendix — BPMN Authoring Reference (for UI Implementers)

Plain reference for whoever builds the connector palette entry and properties panel in the platform's customized `bpmn.js` instance. **Not** Camunda Desktop/Web Modeler — Camunda's element-template JSON format doesn't apply here (§10 Decision log, rev 7.1 row). Nothing below is a new decision; every fact is already settled elsewhere in this document or already live in code.

### The XML contract

A connector-typed stage is a plain `<bpmn:serviceTask>` carrying two `<bpmn:extensionElements>` children — this is the exact shape `definition_service`'s compiler parses (`internal/bpmn_compiler/parser.go`/`bpmncore/compile.go`) and its own tests assert. Don't improvise a different shape.

```xml
<bpmn:serviceTask id="Activity_1" name="Send confirmation email">
  <bpmn:extensionElements>
    <zeebe:taskDefinition type="connector:send-email"/>
    <zeebe:ioMapping>
      <zeebe:input source="sendgrid" target="provider"/>
      <zeebe:input source="secret/connectors/&lt;tenant&gt;/send-email/apiKey" target="apiKey"/>
      <zeebe:input source="no-reply@example.com" target="senderEmail"/>
      <zeebe:input source="=applicantEmail" target="receiverEmail"/>
      <zeebe:input source="Your application was received" target="subject"/>
      <zeebe:input source="text/plain" target="contentType"/>
      <zeebe:input source="Thanks for applying." target="body"/>
      <zeebe:output source="=sent" target="email_sent"/>
      <zeebe:output source="=messageId" target="email_message_id"/>
    </zeebe:ioMapping>
  </bpmn:extensionElements>
</bpmn:serviceTask>
```

- `zeebe:taskDefinition`'s `type` attribute is always `connector:<name>`, where `<name>` is one of `pkg/registry`'s type constants: `storage`, `send-email`, `document-extract`, `rest-call`, `sql-query`, `chat-notify`.
- Every input/output field a connector type declares (§6.4) becomes one `<zeebe:input>`/`<zeebe:output>` element. `target` is always the field's own name (`apiKey`, `bucket`, `operation`, ...), exactly as named in `pkg/registry`. `source` is whatever the author entered — a literal value, or `=<variableName>` to reference a workflow variable — except for credential fields (see below), whose `source` is never author-entered directly.

### Where the field list comes from

Don't hand-copy §6.4's tables into the UI. Call `GET /connectors/registry` (served live by `definition_service`, generated from the same `pkg/registry` Go package the compiler validates against) and render the returned field list directly. This can never drift out of sync the way a hand-maintained copy would.

### The one rule that isn't optional: credential fields

Four connector types have a `secret_ref`-kind field: `storage.accessKey`/`.secretKey`, `send-email.apiKey`, `document-extract.accessKey`/`.secretKey`, `chat-notify.authToken`. The raw credential the author types into that field must **never** be written into the BPMN XML. Before writing its `<zeebe:input>` element:

1. Call `POST /connectors/credentials` with the raw value (`{"connector_type", "field_name", "value"}`).
2. Write only the response's `secret_path` as the `source` attribute — never the raw value.

This isn't a style preference. The platform's OpenBao-backed credential design (§6.2/§8) assumes it; a UI that skips this step puts a live secret directly into `context_json`, Temporal's workflow history, and the `WorkflowTaskCreated` event payload.

The worked example above shows this: `apiKey`'s `source` is an OpenBao secret path, not a literal API key — everything else is a literal or a `=variable` reference, written directly.
