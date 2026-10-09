# -----------------------------
# CONFIG
# -----------------------------
# Local development environment (copy of .env-example; `make setup` creates
# it). It sets the compose host ports (WC_*), the TEST_* variables and
# pgcommon's PG_* settings; values here override the defaults below.
-include .env

# Follows iam-org-membership's Makefile. This module is a library, so it has
# no runtime configuration, binaries, image or event schemas: the service
# targets run,
# mock-servers, pin-base-images, test-e2e, test-smoke, metrics-*, schema-*,
# swag* and docs-serve have no counterpart here. Integration infrastructure
# comes from docker-compose.yml (docker-up / docker-down) rather than
# testcontainers.

APP_NAME      ?= workflow-connectors
GO            ?= go
BUILD_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Private modules — resolved via SSH (git@github.com:) using the global URL
# rewrite. No tokens needed for local dev; an SSH key registered with the
# BCBP-SOLUTIONS-FZC-LLC org is required.
export GOPRIVATE  ?= github.com/BCBP-SOLUTIONS-FZC-LLC/*
export GONOSUMDB  ?= github.com/BCBP-SOLUTIONS-FZC-LLC/*

export APP_NAME BUILD_VERSION

# Test package groups (explicit to handle per-group build tags cleanly).
TEST_UNIT_PKGS     := ./test/unit/...
# googledrive's white-box tests have a PostgreSQL leg behind the integration
# tag (stores_postgres_test.go), so the package also runs in the postgres
# suite; untagged, the unit suite runs its in-memory leg.
TEST_POSTGRES_PKGS := ./test/postgres/... ./pkg/connectors/storage/googledrive/...
TEST_INT_PKGS      := ./test/integration/...

# The test/postgres suites share one compose PostgreSQL (each test uses its own
# tenant or database); capped so a dev machine's connection count stays sane.
# Override on the CLI (e.g. `make test-postgres TEST_POSTGRES_PARALLEL=8`).
TEST_POSTGRES_PARALLEL ?= 4

# Every build tag the test/ tree declares (integration: test/postgres +
# test/integration). vet and lint run a second pass with all of them — CI's
# quality gate calls `make vet`/`make lint`, so without it the tagged test
# files would never be vetted or linted.
ALL_TEST_TAGS := integration

# White-box (package-internal) tests included in unit runs: the provider
# adapters' private SDK seams, shared's client-cache internals,
# valkeystore's durability rules and the duplicate-type guards (indexByType)
# in pkg/connectors and pkg/registry. Everything else lives under ./test.
TEST_INTERNAL_PKGS := ./pkg/...

COVER_PKG_LIST := $(shell $(GO) list ./pkg/... 2>/dev/null | tr '\n' ',' | sed 's/,$$//')

GOVULNCHECK_VERSION ?= v1.1.4
PKGSITE_VERSION     ?= v0.5.0

# golangci-lint is pinned in tools/go.mod, a separate module, so its
# dependencies never enter the library's go.mod (and every consumer's graph).
GOLANGCI_LINT ?= $(GO) tool -modfile=tools/go.mod golangci-lint

# Integration infrastructure: docker-compose.yml (postgres, pgbouncer, valkey,
# floci). Host ports are overridable; the TEST_* variables the tests read are
# derived from the same values, so a port change needs no other edit.
COMPOSE           ?= docker compose
WC_PG_PORT        ?= 55432
WC_PGBOUNCER_PORT ?= 55433
WC_VALKEY_PORT    ?= 56379
WC_FLOCI_PORT     ?= 4580
export WC_PG_PORT WC_PGBOUNCER_PORT WC_VALKEY_PORT WC_FLOCI_PORT

INT_ENV := TEST_POSTGRES_DSN='postgres://postgres:test@localhost:$(WC_PG_PORT)/connectors?sslmode=disable' \
           TEST_PGBOUNCER_DSN='postgres://postgres:test@localhost:$(WC_PGBOUNCER_PORT)/connectors?sslmode=disable' \
           TEST_VALKEY_ADDR='localhost:$(WC_VALKEY_PORT)' \
           TEST_S3_ENDPOINT='http://localhost:$(WC_FLOCI_PORT)' \
           TEST_VALKEY_DOCKER=1

# -----------------------------
# SETUP
# -----------------------------

.PHONY: setup
setup: install-hooks
	@test -f .env || cp .env-example .env
	$(GO) mod download
	@echo "Environment ready (.env), modules downloaded and git hooks installed"

.PHONY: install-hooks
install-hooks:
	@mkdir -p .git/hooks
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Installed git hooks"

# godoc: serve package documentation locally using pkgsite.
# Opens http://localhost:8080 — browse to the module path in the UI.
.PHONY: godoc
godoc:
	@echo "Starting pkgsite at http://localhost:8080 — press Ctrl-C to stop"
	$(GO) run golang.org/x/pkgsite/cmd/pkgsite@$(PKGSITE_VERSION) -open .

.PHONY: help
help:
	@echo "Available commands:"
	@echo "  make setup           - create .env from .env-example if missing, go mod download, install git hooks"
	@echo "  make tidy            - go mod tidy (library and tools module)"
	@echo "  make tidy-check      - fail if either go.mod is not tidy (CI, pre-commit)"
	@echo "  make fmt             - gofmt -w all files"
	@echo "  make fmt-check       - verify gofmt formatting (mirrors CI)"
	@echo "  make vet             - go vet (default build + every test build tag)"
	@echo "  make lint            - run golangci-lint (default build + every test build tag)"
	@echo "  make arch-lint       - run go-arch-lint against .go-arch-lint.yml"
	@echo "  make docs-check      - ARCHITECTURE.md diagrams identical to docs/architecture/mermaid/*.mmd"
	@echo "  make ci-scripts-test - regression tests for the CI scripts (detect-changes.sh)"
	@echo "  make api-compat      - apidiff vs the last release tag (API_NEW_VERSION=vX.Y.Z: fail unless compatible or a major bump)"
	@echo "  make test            - unit + postgres + integration tests (requires Docker)"
	@echo "  make test-ci         - test with race detector + coverage (used in CI)"
	@echo "  make test-unit       - unit tests only (no Docker required)"
	@echo "  make test-postgres   - PostgreSQL tests: stores direct + through PgBouncer (requires Docker)"
	@echo "  make test-integration - cross-layer integration tests (Valkey, floci S3, PostgreSQL; requires Docker)"
	@echo "  make race            - all tests with -race flag"
	@echo "  make build           - compile-check all packages"
	@echo "  make cover           - coverage HTML report"
	@echo "  make cover-func      - coverage summary by function"
	@echo "  make ci              - tidy-check + fmt-check + vet + lint + arch-lint + docs-check + ci-scripts-test + test-ci + build"
	@echo "  make docker-up       - start docker-compose.yml (postgres, pgbouncer, valkey, floci) and wait until healthy"
	@echo "  make docker-down     - stop local containers and delete their volumes"
	@echo "  make mod-verify      - go mod verify"
	@echo "  make vuln-check      - govulncheck on pkg (pinned $(GOVULNCHECK_VERSION))"
	@echo "  make install-hooks   - install .githooks/pre-commit into .git/hooks"
	@echo "  make godoc           - serve local godoc/pkgsite at http://localhost:8080"
	@echo "  make clean           - remove test/coverage artefacts"

# -----------------------------
# GO BASICS
# -----------------------------

.PHONY: tidy
tidy:
	$(GO) mod tidy
	cd tools && $(GO) mod tidy

# tidy-check: fail (without rewriting anything) when go.mod/go.sum of the
# library or of the tools module is not tidy. CI and the pre-commit hook run it.
.PHONY: tidy-check
tidy-check:
	@$(GO) mod tidy -diff || { echo "go.mod/go.sum not tidy — run 'make tidy'"; exit 1; }
	@cd tools && $(GO) mod tidy -diff || { echo "tools/go.mod not tidy — run 'make tidy'"; exit 1; }
	@echo "go.mod and tools/go.mod are tidy"

.PHONY: fmt
fmt:
	@gofmt -l -w .

.PHONY: vet
vet:
	$(GO) vet ./...
	$(GO) vet -tags=$(ALL_TEST_TAGS) ./...

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l . 2>/dev/null); \
	if [ -n "$$unformatted" ]; then \
		echo "FAIL: unformatted files:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "gofmt: all files formatted"

.PHONY: mod-verify
mod-verify:
	$(GO) mod verify

# Pinned rather than @latest so CI results don't shift under an unchanged commit.
.PHONY: vuln-check
vuln-check:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./pkg/...

# -----------------------------
# LINT
# -----------------------------

.PHONY: lint
lint:
	@echo "Running linter..."
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags=$(ALL_TEST_TAGS) ./...

# arch-lint: enforce .go-arch-lint.yml component boundaries — the same script
# CI's validate-quality.yml runs.
.PHONY: arch-lint
arch-lint:
	bash .github/scripts/arch-lint.sh

# docs-check: every docs/architecture/mermaid/*.mmd diagram is embedded
# verbatim in ARCHITECTURE.md (and every mermaid block there has a source).
.PHONY: docs-check
docs-check:
	python3 scripts/docs_check.py

# ci-scripts-test: regression tests for the CI shell scripts (no bats; scratch
# git repositories): detect-changes.sh's docs-only decision and log
# sanitising. validate-quality.yml runs the same target. No Docker.
.PHONY: ci-scripts-test
ci-scripts-test:
	bash .github/scripts/detect-changes_test.sh

# api-compat: exported-API diff (golang.org/x/exp/cmd/apidiff, pinned; x/exp
# has no tags) of the module against the previous stable release tag.
# Without API_NEW_VERSION it only warns (CI); with API_NEW_VERSION=vX.Y.Z it
# fails on an incompatible change unless vX.Y.Z bumps the major (release.yml).
APIDIFF_VERSION ?= v0.0.0-20260908205506-85c1c2202aba
API_NEW_VERSION ?=

.PHONY: api-compat
api-compat:
	APIDIFF_VERSION=$(APIDIFF_VERSION) bash .github/scripts/api-compat.sh $(API_NEW_VERSION)

# -----------------------------
# TESTS
# -----------------------------
# The postgres and integration suites need the docker-compose.yml stack. The
# public targets start it (docker compose up --wait is a no-op when it is
# already healthy); the parallel aggregates start it once before fanning out.

.PHONY: _test-unit
_test-unit: | .coverage
	$(GO) test $(TEST_UNIT_PKGS) $(TEST_INTERNAL_PKGS) \
	  -race -count=1 -timeout 120s \
	  -coverpkg=$(COVER_PKG_LIST) \
	  -coverprofile=.coverage/unit.out

.PHONY: _test-postgres
_test-postgres: | .coverage
	{ $(INT_ENV) $(GO) test $(TEST_POSTGRES_PKGS) \
	  -tags=integration -race -count=1 -timeout 600s -parallel $(TEST_POSTGRES_PARALLEL) \
	  -coverpkg=$(COVER_PKG_LIST) \
	  -coverprofile=.coverage/postgres.out \
	  2>&1; echo $$? >.coverage/postgres.exitcode; } | tee .coverage/postgres.raw; \
	_exit=$$(cat .coverage/postgres.exitcode 2>/dev/null || echo 1); \
	[ "$$_exit" = "0" ] || { \
	  printf '\n\n=== FAILING POSTGRES TESTS (see full log above for details) ===\n'; \
	  grep '^--- FAIL:' .coverage/postgres.raw || printf '(no --- FAIL lines — check for DATA RACE or panic above)\n'; \
	  printf '=============================================================\n\n'; \
	}; \
	exit "$$_exit"

.PHONY: _test-integration
_test-integration: | .coverage
	$(INT_ENV) $(GO) test $(TEST_INT_PKGS) \
	  -tags=integration -race -count=1 -timeout 300s \
	  -coverpkg=$(COVER_PKG_LIST) \
	  -coverprofile=.coverage/integration.out

.PHONY: test
test: docker-up
	$(MAKE) -j3 _test-unit-plain _test-postgres-plain _test-integration-plain

.PHONY: _test-unit-plain _test-postgres-plain _test-integration-plain
_test-unit-plain:
	$(GO) test $(TEST_UNIT_PKGS) $(TEST_INTERNAL_PKGS) -count=1 -timeout 120s -v
_test-postgres-plain:
	$(INT_ENV) $(GO) test $(TEST_POSTGRES_PKGS) -tags=integration -count=1 -timeout 300s -parallel $(TEST_POSTGRES_PARALLEL) -v
_test-integration-plain:
	$(INT_ENV) $(GO) test $(TEST_INT_PKGS) -tags=integration -count=1 -timeout 300s -v

# Merge the three per-suite profiles into a single coverage.out (max-count
# strategy — any suite covering a block wins). Mirrors iam-org-membership.
.PHONY: _merge-coverage
_merge-coverage:
	@python3 scripts/merge_coverage.py \
	  .coverage/unit.out .coverage/postgres.out .coverage/integration.out \
	  > coverage.out
	@echo "==> coverage.out merged from all suites (max-count strategy)"

.PHONY: test-ci
test-ci: docker-up | .coverage
	$(MAKE) -j3 _test-unit _test-postgres _test-integration
	$(MAKE) _merge-coverage

.PHONY: test-unit
test-unit:
	$(GO) test $(TEST_UNIT_PKGS) $(TEST_INTERNAL_PKGS) -count=1 -timeout 60s -v

.PHONY: test-postgres
test-postgres: docker-up
	$(INT_ENV) $(GO) test $(TEST_POSTGRES_PKGS) -tags=integration -count=1 -timeout 300s -parallel $(TEST_POSTGRES_PARALLEL) -v

.PHONY: test-integration
test-integration: docker-up
	$(INT_ENV) $(GO) test $(TEST_INT_PKGS) -tags=integration -count=1 -timeout 300s -v

.PHONY: race
race: docker-up
	$(MAKE) -j3 _test-unit _test-postgres _test-integration

# -----------------------------
# BUILD
# -----------------------------

.PHONY: build
build:
	@echo "Verifying library packages compile..."
	$(GO) build ./...

# -----------------------------
# DOCKER
# -----------------------------

.PHONY: docker-up
docker-up:
	@echo "Starting local PostgreSQL + PgBouncer + Valkey + floci (S3)..."
	$(COMPOSE) up -d --wait

.PHONY: docker-down
docker-down:
	@echo "Stopping local containers..."
	$(COMPOSE) down -v --remove-orphans

# -----------------------------
# CI
# -----------------------------

.PHONY: ci
ci: tidy-check fmt-check vet lint arch-lint docs-check ci-scripts-test test-ci build

# -----------------------------
# COVERAGE
# -----------------------------

.PHONY: cover
cover: test-ci
	$(GO) tool cover -html=coverage.out

.PHONY: cover-func
cover-func: test-ci
	$(GO) tool cover -func=coverage.out

# -----------------------------
# CLEAN
# -----------------------------

.coverage:
	@mkdir -p .coverage

.PHONY: clean
clean:
	rm -rf .coverage
	rm -f coverage.out coverage.html
