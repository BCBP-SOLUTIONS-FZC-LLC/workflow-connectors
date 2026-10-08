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

---

## Testing
- [ ] Tests added/updated (`make test`)
- [ ] All tests passing with race detector (`make test-ci`)
- [ ] Golden round-trip fixture updated if a struct/field changed (see CONTRIBUTING.md)

---

## Checklist

### Code Quality
- [ ] Code is properly formatted (`gofmt`)
- [ ] Linting passed (`make lint`)
- [ ] Vet passed (`make vet`)
- [ ] No debug logs / commented-out code
- [ ] Exported symbols have godoc comments

### Security
- [ ] No secrets hardcoded
- [ ] `make vuln-check` passes

### Documentation
- [ ] README updated (if public API changed)
- [ ] `CHANGELOG.md` `[Unreleased]` section updated
- [ ] `VERSIONING.md` impact assessed if `pkg/connectors` or `pkg/registry` changed

---

## Related Issue
Closes #<issue-id>

---

## Deployment Notes
Mention anything important for consumers upgrading (e.g. workflow-definition-service / execution-service migration steps, new required fields).
