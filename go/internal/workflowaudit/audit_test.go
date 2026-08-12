package workflowaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestP8WorkflowContainsRequiredHostedGates(t *testing.T) {
	contents := readHostedWorkflow(t)
	if violations := AuditWorkflow(contents); len(violations) != 0 {
		t.Fatalf("hosted workflow violations=%v", violationCodes(violations))
	}
	inventory := readRequiredInventory(t)
	if violations := AuditRequiredTestInventory(inventory); len(violations) != 0 {
		t.Fatalf("required-test inventory violations=%v", violationCodes(violations))
	}
}

func TestP8RequiredProfileInventoryDeletionAndRenameAreDetected(t *testing.T) {
	original := string(readRequiredInventory(t))
	for _, identity := range []string{
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/linguistic",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestQueryPlanPostgreSQLLiveBoundPlanningWithoutExecution/linguistic",
		"github.com/eleven-am/golem/go/golemtest:TestOptimisticConcurrencySQLiteAndPostgreSQLExternalGeneratedApplication",
		"github.com/eleven-am/golem/go/cmd/golem:TestP8ExecutableGoV002ToV010MigrationGuide",
	} {
		for _, replacement := range []string{"", identity + "Renamed"} {
			mutant := strings.ReplaceAll(original, identity, replacement)
			if mutant == original {
				t.Fatal("required identity mutation was a no-op")
			}
			if codes := violationCodes(AuditRequiredTestInventory([]byte(mutant))); !contains(codes, "P8_WORKFLOW_REQUIRED_TEST_MISSING") {
				t.Fatalf("required identity mutation survived: %v", codes)
			}
		}
	}
}

func TestP8ExecutableMigrationGuideWorkflowIdentityUsesOnlyCoveredMandatoryJobs(t *testing.T) {
	var inventory RequiredTestInventory
	if err := json.Unmarshal(readRequiredInventory(t), &inventory); err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{"toolchain": true, "hardening": true}
	for setName, set := range inventory.Sets {
		count := 0
		for _, identity := range set.Required {
			if identity == executableMigrationGuideWorkflowIdentity {
				count++
			}
		}
		want := 0
		if expected[setName] {
			want = 1
		}
		if count != want {
			t.Fatalf("%s migration-guide identity count=%d want=%d", setName, count, want)
		}
		if contains(set.AllowedSkips, executableMigrationGuideWorkflowIdentity) {
			t.Fatalf("%s permits migration guide skip", setName)
		}
	}
	for setName := range expected {
		if _, ok := inventory.Sets[setName]; !ok {
			t.Fatalf("migration guide set %s absent", setName)
		}
	}
}

func TestP8ExternalOptimisticConcurrencyWorkflowIdentityUsesExistingMandatoryJobs(t *testing.T) {
	var inventory RequiredTestInventory
	if err := json.Unmarshal(readRequiredInventory(t), &inventory); err != nil {
		t.Fatal(err)
	}
	identity := "github.com/eleven-am/golem/go/golemtest:TestOptimisticConcurrencySQLiteAndPostgreSQLExternalGeneratedApplication"
	expectedSets := map[string]bool{"toolchain": true, "hardening": true}
	for setName, set := range inventory.Sets {
		count := 0
		for _, required := range set.Required {
			if required == identity {
				count++
			}
		}
		want := 0
		if expectedSets[setName] {
			want = 1
		}
		if count != want {
			t.Fatalf("%s external optimistic-concurrency identity count=%d; want %d", setName, count, want)
		}
		if contains(set.AllowedSkips, identity) {
			t.Fatalf("%s permits the external optimistic-concurrency gate to skip", setName)
		}
	}
	for setName := range expectedSets {
		if _, ok := inventory.Sets[setName]; !ok {
			t.Fatalf("external optimistic-concurrency evidence set %s is absent", setName)
		}
	}
}

func TestP8ExternalLiveNATSIdentitiesUseEveryExecutingMandatoryJob(t *testing.T) {
	var inventory RequiredTestInventory
	if err := json.Unmarshal(readRequiredInventory(t), &inventory); err != nil {
		t.Fatal(err)
	}
	expectedSets := map[string]bool{"toolchain": true, "provider": true, "hardening": true}
	for _, identity := range externalNATSWorkflowIdentities {
		for setName, set := range inventory.Sets {
			count := 0
			for _, required := range set.Required {
				if required == identity {
					count++
				}
			}
			want := 0
			if expectedSets[setName] {
				want = 1
			}
			if count != want {
				t.Fatalf("%s live NATS identity %s count=%d want=%d", setName, identity, count, want)
			}
			if contains(set.AllowedSkips, identity) {
				t.Fatalf("%s permits live NATS skip for %s", setName, identity)
			}
		}
	}
}

func TestP8LiveNATSWorkflowBoundaryMutationsAreRejected(t *testing.T) {
	original := string(readHostedWorkflow(t))
	mutations := []struct {
		name, before, after, want string
	}{
		{"mandatory-mode-removed", `GOLEM_P8_REQUIRE_NATS: "1"`, `GOLEM_P8_REQUIRE_NATS: "0"`, "P8_WORKFLOW_REQUIRED_NATS_MODE_MISSING"},
		{"fixed-name-preflight-removed", liveNATSAbsenceWorkflow, "if test -n \"container check removed\"; then\n            exit 1\n          fi", "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE"},
		{"pinned-image-replaced", liveNATSImage, `nats:2.14.4@sha256:0000000000000000000000000000000000000000000000000000000000000000`, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE"},
		{"repo-digest-replaced", liveNATSRepoDigest, `nats@sha256:0000000000000000000000000000000000000000000000000000000000000000`, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE"},
		{"cleanup-owner-check-removed", `test -n "${owner}"`, `test -z "${owner}"`, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE"},
		{"cleanup-image-check-removed", `test "${image}" = "` + liveNATSImage + `"`, `test "${image}" != ""`, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE"},
		{"cleanup-container-renamed", "docker container rm --force " + liveNATSContainerName, "docker container rm --force foreign-nats", "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE"},
	}
	for _, identity := range externalNATSWorkflowIdentities {
		mutations = append(mutations, struct {
			name, before, after, want string
		}{"identity-removed-" + strings.ReplaceAll(identity, "/", "_"), "-require-test " + identity, "-require-test removed:TestLiveNATS", "P8_WORKFLOW_LIVE_NATS_IDENTITY_MISSING_TOOLCHAIN_SUITE"})
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if !strings.Contains(original, mutation.before) {
				t.Fatalf("mutation target %q is absent", mutation.before)
			}
			mutant := strings.ReplaceAll(original, mutation.before, mutation.after)
			if codes := violationCodes(AuditWorkflow([]byte(mutant))); !contains(codes, mutation.want) {
				t.Fatalf("live NATS workflow mutation survived: violations=%v want=%s", codes, mutation.want)
			}
		})
	}
	cleanupOrderBefore := `            test -n "${owner}"
            test "${image}" = "` + liveNATSImage + `"
            docker container rm --force ` + liveNATSContainerName
	cleanupOrderAfter := `            docker container rm --force ` + liveNATSContainerName + `
            test -n "${owner}"
            test "${image}" = "` + liveNATSImage + `"`
	cleanupReordered := strings.ReplaceAll(original, cleanupOrderBefore, cleanupOrderAfter)
	if cleanupReordered == original {
		t.Fatal("live NATS cleanup ordering mutation was a no-op")
	}
	if codes := violationCodes(AuditWorkflow([]byte(cleanupReordered))); !contains(codes, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE") {
		t.Fatalf("live NATS cleanup ordering mutation survived: %v", codes)
	}
	for _, mutation := range []struct{ name, before, after, want string }{
		{"toolchain-execution-removed", `go test -json -p=1 -count=1 -timeout=45m ./...`, `go test -json -p=1 -count=1 -timeout=45m ./cmd/...`, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE"},
		{"provider-execution-removed", `./internal/provider/... ./internal/p8oracle/... |`, `./internal/provider/... ./internal/p8oracle/load |`, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_PROVIDER_MATRIX"},
		{"hardening-execution-removed", `go test -json -race -p=1 -count=1 -timeout=45m ./...`, `go test -json -race -p=1 -count=1 -timeout=45m ./runtime`, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_HARDENING"},
	} {
		mutant := strings.Replace(original, mutation.before, mutation.after, 1)
		if mutant == original {
			t.Fatalf("%s mutation was a no-op", mutation.name)
		}
		if codes := violationCodes(AuditWorkflow([]byte(mutant))); !contains(codes, mutation.want) {
			t.Fatalf("%s survived: violations=%v want=%s", mutation.name, codes, mutation.want)
		}
	}
	relocated := strings.ReplaceAll(original, liveNATSAbsenceWorkflow, "")
	relocated = strings.ReplaceAll(relocated, "          docker image pull "+liveNATSImage, "          "+liveNATSAbsenceWorkflow+"\n          docker image pull "+liveNATSImage)
	if relocated == original {
		t.Fatal("live NATS fixed-name preflight relocation was a no-op")
	}
	if codes := violationCodes(AuditWorkflow([]byte(relocated))); !contains(codes, "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_TOOLCHAIN_SUITE") {
		t.Fatalf("live NATS fixed-name preflight relocation survived: %v", codes)
	}

	for _, jobName := range []string{"toolchain-suite", "provider-matrix", "hardening"} {
		before := "  " + jobName + ":\n"
		after := before + "    env:\n      GOLEM_P8_REQUIRE_NATS: \"0\"\n"
		mutant := strings.Replace(original, before, after, 1)
		if mutant == original {
			t.Fatalf("%s live NATS environment override was a no-op", jobName)
		}
		if codes := violationCodes(AuditWorkflow([]byte(mutant))); !contains(codes, "P8_WORKFLOW_LIVE_NATS_ENV_OVERRIDE") {
			t.Fatalf("%s live NATS override survived: %v", jobName, codes)
		}
	}
}

func TestP8LiveNATSInventoryDeletionAndRenameAreDetected(t *testing.T) {
	original := string(readRequiredInventory(t))
	for _, identity := range externalNATSWorkflowIdentities {
		for _, replacement := range []string{"", identity + "Renamed"} {
			mutant := strings.ReplaceAll(original, identity, replacement)
			if mutant == original {
				t.Fatal("live NATS required identity mutation was a no-op")
			}
			if codes := violationCodes(AuditRequiredTestInventory([]byte(mutant))); !contains(codes, "P8_WORKFLOW_REQUIRED_TEST_MISSING") {
				t.Fatalf("live NATS required identity mutation survived: %v", codes)
			}
		}
	}
}

func TestP8ProviderAndHardeningPGVectorAndNestedOracleSerializationAreExact(t *testing.T) {
	original := string(readHostedWorkflow(t))
	for _, mutation := range []struct {
		name, job, next, before, after, want string
	}{
		{
			name: "provider-pgvector-mode-removed", job: "provider-matrix", next: "hardening",
			before: `GOLEM_REQUIRE_PGVECTOR: "1"`, after: `GOLEM_REQUIRE_PGVECTOR: "0"`,
			want: "P8_WORKFLOW_PROVIDER_PGVECTOR_BOUNDARY_MISSING",
		},
		{
			name: "provider-pgvector-port-changed", job: "provider-matrix", next: "hardening",
			before: `ports: ["55434:5432"]`, after: `ports: ["55435:5432"]`,
			want: "P8_WORKFLOW_PROVIDER_PGVECTOR_BOUNDARY_MISSING",
		},
		{
			name: "provider-nested-oracle-parallelized", job: "provider-matrix", next: "hardening",
			before: `go test -json -p=1 -count=1 -timeout=45m`, after: `go test -json -count=1 -timeout=45m`,
			want: "P8_WORKFLOW_NESTED_ORACLE_SERIALIZATION_MISSING_PROVIDER_MATRIX",
		},
		{
			name: "hardening-pgvector-dsn-removed", job: "hardening", next: "fuzz",
			before: `GOLEM_TEST_PGVECTOR_DSN: ` + pgVectorDSN, after: `GOLEM_TEST_PGVECTOR_DSN: postgresql://postgres@127.0.0.1:55435/golem?sslmode=disable`,
			want: "P8_WORKFLOW_HARDENING_PGVECTOR_BOUNDARY_MISSING",
		},
		{
			name: "hardening-pgvector-image-relabelled", job: "hardening", next: "fuzz",
			before: `image: ` + pgVectorImage, after: `image: pgvector/pgvector@sha256:0000000000000000000000000000000000000000000000000000000000000000`,
			want: "P8_WORKFLOW_HARDENING_PGVECTOR_BOUNDARY_MISSING",
		},
		{
			name: "hardening-shuffle-nested-oracle-parallelized", job: "hardening", next: "fuzz",
			before: `go test -json -p=1 -shuffle=on`, after: `go test -json -shuffle=on`,
			want: "P8_WORKFLOW_NESTED_ORACLE_SERIALIZATION_MISSING_HARDENING",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			mutant := mutateWorkflowJob(t, original, mutation.job, mutation.next, mutation.before, mutation.after)
			if codes := violationCodes(AuditWorkflow([]byte(mutant))); !contains(codes, mutation.want) {
				t.Fatalf("provider/hardening boundary mutation survived: violations=%v want=%s", codes, mutation.want)
			}
		})
	}
}

func mutateWorkflowJob(t *testing.T, original, job, next, before, after string) string {
	t.Helper()
	startMarker := "\n  " + job + ":\n"
	nextMarker := "\n  " + next + ":\n"
	start := strings.Index(original, startMarker)
	if start < 0 {
		t.Fatalf("workflow job %s is absent", job)
	}
	endOffset := strings.Index(original[start+len(startMarker):], nextMarker)
	if endOffset < 0 {
		t.Fatalf("workflow successor %s is absent", next)
	}
	end := start + len(startMarker) + endOffset
	section := original[start:end]
	mutated := strings.Replace(section, before, after, 1)
	if mutated == section {
		t.Fatalf("workflow mutation target %q is absent from %s", before, job)
	}
	return original[:start] + mutated + original[end:]
}

func TestP8WorkflowAuditKillsRequiredProfileSkipAndSupplyChainMutations(t *testing.T) {
	original := string(readHostedWorkflow(t))
	mutations := []struct {
		name, before, after, want string
	}{
		{"minimum-go-downgraded", `1.25.x`, `1.23.x`, "P8_WORKFLOW_GO_MINIMUM_MISSING"},
		{"patched-go-downgraded", `1.25.12`, `1.25.0`, "P8_WORKFLOW_GO_PATCH_MISSING"},
		{"platform-compile-rejects-no-test-packages", `-profile compile-${{ matrix.os }}-${{ matrix.go }}`, `-profile compile-${{ matrix.os }}-${{ matrix.go }} -reject-skips`, "P8_WORKFLOW_PLATFORM_COMPILE_SKIP_POLICY"},
		{"hosted-fuzz-unbounded", `GOMAXPROCS: "2"`, `HOSTED_MAXPROCS_REMOVED: "2"`, "P8_WORKFLOW_FUZZ_BOUNDARY_UNBOUNDED"},
		{"external-fuzz-wrapper-reentered", `-run '^FuzzP8PublicInputNeverDisclosesProtectedCanary$'`, `-run '^$'`, "P8_WORKFLOW_FUZZ_BOUNDARY_UNBOUNDED"},
		{"external-example-workspace-removed", `go work edit -replace github.com/eleven-am/golem/go@v0.0.0=`, `go work edit -replace github.com/eleven-am/golem/go@v0.0.0-removed=`, "P8_WORKFLOW_EXAMPLE_WORKSPACE_MISSING"},
		{"required-provider-job-skips", `postgres: "15"`, `postgres: "14"`, "P8_WORKFLOW_POSTGRES_15_MISSING"},
		{"external-oc-mandatory-mode-removed", `GOLEM_P8_REQUIRE_POSTGRESQL: "1"`, `GOLEM_P8_REQUIRE_POSTGRESQL: "0"`, "P8_WORKFLOW_REQUIRED_PROVIDER_MODE_MISSING"},
		{"external-oc-c-dsn-removed", `GOLEM_TEST_POSTGRES_DSN: postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable`, `GOLEM_TEST_POSTGRES_DSN_REMOVED: postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable`, "P8_WORKFLOW_POSTGRES_C_DSN_MISSING"},
		{"external-oc-linguistic-dsn-removed", `GOLEM_TEST_POSTGRES_LINGUISTIC_DSN: postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable`, `GOLEM_TEST_POSTGRES_LINGUISTIC_DSN_REMOVED: postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable`, "P8_WORKFLOW_POSTGRES_LINGUISTIC_DSN_MISSING"},
		{"skip-detector-removed", "-reject-skips", "-accept-skips", "P8_WORKFLOW_SKIP_DETECTOR_MISSING"},
		{"isolated-mutation-mode-removed", `GOLEM_RUN_P8_ISOLATED_MUTATIONS: "1"`, `GOLEM_RUN_P8_ISOLATED_MUTATIONS: "0"`, "P8_WORKFLOW_ISOLATED_MUTATION_MODE_MISSING"},
		{"isolated-mutation-gate-removed", "go test -json -count=1 -timeout=210m ./internal/p8mutation", "go test -json -count=1 -timeout=210m ./internal/compatibility", "P8_WORKFLOW_ISOLATED_MUTATION_GATE_MISSING"},
		{"isolated-mutation-timeout-collapsed", "timeout-minutes: 360", "timeout-minutes: 240", "P8_WORKFLOW_ISOLATED_MUTATION_BOUNDARY_MISSING"},
		{"isolated-mutation-evidence-omitted", "go/p8-isolated-mutation.events.jsonl", "go/p8-isolated-mutation.events.omitted", "P8_WORKFLOW_ISOLATED_MUTATION_BOUNDARY_MISSING"},
		{"moving-action", "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683", "actions/checkout@v4", "P8_WORKFLOW_MUTABLE_ACTION"},
		{"race-removed", "go test -json -race", "go test -json", "P8_WORKFLOW_RACE_MISSING"},
		{"write-permission", "contents: read", "contents: write", "P8_WORKFLOW_PERMISSIONS_NOT_LEAST"},
		{"artifact-retention-removed", "retention-days: 14", "retention-days: 1", "P8_WORKFLOW_ARTIFACT_RETENTION_MISSING"},
		{"signer-path-substituted", `${RUNNER_TEMP}/p8-release-allowed-signers`, `${RUNNER_TEMP}/uncontrolled-signers`, "P8_WORKFLOW_SIGNER_FILE_MISSING"},
		{"signer-digest-check-removed", "sha256sum --check --status", "sha256sum --version", "P8_WORKFLOW_SIGNER_DIGEST_CHECK_MISSING"},
		{"tag-protection-removed", "-verify-tag-protection", "-reject-tag-protection", "P8_WORKFLOW_TAG_PROTECTION_MISSING"},
		{"uncontrolled-secret-substitution", "${{ secrets.GOLEM_RELEASE_ALLOWED_SIGNERS_B64 }}", "${{ secrets.UNCONTROLLED_SIGNER }}", "P8_WORKFLOW_SECRET_REFERENCE"},
		{"mutable-service-image", "postgres:15@sha256:6eb0add3b77c081df18aa518ce43df58fdcc40f2e6d868a6fd08038dc7acd425", "postgres:15", "P8_WORKFLOW_MUTABLE_SERVICE_IMAGE"},
		{"mutable-pgvector-image", "pgvector/pgvector@sha256:7ae6051efd0e60444282c27c7e141af07f322ce033300e727a49c3dd11075e38", "pgvector/pgvector:pg17", "P8_WORKFLOW_MUTABLE_SERVICE_IMAGE"},
		{"semantic-pgvector-gate-removed", "TestFreshGeneratedSemanticPostgreSQLApplicationOwnsPGVectorLifecycle", "TestFreshGeneratedSemanticPostgreSQLApplicationSkipped", "P8_WORKFLOW_PGVECTOR_GATE_MISSING"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if count := strings.Count(original, mutation.before); count == 0 {
				t.Fatalf("mutation target %q is absent", mutation.before)
			}
			mutant := strings.ReplaceAll(original, mutation.before, mutation.after)
			if mutant == original {
				t.Fatal("mutation was a no-op")
			}
			codes := violationCodes(AuditWorkflow([]byte(mutant)))
			if !contains(codes, mutation.want) {
				t.Fatalf("mutation survived: violations=%v want=%s", codes, mutation.want)
			}
		})
	}
}

func TestP8WorkflowAuditRejectsRelocatedIsolatedMutationMode(t *testing.T) {
	original := string(readHostedWorkflow(t))
	mutationStep := strings.Join([]string{
		"      - name: Kill isolated P8 mutation catalogs",
		"        env:",
		`          GOLEM_RUN_P8_ISOLATED_MUTATIONS: "1"`,
		"        run: go test -json -count=1 -timeout=210m ./internal/p8mutation | tee p8-isolated-mutation.events.jsonl",
	}, "\n")
	relocated := strings.Join([]string{
		"      - name: Kill isolated P8 mutation catalogs",
		"        run: go test -json -count=1 -timeout=210m ./internal/p8mutation | tee p8-isolated-mutation.events.jsonl",
		"      - name: Misplaced isolated mutation mode",
		"        env:",
		`          GOLEM_RUN_P8_ISOLATED_MUTATIONS: "1"`,
		"        run: true",
	}, "\n")
	mutant := strings.Replace(original, mutationStep, relocated, 1)
	if mutant == original {
		t.Fatal("isolated mutation mode relocation was a no-op")
	}
	if codes := violationCodes(AuditWorkflow([]byte(mutant))); !contains(codes, "P8_WORKFLOW_ISOLATED_MUTATION_BOUNDARY_MISSING") {
		t.Fatalf("isolated mutation mode relocation survived: %v", codes)
	}
}

func TestP8WorkflowAuditRejectsExternalOptimisticConcurrencyEnvironmentRelocationAndOverride(t *testing.T) {
	original := string(readHostedWorkflow(t))
	rootBlock := `env:
  GOLEM_P8_REQUIRE_POSTGRESQL: "1"
  GOLEM_P8_REQUIRE_NATS: "1"
  GOLEM_TEST_POSTGRES_DSN: postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable
  GOLEM_TEST_POSTGRES_LINGUISTIC_DSN: postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable
  GOLEM_P8_SOCIAL_POSTGRES_DSN: postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable`
	withoutExternal := `env:
  GOLEM_P8_REQUIRE_NATS: "1"
  GOLEM_P8_SOCIAL_POSTGRES_DSN: postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable`
	relocated := strings.Replace(original, rootBlock, withoutExternal, 1)
	relocated = strings.Replace(relocated, "  provider-matrix:\n", `  provider-matrix:
    env:
      GOLEM_P8_REQUIRE_POSTGRESQL: "1"
      GOLEM_TEST_POSTGRES_DSN: postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable
      GOLEM_TEST_POSTGRES_LINGUISTIC_DSN: postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable
`, 1)
	if relocated == original {
		t.Fatal("external optimistic-concurrency environment relocation was a no-op")
	}
	codes := violationCodes(AuditWorkflow([]byte(relocated)))
	for _, want := range []string{"P8_WORKFLOW_REQUIRED_PROVIDER_MODE_MISSING", "P8_WORKFLOW_POSTGRES_C_DSN_MISSING", "P8_WORKFLOW_POSTGRES_LINGUISTIC_DSN_MISSING"} {
		if !contains(codes, want) {
			t.Fatalf("relocated external optimistic-concurrency environment survived: violations=%v want=%s", codes, want)
		}
	}

	for _, jobName := range []string{"toolchain-suite", "hardening"} {
		before := "  " + jobName + ":\n"
		after := before + "    env:\n      GOLEM_P8_REQUIRE_POSTGRESQL: \"0\"\n"
		mutant := strings.Replace(original, before, after, 1)
		if mutant == original {
			t.Fatalf("%s environment override mutation was a no-op", jobName)
		}
		if overrideCodes := violationCodes(AuditWorkflow([]byte(mutant))); !contains(overrideCodes, "P8_WORKFLOW_EXTERNAL_OC_ENV_OVERRIDE") {
			t.Fatalf("%s external optimistic-concurrency environment override survived: %v", jobName, overrideCodes)
		}
	}
}

func TestP8StructuredTestEventAuditRejectsSkipFailureAndMissingProfile(t *testing.T) {
	directory := t.TempDir()
	pass := writeEvents(t, directory, "pass.jsonl", []testEvent{
		{Action: "run", Package: "example.test", Test: "TestGate"},
		{Action: "pass", Package: "example.test", Test: "TestGate"},
		{Action: "pass", Package: "example.test"},
	})
	required := "github.com/eleven-am/golem/go/example/test:TestGate"
	pass = writeEvents(t, directory, "pass.jsonl", []testEvent{{Action: "run", Package: "github.com/eleven-am/golem/go/example/test", Test: "TestGate"}, {Action: "pass", Package: "github.com/eleven-am/golem/go/example/test", Test: "TestGate"}, {Action: "pass", Package: "github.com/eleven-am/golem/go/example/test"}})
	audit, err := AuditTestEvents("postgresql-17-c+linguistic", []string{pass}, []string{required}, nil, true)
	if err != nil || audit.Status != "PASS" || audit.PackagesPass != 1 || audit.TestsPass != 1 || len(audit.SourceSHA256) != 1 || len(audit.SourceSHA256[0]) != 64 {
		t.Fatalf("passing audit=%#v err=%v", audit, err)
	}
	for _, scenario := range []struct {
		name    string
		profile string
		events  []testEvent
	}{
		{"skip", "sqlite", []testEvent{{Action: "skip", Package: "example.test", Test: "TestRequired"}, {Action: "pass", Package: "example.test"}}},
		{"failure", "sqlite", []testEvent{{Action: "fail", Package: "example.test", Test: "TestRequired"}, {Action: "fail", Package: "example.test"}}},
		{"no-package-pass", "sqlite", []testEvent{{Action: "pass", Package: "example.test", Test: "TestRequired"}}},
		{"open-profile", "../../secret", []testEvent{{Action: "pass", Package: "example.test"}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := writeEvents(t, directory, scenario.name+".jsonl", scenario.events)
			if _, err := AuditTestEvents(scenario.profile, []string{path}, nil, nil, true); err == nil {
				t.Fatal("invalid hosted evidence was accepted")
			}
		})
	}
	if _, err := AuditTestEvents("sqlite", []string{pass}, []string{"github.com/eleven-am/golem/go/example/test:TestAbsent"}, nil, true); err == nil {
		t.Fatal("missing exact required test identity was accepted")
	}
}

func readHostedWorkflow(t *testing.T) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve workflow audit source")
	}
	path := filepath.Join(filepath.Dir(source), "..", "..", "..", ".github", "workflows", "p8-release-candidate.yml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func readRequiredInventory(t *testing.T) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve workflow audit source")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(source), "required-tests.json"))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func writeEvents(t *testing.T, directory, name string, events []testEvent) string {
	t.Helper()
	path := filepath.Join(directory, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func violationCodes(values []Violation) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = values[index].Code
	}
	return result
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
