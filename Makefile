SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
GO_DIR := go
TS_DIR := typescript
GO_BINARY := $(GO_DIR)/bin/golem

export GOLEM_P8_REQUIRE_POSTGRESQL ?= 1
export GOLEM_TEST_POSTGRES_DSN ?= postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable
export GOLEM_TEST_POSTGRES_LINGUISTIC_DSN ?= postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable

.PHONY: help install build test check \
	ts-install ts-build ts-test ts-benchmark \
	go-download go-build go-install go-test go-race go-quality go-vuln \
	postgres-check

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

go-test: postgres-check ## Run the serial Go suite
	cd $(GO_DIR) && GOWORK=off $(GO) test -p=1 -count=1 -timeout=45m ./...

go-race: postgres-check ## Run the serial Go race suite (long)
	cd $(GO_DIR) && GOWORK=off $(GO) test -race -p=1 -count=1 -timeout=45m ./...

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
