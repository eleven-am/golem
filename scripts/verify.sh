#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$ROOT/go"
GO="${GO:-go}"
LOG_DIR="${VERIFY_LOG_DIR:-$ROOT/go/bin/verify}"
SCOPE="${1:-full}"

mkdir -p "$LOG_DIR"
rm -f "$LOG_DIR"/*.log

STEP_NAMES=()
STEP_STATUS=()
STEP_SECONDS=()
FAILED=0

colour() {
	if [ -t 1 ]; then printf '\033[%sm%s\033[0m' "$1" "$2"; else printf '%s' "$2"; fi
}

run_step() {
	local name="$1"
	shift
	local started=$SECONDS
	printf '  %-34s ' "$name"
	if "$@" >"$LOG_DIR/$name.log" 2>&1; then
		local elapsed=$((SECONDS - started))
		printf '%s  %ds\n' "$(colour '32' 'pass')" "$elapsed"
		STEP_STATUS+=("pass")
		STEP_SECONDS+=("$elapsed")
	else
		local elapsed=$((SECONDS - started))
		printf '%s  %ds\n' "$(colour '31' 'FAIL')" "$elapsed"
		STEP_STATUS+=("fail")
		STEP_SECONDS+=("$elapsed")
		FAILED=$((FAILED + 1))
	fi
	STEP_NAMES+=("$name")
}

go_test() {
	cd "$GO_DIR" && GOWORK=off "$GO" test "$@"
}

database_free_packages() {
	cd "$GO_DIR" && GOWORK=off "$GO" list ./... |
		grep -vxF -f <(GOWORK=off "$GO" list \
			./cmd/golem ./golemtest ./internal/generate/pipeline ./internal/p7oracle ./internal/p8oracle/... \
			./internal/policy/oracle ./internal/provider/postgresql ./internal/read/decode ./internal/semantic/runtime \
			./provider/postgresql ./runtime)
}

step_database_free() {
	local packages
	packages="$(database_free_packages)" || return 1
	# shellcheck disable=SC2086
	go_test -count=1 -timeout=30m $packages
}

printf '\n%s\n' "$(colour '1' "golem verify — scope: $SCOPE")"
printf '  logs: %s\n\n' "$LOG_DIR"

run_step "format" bash -c "cd '$GO_DIR' && test -z \"\$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)\""
run_step "vet" bash -c "cd '$GO_DIR' && GOWORK=off $GO vet ./..."
run_step "gate-drift-and-identity" make -C "$ROOT" gate
run_step "database-free-packages" step_database_free
run_step "cli-and-harness" go_test -p=1 -count=1 -timeout=30m ./cmd/golem ./golemtest
run_step "database-bound-packages" go_test -p=1 -count=1 -timeout=30m \
	./internal/generate/pipeline ./internal/provider/postgresql ./internal/read/decode \
	./internal/semantic/runtime ./runtime ./provider/postgresql
run_step "isolating-oracles" go_test -count=1 -timeout=30m \
	./internal/p7oracle ./internal/policy/oracle \
	./internal/p8oracle ./internal/p8oracle/analytics ./internal/p8oracle/diagnostic \
	./internal/p8oracle/disclosure ./internal/p8oracle/event ./internal/p8oracle/failure \
	./internal/p8oracle/load ./internal/p8oracle/mutation ./internal/p8oracle/rejection
run_step "nats-oracle" env GOLEM_P8_REQUIRE_NATS=1 GOLEM_P8_REQUIRE_POSTGRESQL=1 \
	bash -c "cd '$GO_DIR' && GOWORK=off $GO test -p=1 -count=1 -timeout=30m ./internal/p8oracle/natslive"

if [ "$SCOPE" = "full" ]; then
	run_step "race" go_test -race -p=1 -count=1 -timeout=45m \
		./events/... ./provider/... ./runtime ./queue \
		./internal/event/outbox ./internal/event/cdc \
		./internal/queue/worker ./internal/subscription
	run_step "documented-commands" go_test -p=1 -count=1 -timeout=20m ./cmd/golem \
		-run "^TestQuickstartFromEmptyDirectory$"
fi

printf '\n%s\n' "$(colour '1' 'summary')"
index=0
while [ "$index" -lt "${#STEP_NAMES[@]}" ]; do
	name="${STEP_NAMES[$index]}"
	status="${STEP_STATUS[$index]}"
	if [ "$status" = "pass" ]; then
		printf '  %s  %-34s %ds\n' "$(colour '32' '✓')" "$name" "${STEP_SECONDS[$index]}"
	else
		printf '  %s  %-34s %ds\n' "$(colour '31' '✗')" "$name" "${STEP_SECONDS[$index]}"
	fi
	index=$((index + 1))
done

if [ "$FAILED" -eq 0 ]; then
	printf '\n%s\n\n' "$(colour '32' 'every step passed — this tree is releasable from this platform')"
	exit 0
fi

printf '\n%s\n' "$(colour '31' "$FAILED step(s) failed — every failure is listed below")"
index=0
while [ "$index" -lt "${#STEP_NAMES[@]}" ]; do
	if [ "${STEP_STATUS[$index]}" = "fail" ]; then
		name="${STEP_NAMES[$index]}"
		printf '\n%s\n' "$(colour '1' "── $name")"
		grep -E '^(--- FAIL|FAIL|panic:|# )' "$LOG_DIR/$name.log" | head -25
		printf '  full log: %s\n' "$LOG_DIR/$name.log"
	fi
	index=$((index + 1))
done
printf '\n'
exit 1
