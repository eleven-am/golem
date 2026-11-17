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
	./runtime ./internal/generate/pipeline ./internal/semantic/runtime ./internal/fulltext/runtime ./internal/provider/postgresql
	./internal/read/decode ./internal/read/sql ./internal/p7oracle ./internal/policy/oracle ./provider/postgresql
	./internal/event/outbox ./internal/queue/worker ./internal/mutation/batch ./internal/testenv ./compatibility
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
RACE_SKIP_TESTS="^(TestBatchChunkMagnitudeAndPerParentPagingSQLite|Test.*FixtureRegeneratesByteIdentically|TestP5GeneratedExtensionFixtureBuildsFromCleanSourceThenRepeatsIdentically|TestP6GeneratedArtifactsAreByteIdenticalAcrossShuffleAndRepeat)$"
RACE_SKIP_COUNT=10
GATE_PACKAGES=(./internal/migration/workflow ./internal/physical ./observe)
GATE_GOLDEN_TEST="TestInspectSocialGoldenAndDeterminism"
GATE_IDENTITY_TESTS="TestP5GeneratedExtensionFixtureRegeneratesByteIdentically|TestP5GeneratedSocialFixtureRegeneratesByteIdentically|TestP6GeneratedMetricsFixtureRegeneratesByteIdentically|TestP6GeneratedArtifactsAreByteIdenticalAcrossShuffleAndRepeat"

WRITER4_RUNTIME_TESTS='^Test(MutationHistoryWriter|MutationHistoryPostgreSQL|PolicyFactRuntime|PolicyFactSQLite|EventHistory(CurrentGuard|Scope)|HistoricalFactDependency|EventPolicyHistory(Delivery|Intermediate|OrdinaryPrefixLoss|NeverUsesLegacyLiveRouting))'
WRITER4_GENERATED_TESTS='^TestFreshGenerated(FramesFactsReadAndWriteEquivalence|RelatedFactTemplateUsesPhysicalTruth|ScopeTemplatePreservesActorDomain|FactTemplatePreservesActorDomain)$'
WRITER4_CLIENT_TESTS='^TestFreshGenerated(ContinuationPagesHTTP|CustomInvalidationHTTP|RestoreHTTP)$'

go_test() {
	(cd "$GO_DIR" && "$GO" test "$@")
}

go_test_serial() {
	go_test -p=1 -count=1 -timeout=30m "$@"
}

require_local_nats() {
	local endpoint="${DOCKER_HOST:-unix:///var/run/docker.sock}" socket
	case "$endpoint" in
	unix:///*) socket="${endpoint#unix://}" ;;
	*)
		echo "Core NATS fixtures require a local Unix Docker endpoint" >&2
		return 1
		;;
	esac
	[ -S "$socket" ] || {
		echo "Core NATS local Docker socket is unavailable; configure DOCKER_HOST" >&2
		return 1
	}
	export DOCKER_HOST="$endpoint"
	unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
	# Tests inspect the pinned image through the bounded Go Docker owner.
	# Fetching is explicit (cache-nats), never part of ordinary verification.
	export GOLEM_P8_REQUIRE_NATS=1
}

# This subshell owns only the cluster it creates. Explicit caller-owned clusters
# retain their lifecycle; fixture guards still verify the socket and data root.
with_private_postgresql() (
	local private_root="${GOLEM_POLICY_HISTORY_PG_ROOT:-}" private_dsn="${GOLEM_POLICY_HISTORY_PG_DSN:-}"
	local pg_bin="${PG_BINDIR:-}" pg_version pg_major owned_root executable
	if [ -n "$private_root" ] || [ -n "$private_dsn" ]; then
		if [ -z "$private_root" ] || [ -z "$private_dsn" ]; then
			echo "private PostgreSQL requires both its explicit root and DSN" >&2
			return 1
		fi
		"$@"
		return $?
	fi
	if [ -z "$pg_bin" ]; then
		command -v pg_config >/dev/null 2>&1 || {
			echo "private PostgreSQL needs PG_BINDIR or pg_config (PostgreSQL 15+)" >&2
			return 1
		}
		pg_bin="$(pg_config --bindir)" || return 1
	fi
	for executable in initdb pg_ctl postgres; do
		[ -x "$pg_bin/$executable" ] || {
			echo "private PostgreSQL binaries are unavailable; configure PG_BINDIR" >&2
			return 1
		}
	done
	pg_version="$("$pg_bin/postgres" --version)" || return 1
	pg_major="$(printf '%s\n' "$pg_version" | sed -n 's/^postgres (PostgreSQL) \([0-9][0-9]*\).*/\1/p')"
	if [ -z "$pg_major" ] || [ "$pg_major" -lt 15 ]; then
		echo "private PostgreSQL requires existing PostgreSQL 15+ binaries" >&2
		return 1
	fi
	# Short path avoids Unix-socket path limits on both Linux and macOS.
	owned_root="$(mktemp -d /tmp/golem-pg.XXXXXXXX)" || return 1
	cleanup_private_postgresql() {
		local result=$?
		trap - EXIT INT TERM
		if [ -f "$owned_root/data/postmaster.pid" ]; then
			if ! "$pg_bin/pg_ctl" -D "$owned_root/data" -m immediate -w -t 30 stop >>"$owned_root/server.log" 2>&1; then
				echo "private PostgreSQL shutdown failed; retained owned root: $owned_root" >&2
				exit 1
			fi
		fi
		if ! rm -rf -- "$owned_root"; then
			echo "private PostgreSQL cleanup failed: $owned_root" >&2
			exit 1
		fi
		exit "$result"
	}
	trap cleanup_private_postgresql EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	chmod 700 "$owned_root" || return 1
	mkdir "$owned_root/socket" || return 1
	if ! "$pg_bin/initdb" -D "$owned_root/data" -U golem --encoding=UTF8 --locale=C --auth-local=trust --auth-host=reject >"$owned_root/init.log" 2>&1; then
		echo "private PostgreSQL initialization failed (existing binaries and non-root OS user required)" >&2
		return 1
	fi
	# Keep normal fsync/full_page_writes; constrain this adoption-fixture cluster.
	cat >>"$owned_root/data/postgresql.conf" <<PGCONF || return 1
listen_addresses = ''
unix_socket_directories = '$owned_root/socket'
port = 55439
max_connections = 24
shared_buffers = '32MB'
work_mem = '2MB'
maintenance_work_mem = '16MB'
max_parallel_workers = 0
PGCONF
	if ! "$pg_bin/pg_ctl" -D "$owned_root/data" -l "$owned_root/server.log" -w -t 30 start >>"$owned_root/server.log" 2>&1; then
		echo "private PostgreSQL startup failed" >&2
		return 1
	fi
	export GOLEM_POLICY_HISTORY_PG_ROOT="$owned_root"
	export GOLEM_POLICY_HISTORY_PG_DSN="host=$owned_root/socket port=55439 user=golem dbname=postgres sslmode=disable"
	"$@"
)

require_matching() {
	local want="$1" filter="$2" package="$3" matched
	matched="$(cd "$GO_DIR" && "$GO" test -list "$filter" "$package" | grep -c '^Test')"
	if [ "$matched" != "$want" ]; then
		echo "filter $filter matched $matched tests in $package; want $want" >&2
		return 1
	fi
}

go_test_matching() {
	require_matching "$1" "$2" "$3" || return 1
	go_test -count=1 -run "$2" "$3"
}

# Mandatory staged passes must execute rather than succeed through t.Skip.
# Keep ordinary V3 tests and their intentional optional gates unchanged.
go_test_required() (
	local want="$1" filter="$2" package="$3" output passed result=0
	require_matching "$want" "$filter" "$package" || return 1
	output="$(mktemp /tmp/golem-required-tests.XXXXXXXX)" || return 1
	trap 'rm -f -- "$output"' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	go_test_serial -v -run "$filter" "$package" 2>&1 | tee "$output" || result=$?
	[ "$result" -eq 0 ] || return "$result"
	if grep -Eq '^[[:space:]]*--- SKIP:' "$output"; then
		echo "required staged acceptance skipped a test/subtest: $filter" >&2
		return 1
	fi
	passed="$(grep -Ec '^--- PASS: Test[^[:space:]]+ \(' "$output")" || true
	[ "$passed" = "$want" ] || {
		echo "required staged acceptance completed $passed top-level tests; want $want" >&2
		return 1
	}
)

# One scoped compile-only overlay serves runtime and generated subprocesses.
# The checked-in release writer remains V3; GOFLAGS preserves caller flags and
# reaches the generated harness's child go test -c invocations.
with_writer4() (
	local source="$GO_DIR/runtime/mutation_history_finalize.go" owned_root source_json writer_json matches caller_flags
	local old='const mutationHistoryWriterVersion = mutationfact.FormatVersion'
	local next='const mutationHistoryWriterVersion = mutationfact.HistoryFactVersion'
	if [ "${GOFLAGS+x}" = x ]; then
		caller_flags="$GOFLAGS"
	else
		caller_flags="$(cd "$GO_DIR" && "$GO" env GOFLAGS)" || return 1
	fi
	case "$caller_flags" in *-overlay*)
		echo "writer4 rehearsal cannot compose an existing GOFLAGS overlay" >&2
		return 1 ;;
	esac
	matches="$(grep -Fxc "$old" "$source")" || true
	[ "$matches" = 1 ] || { echo "expected exactly one default V3 writer constant" >&2; return 1; }
	owned_root="$(mktemp -d /tmp/golem-writer4.XXXXXXXX)" || return 1
	trap 'rm -rf -- "$owned_root"' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	awk -v old="$old" -v next_line="$next" '$0 == old { $0 = next_line } { print }' "$source" >"$owned_root/writer4.go" || return 1
	# JSON path quoting without another runtime/tool dependency. Temp paths are
	# fixed-format; reject control characters in the caller's checkout path.
	if LC_ALL=C printf '%s' "$source" | LC_ALL=C grep -q '[[:cntrl:]]'; then
		echo "writer4 overlay requires a checkout path without control characters" >&2
		return 1
	fi
	source_json="${source//\\/\\\\}"
	source_json="${source_json//\"/\\\"}"
	writer_json="$owned_root/writer4.go"
	printf '{"Replace":{"%s":"%s"' "$source_json" "$writer_json" >"$owned_root/overlay.json" || return 1
	# go test compiles runtime_test even when all selected cases are internal.
	# List the real package before exporting the overlay; never change its
	# import path or the unmodified default-V3 verification pass.
	local external_files file declaration_status retain_external=0 index=0 stub
	external_files="$(cd "$GO_DIR" && "$GO" list -f '{{range .XTestGoFiles}}{{println .}}{{end}}' ./runtime)" || return 1
	while IFS= read -r file; do
		[ -n "$file" ] || continue
		file="$GO_DIR/runtime/$file"
		[ -r "$file" ] || { echo "cannot read runtime external test: $file" >&2; return 1; }
		# Match name tokens conservatively (including comments), so a future
		# selected external declaration is never erased. Retain the WHOLE
		# external package in that case: its helpers may span other files.
		declaration_status=0
		grep -Eo 'Test[[:alnum:]_]+' "$file" >"$owned_root/test-names" || declaration_status=$?
		[ "$declaration_status" -le 1 ] || return "$declaration_status"
		declaration_status=0
		grep -Eq "$WRITER4_RUNTIME_TESTS|^TestMain$" "$owned_root/test-names" || declaration_status=$?
		case "$declaration_status" in 0) retain_external=1 ;; 1) ;; *) return "$declaration_status" ;; esac
		# External init hooks could affect internal cases through shared state.
		declaration_status=0
		grep -Eq '^[[:space:]]*func[[:space:]]+init[[:space:]]*\(' "$file" || declaration_status=$?
		case "$declaration_status" in 0) retain_external=1 ;; 1) ;; *) return "$declaration_status" ;; esac
	done <<<"$external_files"
	if [ "$retain_external" -eq 0 ]; then
		while IFS= read -r file; do
			[ -n "$file" ] || continue
			index=$((index + 1))
			stub="$owned_root/runtime-external-$index.go"
			printf 'package runtime_test\n' >"$stub" || return 1
			source_json="$GO_DIR/runtime/$file"
			if LC_ALL=C printf '%s' "$source_json" | LC_ALL=C grep -q '[[:cntrl:]]'; then
				echo "external test overlay path contains control characters" >&2; return 1
			fi
			source_json="${source_json//\\/\\\\}"
			source_json="${source_json//\"/\\\"}"
			printf ',"%s":"%s"' "$source_json" "$stub" >>"$owned_root/overlay.json" || return 1
		done <<<"$external_files"
		printf 'writer4 overlay: stubbed %d unselected runtime external test files\n' "$index"
	else
		echo 'writer4 overlay: retaining runtime external package for selected tests or initialization hooks'
	fi
	printf '}}\n' >>"$owned_root/overlay.json" || return 1
	export GOFLAGS="${caller_flags:+$caller_flags }-overlay=$owned_root/overlay.json"
	export GOLEM_POLICY_HISTORY_WRITER_REHEARSAL=4 GOLEM_STAGED_FACT_WRITER=4
	"$@"
)

writer4_database_acceptance() {
	go_test_required 47 "$WRITER4_RUNTIME_TESTS" ./runtime || return 1
	go_test_required 4 "$WRITER4_GENERATED_TESTS" ./internal/generate/pipeline
}

database_bound_acceptance() {
	go_test_serial "${DATABASE_PACKAGES[@]}" || return 1
	with_writer4 writer4_database_acceptance
}

writer4_client_acceptance() (
	command -v node >/dev/null 2>&1 || { echo "generated client acceptance requires Node" >&2; return 1; }
	[ -x "$ROOT/client/node_modules/.bin/vitest" ] || {
		echo "generated client acceptance requires the installed locked client dependencies (npm ci in client)" >&2
		return 1
	}
	export GOLEM_GENERATED_CLIENT_ROOT="$ROOT/client"
	with_private_postgresql with_writer4 go_test_required 3 "$WRITER4_CLIENT_TESTS" ./internal/generate/pipeline
)

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
		case "$directory" in "$GO_DIR/examples/"*) continue ;; esac
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
	local unformatted latest install notes="$ROOT/docs/golem-go/RELEASE-NOTES.md"
	unformatted="$(cd "$GO_DIR" && find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)" || return 1
	[ -z "$unformatted" ] || {
		printf 'gofmt would rewrite:\n%s\n' "$unformatted" >&2
		return 1
	}
	latest="$(awk '/^## go\/v[0-9]+\.[0-9]+\.[0-9]+$/ { version=$2; sub(/^go\/v/, "", version); split(version, part, "."); if (!seen || part[1] > major || (part[1] == major && part[2] > minor) || (part[1] == major && part[2] == minor && part[3] > patch)) { major=part[1]; minor=part[2]; patch=part[3]; seen=1 } } END { if (seen) printf "v%d.%d.%d\n", major, minor, patch }' "$notes")" || return 1
	install="$(sed -n 's/^go get github\.com\/eleven-am\/golem\/go@\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$/\1/p' "$notes" | head -n 1)" || return 1
	[ -n "$latest" ] && [ "$install" = "$latest" ] || {
		printf 'release-note install version %s differs from newest section %s\n' "$install" "$latest" >&2
		return 1
	}
}

gate() {
	local work="$GO_DIR/bin/gate.work" social="$GO_DIR/examples/social" kernel="$GO_DIR/examples/kernel"
	make -C "$ROOT" --no-print-directory GO="$GO" go-build || return 1
	printf 'go 1.26.0\n\nuse (\n\t%s\n\t%s\n)\n\nreplace github.com/eleven-am/golem/go v0.0.0 => %s\n' \
		"$GO_DIR" "$social" "$GO_DIR" >"$work" || return 1
	(cd "$social" && GOWORK="$work" "$GO_DIR/bin/golem" check \
		--schema ./social --app-out ./social --migrations migrations) || return 1
	(cd "$social" && GOWORK="$work" "$GO" vet ./...) || return 1
	(cd "$social" && GOWORK="$work" "$GO" test -p=1 -count=1 -timeout=30m ./...) || return 1
	(cd "$kernel" && "$GO_DIR/bin/golem" check \
		--schema ./kernel --app-out ./kernel --migrations migrations) || return 1
	(cd "$kernel" && "$GO" vet ./...) || return 1
	(cd "$kernel" && "$GO" test -p=1 -count=1 -timeout=30m ./...) || return 1
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
	cli-and-harness) go_test -p=1 -count=1 -timeout=90m "${CLI_PACKAGES[@]}" ;;
	database-bound)
		require_local_nats || return 1
		with_private_postgresql database_bound_acceptance
		;;
	writer4-database) with_private_postgresql with_writer4 writer4_database_acceptance ;;
	writer4-client) writer4_client_acceptance ;;
	oracle-mutation-and-load) go_test_serial "${ORACLE_MUTATION_AND_LOAD[@]}" ;;
	oracle-failure-and-event)
		require_local_nats || return 1
		go_test_serial "${ORACLE_FAILURE_AND_EVENT[@]}"
		;;
	oracle-read-and-diagnostics) go_test_serial "${ORACLE_READ_AND_DIAGNOSTICS[@]}" ;;
	race)
		require_matching "$RACE_SKIP_COUNT" "$RACE_SKIP_TESTS" ./runtime || return 1
		with_private_postgresql go_test -race -p=1 -parallel 4 -count=1 -timeout=45m -skip "$RACE_SKIP_TESTS" "${RACE_PACKAGES[@]}"
		;;
	cache-nats)
		# Network/cache preparation only; CI bounds this separate step. Keep
		# the pinned image here, rather than duplicating it in workflow YAML.
		require_local_nats || return 1
		docker --host "$DOCKER_HOST" pull "$NATS_IMAGE"
		;;
	vulncheck) (cd "$GO_DIR" && "$GO" run "$GOVULNCHECK" ./...) ;;
	*)
		echo "unknown step: $1 (steps: ${STEPS[*]}; explicit actions: cache-nats writer4-database writer4-client)" >&2
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
