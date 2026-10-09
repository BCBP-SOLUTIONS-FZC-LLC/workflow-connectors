## Description
Provide a clear description of the changes.

---

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Refactor
- [ ] Documentation
- [ ] Test
- [ ] Breaking change
- [ ] New migration
- [ ] Registry contract change (`pkg/registry` — connector type, field, error class or retry rule)

---

## Testing
- [ ] Unit tests added/updated (`make test-unit`)
- [ ] PostgreSQL store tests added/updated (`make test-postgres` — direct and through PgBouncer)
- [ ] Integration tests added/updated (`make test-integration` — Valkey, S3)
- [ ] All tests passing with race detector (`make test-ci`)
- [ ] Manual testing performed (if required)

---

## Checklist

### Code Quality
- [ ] Code is properly formatted (`make fmt-check`)
- [ ] Linting passed (`make lint`)
- [ ] Vet passed (`make vet`)
- [ ] Architecture rules hold (`make arch-lint`)
- [ ] No debug logs / commented-out code
- [ ] Exported symbols have godoc comments

### Security
- [ ] No secrets or DSNs hardcoded
- [ ] Credentials, tokens and document content are not logged or put in error messages
- [ ] Database access goes through platform-pgcommon only (depguard `pgcommon-only`)
- [ ] `make vuln-check` passes

### Registry contract
*Complete only when `pkg/registry` or a connector's input/output changed.*
- [ ] Registry definitions (`pkg/registry`) and the LLD field tables updated
- [ ] Retry policy and error classes match `docs/runbooks/retry-semantics.md`
- [ ] Change is backward-compatible for workflow-definition-service and execution-service, or flagged as breaking with a `VERSIONING.md` assessment

### Database / Migrations
*Complete only when a `sqlstore/migrations/` directory changed.*
- [ ] New migrations have matching `.up.sql` and `.down.sql`
- [ ] Down migration correctly reverses the up migration
- [ ] Migration tested against a direct connection (`MIGRATION_DATABASE_URL`) and the store against PgBouncer simple-protocol mode (`PG_BOUNCER_MODE=true`)

### Documentation
- [ ] README updated (if public API changed)
- [ ] ARCHITECTURE.md updated (if layering, flows, or key invariants changed)
- [ ] Runbooks (`docs/runbooks/`) updated if operational behaviour changed
- [ ] `CHANGELOG.md` `[Unreleased]` section updated
- [ ] `VERSIONING.md` impact assessed if `pkg/connectors` or `pkg/registry` changed

---

## Related Issue
Closes #<issue-id>

---

## Deployment Notes
Mention anything important for consumers upgrading (e.g. workflow-definition-service / execution-service migration steps, new required fields, Valkey/S3 settings, new migrations).
