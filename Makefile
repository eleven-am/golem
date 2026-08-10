SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
GO_DIR := go
TS_DIR := typescript
GO_BINARY := $(GO_DIR)/bin/golem
GO_TAG_PREFIX := go/v

GOLEM_TEST_POSTGRES_DSN ?= postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable
GOLEM_TEST_POSTGRES_LINGUISTIC_DSN ?= postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable

.PHONY: help install build test check \
	ts-install ts-build ts-test ts-benchmark \
	go-download go-build go-install go-test go-race go-quality go-vuln \
	postgres-check go-docs go-compat go-failure go-mutation go-verify \
	go-release-verify release-next release-patch release-minor release-major _release

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

go-test: ## Run the serial Go suite
	cd $(GO_DIR) && GOWORK=off $(GO) test -p=1 -count=1 -timeout=45m ./...

go-race: ## Run the serial Go race suite (long)
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

go-docs: postgres-check ## Run executable documentation evidence (long; PostgreSQL required)
	@cd $(GO_DIR) && GOWORK=off GOLEM_P8_REQUIRE_POSTGRESQL=1 \
		GOLEM_TEST_POSTGRES_DSN='$(GOLEM_TEST_POSTGRES_DSN)' \
		GOLEM_TEST_POSTGRES_LINGUISTIC_DSN='$(GOLEM_TEST_POSTGRES_LINGUISTIC_DSN)' \
		$(GO) run ./internal/cmd/p8docs -module . -timeout 45m

go-compat: postgres-check ## Run compatibility and upgrade evidence (long; PostgreSQL required)
	@cd $(GO_DIR) && GOWORK=off GOLEM_P8_REQUIRE_POSTGRESQL=1 \
		GOLEM_TEST_POSTGRES_DSN='$(GOLEM_TEST_POSTGRES_DSN)' \
		GOLEM_TEST_POSTGRES_LINGUISTIC_DSN='$(GOLEM_TEST_POSTGRES_LINGUISTIC_DSN)' \
		$(GO) run ./internal/cmd/p8compat -module . -timeout 45m

go-failure: postgres-check ## Run failure and recovery evidence (very long; PostgreSQL required)
	@cd $(GO_DIR) && GOWORK=off GOLEM_P8_REQUIRE_POSTGRESQL=1 \
		GOLEM_TEST_POSTGRES_DSN='$(GOLEM_TEST_POSTGRES_DSN)' \
		GOLEM_TEST_POSTGRES_LINGUISTIC_DSN='$(GOLEM_TEST_POSTGRES_LINGUISTIC_DSN)' \
		$(GO) run ./internal/cmd/p8failure -module . -timeout 90m

go-mutation: postgres-check ## Run the complete P8 mutation catalog (very long; PostgreSQL required)
	@cd $(GO_DIR) && GOWORK=off GOLEM_P8_REQUIRE_POSTGRESQL=1 \
		GOLEM_TEST_POSTGRES_DSN='$(GOLEM_TEST_POSTGRES_DSN)' \
		GOLEM_TEST_POSTGRES_LINGUISTIC_DSN='$(GOLEM_TEST_POSTGRES_LINGUISTIC_DSN)' \
		$(GO) run ./internal/cmd/p8mutation -repository .. -timeout 45m

go-verify: postgres-check ## Run the local P8 candidate audit (very long; PostgreSQL required)
	@cd $(GO_DIR) && GOWORK=off GOLEM_P8_REQUIRE_POSTGRESQL=1 \
		GOLEM_TEST_POSTGRES_DSN='$(GOLEM_TEST_POSTGRES_DSN)' \
		GOLEM_TEST_POSTGRES_LINGUISTIC_DSN='$(GOLEM_TEST_POSTGRES_LINGUISTIC_DSN)' \
		$(GO) run ./internal/cmd/p8verify -module . -timeout 30m

go-release-verify: ## Verify an existing signed candidate (TAG, signer trust, and PROXY required)
	@test -n "$(TAG)" || { echo "TAG=go/vX.Y.Z is required" >&2; exit 1; }
	@[[ "$(TAG)" =~ ^go/v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$$ ]] || { echo "TAG must be go/vX.Y.Z" >&2; exit 1; }
	@test -n "$(GOLEM_RELEASE_ALLOWED_SIGNERS)" || { echo "GOLEM_RELEASE_ALLOWED_SIGNERS is required" >&2; exit 1; }
	@test -n "$(GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256)" || { echo "GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256 is required" >&2; exit 1; }
	@test -n "$(PROXY)" || { echo "PROXY is required (use direct for the public candidate)" >&2; exit 1; }
	@cd $(GO_DIR) && GOWORK=off $(GO) run ./internal/cmd/p8release -mode verify \
		-tag '$(TAG)' -module . -proxy '$(PROXY)' \
		-allowed-signers '$(GOLEM_RELEASE_ALLOWED_SIGNERS)' \
		-allowed-signers-sha256 '$(GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256)'

release-next: ## Print the next Go semantic-version tag (BUMP=patch|minor|major)
	@$(MAKE) --no-print-directory _release BUMP=$${BUMP:-patch} DRY_RUN=1

release-patch: ## Sign and push the next Go patch tag
	@$(MAKE) --no-print-directory _release BUMP=patch

release-minor: ## Sign and push the next Go minor tag
	@$(MAKE) --no-print-directory _release BUMP=minor

release-major: ## Sign and push the next Go major tag
	@$(MAKE) --no-print-directory _release BUMP=major

_release:
	@current="$$(git tag --list '$(GO_TAG_PREFIX)*.*.*' --sort=-version:refname | head -1)"; \
		current="$${current:-$(GO_TAG_PREFIX)0.0.0}"; \
		version="$${current#$(GO_TAG_PREFIX)}"; \
		IFS=. read -r major minor patch <<< "$$version"; \
		case "$(BUMP)" in \
			patch) ((patch += 1)) ;; \
			minor) ((minor += 1)); patch=0 ;; \
			major) ((major += 1)); minor=0; patch=0 ;; \
			*) echo "BUMP must be patch, minor, or major" >&2; exit 1 ;; \
		esac; \
		tag="$(GO_TAG_PREFIX)$$major.$$minor.$$patch"; \
		if [[ "$(DRY_RUN)" == "1" ]]; then echo "$$tag"; exit 0; fi; \
		test -z "$$(git status --porcelain)" || { echo "release requires a clean worktree" >&2; exit 1; }; \
		upstream="$$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null)" || { echo "release requires an upstream branch" >&2; exit 1; }; \
		test "$$(git rev-parse HEAD)" = "$$(git rev-parse "$$upstream")" || { echo "release requires HEAD to be pushed" >&2; exit 1; }; \
		! git show-ref --verify --quiet "refs/tags/$$tag" || { echo "tag $$tag already exists locally" >&2; exit 1; }; \
		! git ls-remote --exit-code --tags origin "refs/tags/$$tag" >/dev/null 2>&1 || { echo "tag $$tag already exists on origin" >&2; exit 1; }; \
		key="$${SIGNING_KEY:-$$(git config --get user.signingkey || true)}"; \
		test -n "$$key" || { echo "configure user.signingkey or pass SIGNING_KEY=..." >&2; exit 1; }; \
		$(MAKE) --no-print-directory go-quality; \
		cd $(GO_DIR) && GOWORK=off $(GO) test -count=1 ./internal/release ./internal/compatibility ./internal/workflowaudit; \
		cd ..; \
		git tag -s -u "$$key" "$$tag" -m "Golem Go v$$major.$$minor.$$patch"; \
		git push origin "refs/tags/$$tag"; \
		echo "Released $$tag; protected hosted verification is now running."
