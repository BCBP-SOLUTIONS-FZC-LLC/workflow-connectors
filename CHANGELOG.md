# Changelog

All notable changes to `workflow-connectors` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `pkg/registry` — `Definition`/`Field` metadata for all 6 v1 connector types (`design/LLD/workflow_connectors.md` §6.4).
- `pkg/connectors` — `Connector` interface and stub implementations (`ErrNotImplemented`) for all 6 v1 types. Real provider implementations are separate, later work.
