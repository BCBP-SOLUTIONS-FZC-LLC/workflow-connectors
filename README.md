# workflow-connectors

The fixed catalogue of automatic connector tasks for the workflow engine: metadata for the authoring side and the implementations the Execution Service's connector worker runs.

**Repository:** `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors`  
**Module:** Go 1.26.6+ · private module · a library with no server and no logging, metrics, tracing, database or event code of its own · not deployed on its own

---

## Mental model

| Package | What it owns | What it does NOT own |
|---------|-------------|----------------------|
| `pkg/registry` | Compile-time metadata for the catalogue: type names, authoring field descriptions, retry policy. No SDK dependencies | Running a connector |
| `pkg/connectors` | The `Connector` implementations, built from one `Config` by `connectors.New` | Resolving credentials, workflow context, retries (the caller's job) |
| `pkg/connectors/aliasconfig` | The `endpointAlias` / `queryAlias` registry schema, its loader and resolvers | The alias file's contents: the worker owns and loads it |
| `pkg/connectors/shared` | Sentinel errors, input helpers, the document-reference store, the internal-call header names | Anything specific to one connector |

**Source of truth for one platform-wide contract:** the connector catalogue, meaning which types exist, which fields each takes, and which of them are secret references.

---

## Why this library exists

A `connector:`-prefixed BPMN service task runs without a human and resolves its next exclusive-gateway branch from its result. Definition Service needs only the type list and field shapes to compile and to generate authoring templates. Execution Service's connector worker needs the implementations and their SDKs. Two packages keep the heavy dependencies (AWS SDK, SendGrid, cloud storage) out of Definition's build. Design: `docs/lld/workflow_connectors.md`.

---

## Contents

- [Mental model](#mental-model)
- [Why this library exists](#why-this-library-exists)
- [Architecture](#architecture)
- [Catalogue](#catalogue)
- [Consuming this module](#consuming-this-module)
- [Security](#security)
- [Local development](#local-development)
- [Testing](#testing)
- [CI](#ci)
- [Versioning and releases](#versioning-and-releases)
- [Out of scope](#out-of-scope)
- [See also](#see-also)

---

## Architecture

```text
pkg/registry                 ← imported by definition_service (no heavy dependencies)
pkg/connectors               ← imported by execution_service's cmd/connector-worker
  ├─ aliasconfig             ← alias schema, loader, resolvers
  ├─ shared                  ← errors, helpers, document refs, header names
  └─ storage · sendemail · chatnotify · restcall · sqlquery · documentextract
```

- `pkg/registry` never imports `pkg/connectors`, and Definition Service never imports `pkg/connectors`.
- Every connector package imports `shared` and never the root `pkg/connectors`, so there is no import cycle. The root re-exports `shared`'s sentinel errors and context helpers.
- Provider backends are injected through `Config`, one constructor per provider name, called per request because credentials are per tenant. A provider with no constructor is a validation error, never a mock fallback.

---

## Catalogue

Four connectors are active in `connectors.New` and listed in `registry.All()`:

| Type | Mechanism | Providers |
|------|-----------|-----------|
| `storage` | Fetch, upload or delete a document | `aws-s3`, `azure-blob`, `gcp-gcs` (through `gocloud.dev/blob`), `google-drive` |
| `send-email` | Send an email, with document-ref attachments | `sendgrid`, `aws-ses`, `microsoft-365`, `google-workspace` |
| `rest-call` | HTTP call to an internal platform endpoint, resolved by alias | none; carries the internal token and `x-departments` |
| `chat-notify` | Post a chat notification | in-memory mock client until a real provider is passed as `Config.ChatNotifyClient` |

`sql-query` and `document-extract` are implemented in their subpackages but are neither wired into `connectors.New` nor listed in `registry.All()`. Their type constants remain because the subpackages still use them.

Field tables per connector are in `pkg/registry` and the LLD §6.4.

---

## Consuming this module

```bash
go env -w GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*
go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors@vX.Y.Z
go mod tidy
go mod vendor   # if the consuming service vendors dependencies
```

Never add a `replace` directive pointing at a filesystem path: consume a tagged version.

**Compile side (Definition Service):** import `pkg/registry` only.

```go
defs := registry.All()
def, known := defs[registry.TypeStorage]
```

**Run side (connector worker):**

```go
byType, err := connectors.New(connectors.Config{
    Aliases:            aliases,
    HTTPClient:         httpClient,
    InternalToken:      internalToken,
    StorageProviders:   storageProviders,
    SendEmailProviders: emailProviders,
    ChatNotifyClient:   chatClient,
})

ctx = connectors.WithDepartments(ctx, departments)
out, err := byType[registry.TypeStorage].Execute(ctx, input)
```

`connectors.New` fails when `InternalToken` is empty. Errors wrap `connectors.ErrValidation`, `connectors.ErrUpstream` or `connectors.ErrMissingInternalAuth`, so callers branch with `errors.Is`. `rest-call` fails closed with `ErrMissingInternalAuth` when the departments are absent from the context.

---

## Security

- **Credentials never travel in a plan.** For every field the registry marks as a secret reference, the worker reads the tenant's stored credential from OpenBao just before `Execute` and passes the resolved value. A connector never sees an OpenBao path, and a diagram never names a credential (LLD §6.2, rev 8.13).
- **`gcp-gcs` accepts only a `service_account` key.** Other credential types, such as workload-identity configurations that read the worker's filesystem, are refused.
- **`rest-call` reaches internal platform services only.** Every call carries `x-internal-token` and the caller's `x-departments` header.
- **Email headers are sanitised.** Sender, receiver and subject values are stripped of CR and LF before they reach a raw header, and the Graph path escapes the sender in its URL.

Vulnerability reporting: [SECURITY.md](./SECURITY.md).

---

## Local development

```bash
export GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*
make setup      # downloads modules and installs the pre-commit hook (tidy, fmt-check, lint)
make ci         # tidy, fmt-check, vet, lint, test-ci (race), build
make test       # tests only
make vuln-check # govulncheck, pinned version
make cover-func # coverage by function
```

There is no `.env`, no Docker and no database. Provider SDKs are exercised through fake clients and in-memory buckets.

---

## Testing

Tests sit next to the code in each package. Provider clients are replaced by fakes behind each connector's provider interface, and `gocloud.dev/blob/memblob` stands in for the object stores. `pkg/registry` has per-field assertions (name, kind, required, enum values) for every connector, plus a rule that no secret-reference description claims the credential is author-supplied.

---

## CI

Workflows in `.github/workflows/`: `ci.yml` (quality, test and a Trivy filesystem scan on pushes and pull requests), `changelog-check.yml` (a PR that touches `pkg/` must update `CHANGELOG.md`) and `release.yml` (tag-triggered; the tag's CHANGELOG section becomes the GitHub Release body). `Build image (cache)`, `Lint Dockerfile` and `Smoke tests` are no-op jobs that satisfy the org branch-protection ruleset, since a library has no image.

---

## Versioning and releases

SemVer, described in [VERSIONING.md](./VERSIONING.md). A change to `Config`, a connector's input or output field names, or a sentinel error is breaking. After a PR merges, move `[Unreleased]` in `CHANGELOG.md` to a versioned heading, then tag:

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

---

## Out of scope

- The connector worker, the credential and callback APIs, and per-connector field catalogues beyond the registry: Execution Service.
- BPMN compilation and the `connector:` task rule: Definition Service.
- Reading secrets from OpenBao: the caller, before `Execute`.
- Any logging, metrics, tracing, database or event code: this module has none.

Ownership: the workflow team (see CODEOWNERS in `.github/`). The repo is private and carries no `LICENSE` file.

---

## See also

| Document | Description |
| --- | --- |
| [ARCHITECTURE.md](./ARCHITECTURE.md) | Why two packages, the secret-resolution boundary, the `sql-query` data-access model, status |
| [docs/lld/workflow_connectors.md](./docs/lld/workflow_connectors.md) | Full design, kept content-identical to the design repo's LLD |
| [VERSIONING.md](./VERSIONING.md) | SemVer rules and release process |
| [CHANGELOG.md](./CHANGELOG.md) | Per-version changes |
| [CONTRIBUTING.md](./CONTRIBUTING.md) | Development setup, PR checklist |
| [SECURITY.md](./SECURITY.md) | Vulnerability reporting |
