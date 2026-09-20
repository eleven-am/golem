SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
GO_DIR := go
TS_DIR := typescript
GO_BINARY := $(GO_DIR)/bin/golem
VERIFY := GO=$(GO) scripts/verify.sh

export GOLEM_TEST_POSTGRES_DSN ?= postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable
export GOLEM_TEST_POSTGRES_LINGUISTIC_DSN ?= postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable
export GOLEM_TEST_PGVECTOR_DSN ?= postgresql://postgres:golem@127.0.0.1:55434/golem?sslmode=disable

.PHONY: help install build test check \
	ts-install ts-build ts-test ts-benchmark \
	go-download go-build go-install go-vuln \
	gate verify release-check postgres-up postgres-check

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install: ts-install go-download ## Install locked TypeScript and Go dependencies

build: ts-build go-build ## Build TypeScript packages, demo, and the Golem CLI

test: ts-test postgres-check ## Run TypeScript tests and every Go step except race
	$(VERIFY) $$($(VERIFY) list | grep -vxF race)

check: ## Check Go formatting and vet
	$(VERIFY) format vet

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

go-vuln: ## Scan for reachable known vulnerabilities with the pinned govulncheck
	$(VERIFY) vulncheck

gate: ## Fast pre-push gate: drift and byte-identity, no database required
	$(VERIFY) gate

verify: postgres-check ## Run every CI step locally, reporting all failures at once
	$(VERIFY)

release-check: postgres-check ## Run before pushing a go/v* tag: the vulnerability scan plus the full suite
	$(VERIFY) vulncheck $$($(VERIFY) list)

postgres-up: ## Start the PostgreSQL test servers, creating them when absent
	@docker info >/dev/null 2>&1 || { echo "Docker is not running; start it first" >&2; exit 1; }
	@for spec in \
		"golem-pg-c|postgres:17|55433|-e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_INITDB_ARGS=--locale=C" \
		"golem-pg-linguistic|postgres:17|55432|-e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_INITDB_ARGS=--locale=en_US.utf8" \
		"golem-pgvector|pgvector/pgvector:pg17|55434|-e POSTGRES_PASSWORD=golem"; do \
		name=$${spec%%|*}; rest=$${spec#*|}; image=$${rest%%|*}; rest=$${rest#*|}; port=$${rest%%|*}; env=$${rest#*|}; \
		if [ -n "$$(docker ps -q -f name=^$$name$$)" ]; then continue; fi; \
		if [ -n "$$(docker ps -aq -f name=^$$name$$)" ]; then \
			docker start "$$name" >/dev/null || exit 1; \
		else \
			echo "creating $$name on $$port"; \
			docker run -d --name "$$name" -p "127.0.0.1:$$port:5432" -e POSTGRES_DB=golem $$env "$$image" >/dev/null || exit 1; \
		fi; \
	done
	@for attempt in $$(seq 1 60); do \
		$(MAKE) --no-print-directory postgres-check >/dev/null 2>&1 && exit 0; \
		sleep 1; \
	done; \
	echo "the test servers did not accept connections in time" >&2; exit 1

postgres-check:
	@for name in GOLEM_TEST_POSTGRES_DSN GOLEM_TEST_POSTGRES_LINGUISTIC_DSN GOLEM_TEST_PGVECTOR_DSN; do \
		dsn="$${!name:-}"; \
		test -n "$$dsn" || { echo "$$name is required" >&2; exit 1; }; \
	done
	@test "$${GOLEM_TEST_POSTGRES_DSN}" != "$${GOLEM_TEST_POSTGRES_LINGUISTIC_DSN}" || { echo "PostgreSQL C and linguistic DSNs must differ" >&2; exit 1; }
	@command -v pg_isready >/dev/null || { echo "pg_isready is required to confirm the test servers accept connections" >&2; exit 1; }
	@down=""; \
	for name in GOLEM_TEST_POSTGRES_DSN GOLEM_TEST_POSTGRES_LINGUISTIC_DSN GOLEM_TEST_PGVECTOR_DSN; do \
		dsn="$${!name}"; \
		pg_isready -d "$$dsn" -t 5 >/dev/null 2>&1 || down="$$down\n  $$name"; \
	done; \
	test -z "$$down" || { printf "these test servers are not accepting connections:%b\nRun: make postgres-up\nA DSN may carry a password, so only the variable is named.\n" "$$down" >&2; exit 1; }
