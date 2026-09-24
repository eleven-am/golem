#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$ROOT/go"
GO="${GO:-go}"
LOG_DIR="${VERIFY_LOG_DIR:-$GO_DIR/bin/verify}"
GOVULNCHECK="golang.org/x/vuln/cmd/govulncheck@v1.7.0"
NATS_IMAGE="nats:2.14.4@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"

export GOWORK=off GOLEM_P8_REQUIRE_POSTGRESQL=1 GOLEM_REQUIRE_PGVECTOR=1

STEPS=(
	format vet gate database-free cli-and-harness database-bound
	oracle-mutation-and-load oracle-failure-and-event oracle-read-and-diagnostics race
)

CLI_PACKAGES=(./cmd/golem ./golemtest)
DATABASE_PACKAGES=(
	./runtime ./internal/generate/pipeline ./internal/semantic/runtime ./internal/provider/postgresql
	./internal/read/decode ./internal/p7oracle ./internal/policy/oracle ./provider/postgresql
	./internal/event/outbox
)
ORACLE_MUTATION_AND_LOAD=(./internal/p8oracle/mutation ./internal/p8oracle/load ./internal/p8oracle/analytics)
ORACLE_FAILURE_AND_EVENT=(./internal/p8oracle/failure ./internal/p8oracle/event ./internal/p8oracle/natslive)
ORACLE_READ_AND_DIAGNOSTICS=(
	./internal/p8oracle ./internal/p8oracle/disclosure ./internal/p8oracle/rejection ./internal/p8oracle/diagnostic
)
RACE_PACKAGES=(
	./events/... ./provider/... ./runtime ./queue
	./internal/event/outbox ./internal/event/cdc ./internal/queue/worker ./internal/subscription
)
GATE_PACKAGES=(./internal/migration/workflow ./internal/physical ./observe)
GATE_GOLDEN_TEST="TestInspectSocialGoldenAndDeterminism"
GATE_IDENTITY_TESTS="TestP5GeneratedExtensionFixtureRegeneratesByteIdentically|TestP5GeneratedSocialFixtureRegeneratesByteIdentically|TestP6GeneratedMetricsFixtureRegeneratesByteIdentically|TestP6GeneratedArtifactsAreByteIdenticalAcrossShuffleAndRepeat"

go_test() {
	(cd "$GO_DIR" && "$GO" test "$@")
}

go_test_serial() {
	go_test -p=1 -count=1 -timeout=30m "$@"
}

go_test_matching() {
	local want="$1" filter="$2" package="$3" matched
	matched="$(cd "$GO_DIR" && "$GO" test -list "$filter" "$package" | grep -c '^Test')"
	if [ "$matched" != "$want" ]; then
		echo "filter $filter matched $matched tests in $package; want $want" >&2
		return 1
	fi
	go_test -count=1 -run "$filter" "$package"
}

database_bound_packages() {
	(cd "$GO_DIR" && "$GO" list "${CLI_PACKAGES[@]}" "${DATABASE_PACKAGES[@]}" \
		"${ORACLE_MUTATION_AND_LOAD[@]}" "${ORACLE_FAILURE_AND_EVENT[@]}" "${ORACLE_READ_AND_DIAGNOSTICS[@]}")
}

check_database_tier() {
	local bound declared file directory candidate covered status=0
	bound="$(database_bound_packages)" || return 1
	declared="$(cd "$GO_DIR" && "$GO" list -f '{{.Dir}}' $bound)" || return 1
	while IFS= read -r file; do
		directory="$(cd "$(dirname "$file")" && pwd)"
		case "$directory" in "$GO_DIR/examples/"* | "$GO_DIR/internal/testenv") continue ;; esac
		covered=0
		for candidate in $declared; do
			case "$directory" in "$candidate" | "$candidate"/*) covered=1 ;; esac
		done
		if [ "$covered" -eq 0 ]; then
			echo "undeclared database package: $file" >&2
			status=1
		fi
	done < <(grep -rl --include='*.go' \
		-e GOLEM_TEST_POSTGRES_DSN -e GOLEM_TEST_POSTGRES_LINGUISTIC_DSN -e GOLEM_TEST_PGVECTOR_DSN \
		-e '/internal/testenv"' "$GO_DIR")
	return "$status"
}

format() {
	local unformatted
	unformatted="$(cd "$GO_DIR" && find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)" || return 1
	[ -z "$unformatted" ] || {
		printf 'gofmt would rewrite:\n%s\n' "$unformatted" >&2
		return 1
	}
}

gate() {
	local work="$GO_DIR/bin/gate.work" social="$GO_DIR/examples/social"
	make -C "$ROOT" --no-print-directory GO="$GO" go-build || return 1
	printf 'go 1.25.0\n\nuse (\n\t%s\n\t%s\n)\n\nreplace github.com/eleven-am/golem/go v0.0.0 => %s\n' \
		"$GO_DIR" "$social" "$GO_DIR" >"$work" || return 1
	(cd "$social" && GOWORK="$work" "$GO_DIR/bin/golem" check \
		--schema ./social --app-out ./social --migrations migrations) || return 1
	(cd "$social" && GOWORK="$work" "$GO" vet ./...) || return 1
	(cd "$social" && GOWORK="$work" "$GO" test -p=1 -count=1 -timeout=30m ./...) || return 1
	go_test_matching 1 "$GATE_GOLDEN_TEST" ./cmd/golem || return 1
	go_test_matching 4 "$GATE_IDENTITY_TESTS" ./runtime || return 1
	go_test -count=1 "${GATE_PACKAGES[@]}"
}

database_free() {
	local bound packages
	check_database_tier || return 1
	bound="$(database_bound_packages)" || return 1
	packages="$(cd "$GO_DIR" && "$GO" list ./... | grep -vxF -f <(printf '%s\n' "$bound"))" || return 1
	go_test -count=1 -timeout=30m $packages
}

run() {
	case "$1" in
	format) format ;;
	vet) (cd "$GO_DIR" && "$GO" vet ./...) ;;
	gate) gate ;;
	database-free) database_free ;;
	cli-and-harness) go_test_serial "${CLI_PACKAGES[@]}" ;;
	database-bound) go_test_serial "${DATABASE_PACKAGES[@]}" ;;
	oracle-mutation-and-load) go_test_serial "${ORACLE_MUTATION_AND_LOAD[@]}" ;;
	oracle-failure-and-event)
		docker image inspect "$NATS_IMAGE" >/dev/null 2>&1 || docker pull "$NATS_IMAGE" || return 1
		export GOLEM_P8_REQUIRE_NATS=1
		go_test_serial "${ORACLE_FAILURE_AND_EVENT[@]}"
		;;
	oracle-read-and-diagnostics) go_test_serial "${ORACLE_READ_AND_DIAGNOSTICS[@]}" ;;
	race) go_test -race -p=1 -parallel 4 -count=1 -timeout=45m "${RACE_PACKAGES[@]}" ;;
	vulncheck) (cd "$GO_DIR" && "$GO" run "$GOVULNCHECK" ./...) ;;
	*)
		echo "unknown step: $1 (steps: ${STEPS[*]})" >&2
		return 2
		;;
	esac
}

if [ "${1:-}" = list ]; then
	printf '%s\n' "${STEPS[@]}"
	exit 0
fi
[ "$#" -gt 0 ] || set -- "${STEPS[@]}"

mkdir -p "$LOG_DIR"
rm -f "$LOG_DIR"/*.log
failed=""
for name in "$@"; do
	printf '\n── %s\n' "$name"
	(run "$name") 2>&1 | tee "$LOG_DIR/$name.log"
	[ "${PIPESTATUS[0]}" -eq 0 ] || failed="$failed $name"
done

if [ -z "$failed" ]; then
	printf '\npassed: %s\n' "$*"
	exit 0
fi
printf '\nfailed:%s\nlogs: %s\n' "$failed" "$LOG_DIR"
exit 1
