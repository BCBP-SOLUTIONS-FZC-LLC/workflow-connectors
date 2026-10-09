---
name: Bug report
about: Report a defect in workflow-connectors (pkg/connectors, pkg/registry)
title: '[BUG] '
labels: bug
assignees: ''
---

## Description
A clear description of the bug.

## Library version
`github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2 vX.Y.Z`

## Go version
`go version goX.Y.Z ...`

## Environment
- [ ] Local dev (`make docker-up` compose stack)
- [ ] Consuming service — workflow-definition-service
- [ ] Consuming service — execution-service (`cmd/connector-worker`)
- [ ] Staging
- [ ] Production

## Affected area
- [ ] Connector (type: `...`)
- [ ] Provider adapter (SendGrid / SES / Microsoft Graph / Gmail / Google Drive / gocloud S3 · Azure · GCS)
- [ ] Document refs (Valkey metadata / S3 content)
- [ ] Send intents (duplicate-send protection)
- [ ] Document registry (Drive upload claim/lease)
- [ ] Database / migrations
- [ ] Error classification / retry decision
- [ ] Registry (`pkg/registry`)

## Steps to reproduce
1.
2.
3.

## Expected behaviour
What you expected to happen.

## Actual behaviour
What actually happened. Include error messages, the error class and retry decision (`RetryDecision.LogAttrs()`), log output, or a failing round-trip diff.

```
// paste relevant log output or error here
```

## Minimal reproduction
```go
// paste the smallest Go snippet that triggers the bug
```

## Additional context
Any other relevant context (PostgreSQL version, PgBouncer mode, Valkey version and persistence settings, related issues).
