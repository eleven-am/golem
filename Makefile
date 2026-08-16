SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
GO_DIR := go
TS_DIR := typescript
GO_BINARY := $(GO_DIR)/bin/golem
GATE_WORK := $(CURDIR)/$(GO_DIR)/bin/gate.work
SOCIAL_DIR := $(GO_DIR)/examples/social

GO_DB_PATTERNS := ./cmd/golem ./golemtest ./internal/p7oracle ./internal/p8oracle/... \
	./internal/policy/oracle ./internal/provider/postgresql ./internal/read/decode \
	./provider/postgresql ./runtime

GO_RACE_PACKAGES := ./events/... ./provider/... ./runtime ./queue \
	./internal/event/outbox ./internal/event/cdc ./internal/queue/worker \
	./internal/subscription

GATE_IDENTITY_TESTS := TestP5GeneratedExtensionFixtureRegeneratesByteIdentically|TestP5GeneratedSocialFixtureRegeneratesByteIdentically|TestP6GeneratedMetricsFixtureRegeneratesByteIdentically|TestP6GeneratedArtifactsAreByteIdenticalAcrossShuffleAndRepeat
GATE_IDENTITY_COUNT := 4
GATE_GOLDEN_TEST := TestInspectSocialGoldenAndDeterminism

export GOLEM_P8_REQUIRE_POSTGRESQL ?= 1
export GOLEM_TEST_POSTGRES_DSN ?= postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable
export GOLEM_TEST_POSTGRES_LINGUISTIC_DSN ?= postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable
export GOLEM_TEST_PGVECTOR_DSN ?= postgresql://postgres:golem@127.0.0.1:55434/golem?sslmode=disable

.PHONY: help install build test check \
	ts-install ts-build ts-test ts-benchmark \
	go-download go-build go-install go-test go-race go-quality go-vuln \
	gate gate-work gate-check gate-names gate-full verify verify-quick go-test-fast go-test-db go-test-serial \
	go-tier-check postgres-check

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install: ts-install go-download ## Install locked TypeScript and Go dependencies

build: ts-build go-build ## Build TypeScript packages, demo, and the Golem CLI

test: ts-test go-test ## Run TypeScript and Go tests

check: go-quality ## Run fast repository quality checks

ts-install: ## Install locked npm workspace dependencies
	$(NPM) --prefix $(TS_DIR) ci

ts-build: ## Build all TypeScript packages and the demo
	$(NPM) --prefix $(TS_DIR) run build

ts-test: ## Test all TypeScript workspaces
	$(NPM) --prefix $(TS_DIR) test

ts-benchmark: ## Run the TypeScript hardening benchmark
	$(NPM) --prefix $(TS_DIR) run benchmark:hardening

go-download: ## Download and verify the Go module graph
	cd $(GO_DIR) && GOWORK=off $(GO) mod download
	cd $(GO_DIR) && GOWORK=off $(GO) mod verify

go-build: ## Build the Golem CLI
	mkdir -p $(dir $(GO_BINARY))
	cd $(GO_DIR) && GOWORK=off $(GO) build -trimpath -o bin/golem ./cmd/golem

go-install: ## Install the Golem CLI into GOBIN
	cd $(GO_DIR) && GOWORK=off $(GO) install ./cmd/golem

gate-work: go-build
	@printf 'go 1.25.0\n\nuse (\n\t%s/%s\n\t%s/%s\n)\n\nreplace github.com/eleven-am/golem/go v0.0.0 => %s/%s\n' \
		"$(CURDIR)" "$(GO_DIR)" "$(CURDIR)" "$(SOCIAL_DIR)" "$(CURDIR)" "$(GO_DIR)" > $(GATE_WORK)

gate-check: gate-work ## Prove the example's generated tree and migration head agree
	cd $(SOCIAL_DIR) && GOWORK=$(GATE_WORK) $(CURDIR)/$(GO_BINARY) check \
		--schema ./social --app-out ./social --migrations migrations

gate-names: ## Fail if a gate filter no longer matches the tests it names
	@cd $(GO_DIR) && matched=$$(GOWORK=off $(GO) test -list '$(GATE_IDENTITY_TESTS)' ./runtime | grep -c '^Test' || true); \
	test "$$matched" -eq $(GATE_IDENTITY_COUNT) || { echo "gate identity filter matched $$matched tests; want $(GATE_IDENTITY_COUNT)" >&2; exit 1; }
	@cd $(GO_DIR) && matched=$$(GOWORK=off $(GO) test -list '$(GATE_GOLDEN_TEST)' ./cmd/golem | grep -c '^Test' || true); \
	test "$$matched" -eq 1 || { echo "gate golden filter matched $$matched tests; want 1" >&2; exit 1; }

gate: gate-check gate-names ## Fast pre-push gate: drift and byte-identity, no database required
	cd $(GO_DIR) && GOWORK=off $(GO) test -count=1 -run '$(GATE_GOLDEN_TEST)' ./cmd/golem
	cd $(GO_DIR) && GOWORK=off $(GO) test -count=1 -run '$(GATE_IDENTITY_TESTS)' ./runtime
	cd $(GO_DIR) && GOWORK=off $(GO) test -count=1 ./internal/migration/workflow ./internal/physical ./observe

gate-full: gate go-test-fast ## The gate plus every database-free package

verify: postgres-check ## Run every check CI runs, locally, reporting all failures at once
	scripts/verify.sh full

verify-quick: postgres-check ## verify without the race and documentation passes
	scripts/verify.sh quick

go-tier-check: ## Fail if a package opens a database outside the declared serial tier
	@cd $(GO_DIR) && declared="$$(GOWORK=off $(GO) list -f '{{.Dir}}' $(GO_DB_PATTERNS))"; \
	status=0; \
	for file in $$(grep -rl 'GOLEM_TEST_POSTGRES_DSN\|GOLEM_TEST_POSTGRES_LINGUISTIC_DSN' --include='*.go' .); do \
		directory="$$(cd "$$(dirname "$$file")" && pwd)"; \
		case "$$directory" in "$$(pwd)/examples/"*) continue;; esac; \
		covered=0; \
		for candidate in $$declared; do \
			case "$$directory" in "$$candidate"|"$$candidate"/*) covered=1;; esac; \
		done; \
		if [ "$$covered" -eq 0 ]; then echo "undeclared database package: $$file" >&2; status=1; fi; \
	done; \
	exit $$status

go-test-fast: go-tier-check ## Run every database-free Go package in parallel
	cd $(GO_DIR) && GOWORK=off $(GO) test -count=1 -timeout=30m \
		$$(GOWORK=off $(GO) list ./... | grep -vxF -f <(GOWORK=off $(GO) list $(GO_DB_PATTERNS)))

go-test-db: postgres-check ## Run the database-bound Go packages serially
	cd $(GO_DIR) && GOWORK=off $(GO) test -p=1 -count=1 -timeout=45m $(GO_DB_PATTERNS)

go-test: go-test-fast go-test-db ## Run the full Go suite, parallel where safe

go-test-serial: postgres-check ## Run the whole Go suite serially (the old single-tier run)
	cd $(GO_DIR) && GOWORK=off $(GO) test -p=1 -count=1 -timeout=45m ./...

go-race: postgres-check ## Race-check the concurrency-bearing Go packages
	cd $(GO_DIR) && GOWORK=off $(GO) test -race -p=1 -count=1 -timeout=45m $(GO_RACE_PACKAGES)

go-quality: ## Check Go formatting, whitespace, and vet
	@test -z "$$(find $(GO_DIR) -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
	git diff --check
	cd $(GO_DIR) && GOWORK=off $(GO) vet ./...

go-vuln: ## Run govulncheck (install it separately first)
	cd $(GO_DIR) && GOWORK=off govulncheck ./...

postgres-check:
	@test -n "$(GOLEM_TEST_POSTGRES_DSN)" || { echo "GOLEM_TEST_POSTGRES_DSN is required" >&2; exit 1; }
	@test -n "$(GOLEM_TEST_POSTGRES_LINGUISTIC_DSN)" || { echo "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN is required" >&2; exit 1; }
	@test "$(GOLEM_TEST_POSTGRES_DSN)" != "$(GOLEM_TEST_POSTGRES_LINGUISTIC_DSN)" || { echo "PostgreSQL C and linguistic DSNs must differ" >&2; exit 1; }
