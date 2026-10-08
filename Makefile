# -----------------------------
# CONFIG
# -----------------------------
GO            ?= go
BUILD_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

export BUILD_VERSION

COVER_PKG_LIST := $(shell $(GO) list ./pkg/... | tr '\n' ',' | sed 's/,$$//')

.PHONY: setup install-hooks help tidy fmt fmt-check vet lint test test-ci build ci \
        cover cover-func mod-verify vuln-check clean

setup: install-hooks
	$(GO) mod download
	@echo "Modules downloaded and git hooks installed (no .env needed: this module has no runtime config)"

install-hooks:
	@mkdir -p .git/hooks
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Installed git hooks"

help:
	@echo "Available commands:"
	@echo "  make setup           - go mod download and install git hooks"
	@echo "  make install-hooks   - install .githooks/pre-commit"
	@echo "  make tidy            - go mod tidy"
	@echo "  make fmt             - go fmt ./..."
	@echo "  make vet             - go vet all packages"
	@echo "  make lint            - run golangci-lint"
	@echo "  make test            - run all tests"
	@echo "  make test-ci         - run all tests with race detector (used in CI)"
	@echo "  make build           - compile-check all packages"
	@echo "  make cover           - coverage profile + open HTML report"
	@echo "  make cover-func      - coverage summary by function"
	@echo "  make ci              - tidy + fmt-check + vet + lint + test-ci + build"
	@echo "  make fmt-check       - verify gofmt formatting (no changes applied)"
	@echo "  make mod-verify      - go mod verify (check module download integrity)"
	@echo "  make vuln-check      - govulncheck on library packages"
	@echo "  make clean           - remove test/coverage artefacts"

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt ./...

fmt-check:
	@unformatted=$$(gofmt -l pkg/); \
	if [ -n "$$unformatted" ]; then \
		echo "FAIL: unformatted files:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

lint:
	$(GO) tool golangci-lint run ./...

mod-verify:
	$(GO) mod verify

GOVULNCHECK_VERSION ?= v1.1.4
vuln-check:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

test:
	$(GO) test ./pkg/... -count=1

test-ci:
	$(GO) test ./pkg/... -count=1 -race

cover:
	$(GO) test ./pkg/... -coverprofile=coverage.out -covermode=atomic -coverpkg=$(COVER_PKG_LIST)
	$(GO) tool cover -html=coverage.out -o coverage.html

cover-func:
	$(GO) test ./pkg/... -coverprofile=coverage.out -covermode=atomic -coverpkg=$(COVER_PKG_LIST)
	$(GO) tool cover -func=coverage.out

build:
	$(GO) build ./...

ci: tidy fmt-check vet lint test-ci build

clean:
	rm -f coverage.out coverage.html
