package workflowaudit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var immutableAction = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$`)

var externalOptimisticConcurrencyWorkflowEnvironment = []struct {
	key, value, missing string
}{
	{"GOLEM_P8_REQUIRE_POSTGRESQL", "1", "P8_WORKFLOW_REQUIRED_PROVIDER_MODE_MISSING"},
	{"GOLEM_P8_REQUIRE_NATS", "1", "P8_WORKFLOW_REQUIRED_NATS_MODE_MISSING"},
	{"GOLEM_TEST_POSTGRES_DSN", "postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable", "P8_WORKFLOW_POSTGRES_C_DSN_MISSING"},
	{"GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", "postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable", "P8_WORKFLOW_POSTGRES_LINGUISTIC_DSN_MISSING"},
}

type Violation struct {
	Code string
}

// AuditWorkflow validates the closed, hosted P8 release-candidate workflow.
// It deliberately checks semantics that GitHub accepts but that would weaken
// evidence, including skipped profiles, mutable actions and permissive jobs.
func AuditWorkflow(contents []byte) []Violation {
	var document yaml.Node
	violations := []Violation{}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return []Violation{{Code: "P8_WORKFLOW_YAML_INVALID"}}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return []Violation{{Code: "P8_WORKFLOW_ROOT_INVALID"}}
	}
	root := document.Content[0]
	if scalar(mappingValue(root, "name")) != "p8-release-candidate" {
		violations = append(violations, Violation{Code: "P8_WORKFLOW_NAME_MISSING"})
	}
	permissions := mappingValue(root, "permissions")
	if permissions == nil || scalar(mappingValue(permissions, "contents")) != "read" || len(permissions.Content) != 2 {
		violations = append(violations, Violation{Code: "P8_WORKFLOW_PERMISSIONS_NOT_LEAST"})
	}
	concurrency := mappingValue(root, "concurrency")
	if concurrency == nil || scalar(mappingValue(concurrency, "cancel-in-progress")) != "true" || mappingValue(concurrency, "group") == nil {
		violations = append(violations, Violation{Code: "P8_WORKFLOW_CONCURRENCY_MISSING"})
	}
	rootEnvironment := mappingValue(root, "env")
	for _, required := range externalOptimisticConcurrencyWorkflowEnvironment {
		if scalar(mappingValue(rootEnvironment, required.key)) != required.value {
			violations = append(violations, Violation{Code: required.missing})
		}
	}
	jobs := mappingValue(root, "jobs")
	requiredJobs := []string{"workflow-audit", "toolchain-suite", "platform-compile", "provider-matrix", "hardening", "fuzz", "mutation-crash", "quality-docs-abi", "tag-release-candidate", "candidate-evidence"}
	for _, job := range requiredJobs {
		if mappingValue(jobs, job) == nil {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_JOB_MISSING_" + strings.ToUpper(strings.ReplaceAll(job, "-", "_"))})
		}
	}
	if jobs != nil {
		for index := 0; index+1 < len(jobs.Content); index += 2 {
			jobName, job := jobs.Content[index].Value, jobs.Content[index+1]
			if mappingValue(job, "timeout-minutes") == nil {
				violations = append(violations, Violation{Code: "P8_WORKFLOW_TIMEOUT_MISSING_" + strings.ToUpper(strings.ReplaceAll(jobName, "-", "_"))})
			}
		}
		tagJob := mappingValue(jobs, "tag-release-candidate")
		if tagJob == nil || scalar(mappingValue(tagJob, "environment")) != "go-release" || !jobUses(tagJob, "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683") {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_TAG_TRUST_ENVIRONMENT_MISSING"})
		}
		candidateJob := mappingValue(jobs, "candidate-evidence")
		if candidateJob == nil || !jobUses(candidateJob, "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683") {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_CANDIDATE_CHECKOUT_MISSING"})
		}
		if !sameStrings(sequenceScalars(mappingValue(candidateJob, "needs")), append(append([]string(nil), requiredJobs[:8]...), "tag-release-candidate")) {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_CANDIDATE_AGGREGATION_INCOMPLETE"})
		}
		platformJob := mappingValue(jobs, "platform-compile")
		if jobContains(platformJob, "-reject-skips") {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_PLATFORM_COMPILE_SKIP_POLICY"})
		}
		fuzzJob := mappingValue(jobs, "fuzz")
		if !jobContains(fuzzJob, "GOMAXPROCS") || !jobContains(fuzzJob, "GOFLAGS") || !jobContains(fuzzJob, `-run '^FuzzP8PublicInputNeverDisclosesProtectedCanary$'`) {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_FUZZ_BOUNDARY_UNBOUNDED"})
		}
		mutationJob := mappingValue(jobs, "mutation-crash")
		if scalar(mappingValue(mutationJob, "timeout-minutes")) != "360" ||
			!jobHasMutationExecutionStep(mutationJob) ||
			!jobContains(mutationJob, "go/p8-isolated-mutation.events.jsonl") {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_ISOLATED_MUTATION_BOUNDARY_MISSING"})
		}
		for _, jobName := range []string{"toolchain-suite", "hardening"} {
			job := mappingValue(jobs, jobName)
			for _, required := range externalOptimisticConcurrencyWorkflowEnvironment {
				if jobDefinesEnvironment(job, required.key) {
					violations = append(violations, Violation{Code: "P8_WORKFLOW_EXTERNAL_OC_ENV_OVERRIDE"})
				}
			}
		}
		for jobName, boundary := range map[string][3]string{
			"toolchain-suite": {"Complete P0-P8 suite", "Retain structured toolchain evidence", "./..."},
			"provider-matrix": {"SQLite and PostgreSQL C plus linguistic provider matrix", "Retain structured provider evidence", "./internal/p8oracle/..."},
			"hardening":       {"Race and resource-leak matrix", "Retain structured hardening evidence", "./..."},
		} {
			job := mappingValue(jobs, jobName)
			if !jobHasLiveNATSBoundary(job, boundary[0], boundary[1], boundary[2]) {
				violations = append(violations, Violation{Code: "P8_WORKFLOW_LIVE_NATS_BOUNDARY_MISSING_" + strings.ToUpper(strings.ReplaceAll(jobName, "-", "_"))})
			}
			for _, identity := range externalNATSWorkflowIdentities {
				if !jobContains(job, "-require-test "+identity) {
					violations = append(violations, Violation{Code: "P8_WORKFLOW_LIVE_NATS_IDENTITY_MISSING_" + strings.ToUpper(strings.ReplaceAll(jobName, "-", "_"))})
				}
			}
			for _, required := range externalOptimisticConcurrencyWorkflowEnvironment {
				if jobDefinesEnvironment(job, required.key) {
					violations = append(violations, Violation{Code: "P8_WORKFLOW_LIVE_NATS_ENV_OVERRIDE"})
				}
			}
		}
	}
	walk(root, func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && strings.Contains(node.Value, "@") && strings.Contains(node.Value, "/") && strings.HasPrefix(node.Value, "actions/") && !immutableAction.MatchString(node.Value) {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_MUTABLE_ACTION"})
		}
	})
	text := string(contents)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "image: postgres:") || strings.HasPrefix(trimmed, "image: pgvector/pgvector") {
			if strings.Contains(trimmed, "@sha256:") || strings.Contains(trimmed, "${{") {
				continue
			}
			violations = append(violations, Violation{Code: "P8_WORKFLOW_MUTABLE_SERVICE_IMAGE"})
		}
	}
	requiredFragments := []struct{ code, value string }{
		{"P8_WORKFLOW_GO_MINIMUM_MISSING", "1.25.x"},
		{"P8_WORKFLOW_GO_PATCH_MISSING", "1.25.12"},
		{"P8_WORKFLOW_GO_STABLE_MISSING", "stable"},
		{"P8_WORKFLOW_LINUX_MISSING", "ubuntu-24.04"},
		{"P8_WORKFLOW_MACOS_MISSING", "macos-14"},
		{"P8_WORKFLOW_WINDOWS_MISSING", "windows-2022"},
		{"P8_WORKFLOW_POSTGRES_15_MISSING", `postgres: "15"`},
		{"P8_WORKFLOW_POSTGRES_16_MISSING", `postgres: "16"`},
		{"P8_WORKFLOW_POSTGRES_17_MISSING", `postgres: "17"`},
		{"P8_WORKFLOW_POSTGRES_C_MISSING", "POSTGRES_INITDB_ARGS: --locale=C"},
		{"P8_WORKFLOW_POSTGRES_LINGUISTIC_MISSING", "POSTGRES_INITDB_ARGS: --locale=en_US.utf8"},
		{"P8_WORKFLOW_PGVECTOR_IMAGE_MISSING", "pgvector/pgvector@sha256:7ae6051efd0e60444282c27c7e141af07f322ce033300e727a49c3dd11075e38"},
		{"P8_WORKFLOW_PGVECTOR_REQUIRED_MODE_MISSING", "GOLEM_REQUIRE_PGVECTOR: \"1\""},
		{"P8_WORKFLOW_PGVECTOR_DSN_MISSING", "GOLEM_TEST_PGVECTOR_DSN: postgresql://postgres@127.0.0.1:55434/golem?sslmode=disable"},
		{"P8_WORKFLOW_PGVECTOR_GATE_MISSING", "TestFreshGeneratedSemanticPostgreSQLApplicationOwnsPGVectorLifecycle"},
		{"P8_WORKFLOW_JSON_EVENTS_MISSING", "go test -json"},
		{"P8_WORKFLOW_SKIP_DETECTOR_MISSING", "-reject-skips"},
		{"P8_WORKFLOW_EXPECTED_PROFILE_MISSING", "-inventory internal/workflowaudit/required-tests.json"},
		{"P8_WORKFLOW_EVENT_AUDITOR_MISSING", "./internal/cmd/p8workflowaudit"},
		{"P8_WORKFLOW_RACE_MISSING", "go test -json -race"},
		{"P8_WORKFLOW_REPEAT_MISSING", "-count=2"},
		{"P8_WORKFLOW_SHUFFLE_MISSING", "-shuffle=on"},
		{"P8_WORKFLOW_FUZZ_MISSING", "-fuzztime=60s"},
		{"P8_WORKFLOW_MUTATION_MISSING", "./internal/cmd/p8mutation"},
		{"P8_WORKFLOW_ISOLATED_MUTATION_MODE_MISSING", "GOLEM_RUN_P8_ISOLATED_MUTATIONS: \"1\""},
		{"P8_WORKFLOW_ISOLATED_MUTATION_GATE_MISSING", "go test -json -count=1 -timeout=210m ./internal/p8mutation"},
		{"P8_WORKFLOW_CRASH_MISSING", "./internal/cmd/p7crash"},
		{"P8_WORKFLOW_FAILURE_COMPLETION_MISSING", "./internal/cmd/p8failure"},
		{"P8_WORKFLOW_DOCS_COMPLETION_MISSING", "./internal/cmd/p8docs"},
		{"P8_WORKFLOW_COMPAT_COMPLETION_MISSING", "./internal/cmd/p8compat"},
		{"P8_WORKFLOW_LEAK_MISSING", "TestP8GoroutineQueueAndEvaluationHardBounds"},
		{"P8_WORKFLOW_VET_MISSING", "go vet ./..."},
		{"P8_WORKFLOW_FORMAT_MISSING", "gofmt -l"},
		{"P8_WORKFLOW_VULN_MISSING", "govulncheck ./..."},
		{"P8_WORKFLOW_DOCS_MISSING", "TestP8DocumentationCommandCorpus"},
		{"P8_WORKFLOW_EXAMPLE_MISSING", "TestP8ExternalSocialApplicationGenerateCheckBuildAndRun"},
		{"P8_WORKFLOW_EXAMPLE_WORKSPACE_MISSING", "go work edit -replace github.com/eleven-am/golem/go@v0.0.0="},
		{"P8_WORKFLOW_PUBLIC_ABI_MISSING", "TestP8PublicGoAPIDiffGate"},
		{"P8_WORKFLOW_REPRODUCIBLE_GENERATION_MISSING", "TestGeneratePublishesThenCheckIsReadOnlyAndDeterministic"},
		{"P8_WORKFLOW_SIGNED_TAG_TRIGGER_MISSING", `tags: ["go/v*.*.*"]`},
		{"P8_WORKFLOW_SIGNER_CONTENT_MISSING", "GOLEM_RELEASE_ALLOWED_SIGNERS_B64"},
		{"P8_WORKFLOW_SIGNER_DIGEST_MISSING", "GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256"},
		{"P8_WORKFLOW_SIGNER_FILE_MISSING", `${RUNNER_TEMP}/p8-release-allowed-signers`},
		{"P8_WORKFLOW_SIGNER_DIGEST_CHECK_MISSING", "sha256sum --check --status"},
		{"P8_WORKFLOW_TAG_PROTECTION_MISSING", "-verify-tag-protection"},
		{"P8_WORKFLOW_TAG_PROTECTION_EVIDENCE_MISSING", "p8-tag-protection.json"},
		{"P8_WORKFLOW_RELEASE_VERIFY_MISSING", "./internal/cmd/p8release -mode verify"},
		{"P8_WORKFLOW_RELEASE_IMMUTABILITY_MISSING", "./internal/cmd/p8release -mode publish"},
		{"P8_WORKFLOW_ARTIFACT_RETENTION_MISSING", "retention-days: 14"},
	}
	for _, required := range requiredFragments {
		if !strings.Contains(text, required.value) {
			violations = append(violations, Violation{Code: required.code})
		}
	}
	if strings.Count(text, "-allowed-signers-sha256 \"${GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256}\"") != 4 || strings.Count(text, "-allowed-signers \"${RUNNER_TEMP}/p8-release-allowed-signers\"") != 4 {
		violations = append(violations, Violation{Code: "P8_WORKFLOW_RELEASE_TRUST_ARGUMENT_MISSING"})
	}
	secretScrubbed := strings.ReplaceAll(text, "${{ secrets.GOLEM_RELEASE_ALLOWED_SIGNERS_B64 }}", "")
	secretScrubbed = strings.ReplaceAll(secretScrubbed, "${{ secrets.GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256 }}", "")
	for _, forbidden := range []struct{ code, value, source string }{
		{"P8_WORKFLOW_CONTINUE_ON_ERROR", "continue-on-error: true", text},
		{"P8_WORKFLOW_SHELL_FAILURE_IGNORED", "|| true", text},
		{"P8_WORKFLOW_UNCOVERED_MUTATION_ALLOWED", "allow-uncovered", text},
		{"P8_WORKFLOW_SECRET_REFERENCE", "secrets.", secretScrubbed},
		{"P8_WORKFLOW_WRITE_PERMISSION", ": write", text},
	} {
		if strings.Contains(forbidden.source, forbidden.value) {
			violations = append(violations, Violation{Code: forbidden.code})
		}
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Code < violations[j].Code })
	return deduplicate(violations)
}

func jobUses(job *yaml.Node, value string) bool {
	found := false
	walk(job, func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && node.Value == value {
			found = true
		}
	})
	return found
}

func jobContains(job *yaml.Node, value string) bool {
	found := false
	walk(job, func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && strings.Contains(node.Value, value) {
			found = true
		}
	})
	return found
}

func jobDefinesEnvironment(job *yaml.Node, key string) bool {
	found := false
	walk(job, func(node *yaml.Node) {
		if node.Kind != yaml.MappingNode {
			return
		}
		environment := mappingValue(node, "env")
		if environment != nil && mappingValue(environment, key) != nil {
			found = true
		}
	})
	return found
}

func jobHasEnvironmentValue(job *yaml.Node, key, value string) bool {
	found := false
	walk(job, func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode && scalar(mappingValue(node, key)) == value {
			found = true
		}
	})
	return found
}

func jobHasMutationExecutionStep(job *yaml.Node) bool {
	steps := mappingValue(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return false
	}
	found := false
	for _, step := range steps.Content {
		command := scalar(mappingValue(step, "run"))
		if strings.Contains(command, "go test -json -count=1 -timeout=210m ./internal/p8mutation") &&
			scalar(mappingValue(mappingValue(step, "env"), "GOLEM_RUN_P8_ISOLATED_MUTATIONS")) == "1" {
			if found {
				return false
			}
			found = true
		}
	}
	return found
}

const liveNATSImage = "nats:2.14.4@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"
const liveNATSRepoDigest = "nats@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"
const liveNATSContainerName = "golem-p8-order7-nats"
const liveNATSAbsenceCheck = "if docker container inspect golem-p8-order7-nats >/dev/null 2>&1; then\n  exit 1\nfi"
const liveNATSAbsenceWorkflow = "if docker container inspect golem-p8-order7-nats >/dev/null 2>&1; then\n            exit 1\n          fi"

func jobHasLiveNATSBoundary(job *yaml.Node, firstEvidence, lastEvidence, executionFragment string) bool {
	if job == nil {
		return false
	}
	steps := mappingValue(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return false
	}
	pull, first, last, cleanup := -1, -1, -1, -1
	for index, step := range steps.Content {
		name := scalar(mappingValue(step, "name"))
		switch name {
		case "Materialize pinned Core NATS image":
			command := scalar(mappingValue(step, "run"))
			absence := strings.Index(command, liveNATSAbsenceCheck)
			imagePull := strings.Index(command, "docker image pull "+liveNATSImage)
			digestCheck := strings.Index(command, "grep -Fx '"+liveNATSRepoDigest+"'")
			if imagePull >= 0 && digestCheck > imagePull && absence > digestCheck {
				pull = index
			}
		case firstEvidence:
			if strings.Contains(scalar(mappingValue(step, "run")), executionFragment) {
				first = index
			}
		case lastEvidence:
			last = index
		case "Clean test-owned Core NATS container":
			command := scalar(mappingValue(step, "run"))
			inspect := strings.Index(command, "docker container inspect "+liveNATSContainerName)
			owner := strings.Index(command, `test -n "${owner}"`)
			image := strings.Index(command, `test "${image}" = "`+liveNATSImage+`"`)
			remove := strings.Index(command, "docker container rm --force "+liveNATSContainerName)
			if scalar(mappingValue(step, "if")) == "always()" && strings.Contains(command, `{{index .Config.Labels "golem.p8.owner"}}|{{.Config.Image}}`) && inspect >= 0 && owner > inspect && image > owner && remove > image {
				cleanup = index
			}
		}
	}
	return pull >= 0 && pull < first && first <= last && last < cleanup
}

func sequenceScalars(node *yaml.Node) []string {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	result := make([]string, 0, len(node.Content))
	for _, child := range node.Content {
		result = append(result, scalar(child))
	}
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type TestEventAudit struct {
	Version      int      `json:"version"`
	Profile      string   `json:"profile"`
	Status       string   `json:"status"`
	SourceSHA256 []string `json:"sourceSHA256"`
	PackagesPass int      `json:"packagesPass"`
	TestsPass    int      `json:"testsPass"`
	Failures     []string `json:"failures"`
	Skipped      []string `json:"skipped"`
}

type RequiredTestSet struct {
	Required     []string `json:"required"`
	AllowedSkips []string `json:"allowedSkips"`
}

type RequiredTestInventory struct {
	Version int                        `json:"version"`
	Sets    map[string]RequiredTestSet `json:"sets"`
}

const externalOptimisticConcurrencyWorkflowIdentity = "github.com/eleven-am/golem/go/golemtest:TestOptimisticConcurrencySQLiteAndPostgreSQLExternalGeneratedApplication"
const executableMigrationGuideWorkflowIdentity = "github.com/eleven-am/golem/go/cmd/golem:TestP8ExecutableGoV002ToV010MigrationGuide"
const externalNATSOutageCWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSOutageReconnectAndReadiness/postgresql-c"
const externalNATSOutageLinguisticWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSOutageReconnectAndReadiness/postgresql-linguistic"
const externalNATSDuplicateCWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSDuplicateIdentityAndCoreNoReplay/postgresql-c"
const externalNATSDuplicateLinguisticWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSDuplicateIdentityAndCoreNoReplay/postgresql-linguistic"

var externalNATSWorkflowIdentities = []string{
	externalNATSOutageCWorkflowIdentity,
	externalNATSOutageLinguisticWorkflowIdentity,
	externalNATSDuplicateCWorkflowIdentity,
	externalNATSDuplicateLinguisticWorkflowIdentity,
}

var canonicalRequiredTests = map[string][]string{
	"workflow-audit": {"github.com/eleven-am/golem/go/internal/workflowaudit:TestP8WorkflowContainsRequiredHostedGates"},
	"toolchain": {
		"github.com/eleven-am/golem/go/internal/provider/sqlite:TestP8SQLiteClaimDepthSnapshotIsExactAndSerialized",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/c",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/linguistic",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestQueryPlanPostgreSQLLiveBoundPlanningWithoutExecution/c",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestQueryPlanPostgreSQLLiveBoundPlanningWithoutExecution/linguistic",
		externalOptimisticConcurrencyWorkflowIdentity,
		executableMigrationGuideWorkflowIdentity,
		externalNATSOutageCWorkflowIdentity,
		externalNATSOutageLinguisticWorkflowIdentity,
		externalNATSDuplicateCWorkflowIdentity,
		externalNATSDuplicateLinguisticWorkflowIdentity,
	},
	"provider": {
		"github.com/eleven-am/golem/go/internal/provider/sqlite:TestP8SQLiteClaimDepthSnapshotIsExactAndSerialized",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/c",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/linguistic",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestQueryPlanPostgreSQLLiveBoundPlanningWithoutExecution/c",
		"github.com/eleven-am/golem/go/internal/provider/postgresql:TestQueryPlanPostgreSQLLiveBoundPlanningWithoutExecution/linguistic",
		externalNATSOutageCWorkflowIdentity,
		externalNATSOutageLinguisticWorkflowIdentity,
		externalNATSDuplicateCWorkflowIdentity,
		externalNATSDuplicateLinguisticWorkflowIdentity,
	},
	"hardening": {
		"github.com/eleven-am/golem/go/internal/p8oracle/load:TestP8GoroutineQueueAndEvaluationHardBounds",
		externalOptimisticConcurrencyWorkflowIdentity,
		executableMigrationGuideWorkflowIdentity,
		externalNATSOutageCWorkflowIdentity,
		externalNATSOutageLinguisticWorkflowIdentity,
		externalNATSDuplicateCWorkflowIdentity,
		externalNATSDuplicateLinguisticWorkflowIdentity,
	},
	"fuzz-disclosure":  {"github.com/eleven-am/golem/go/internal/p8oracle/disclosure:FuzzP8PublicInputNeverDisclosesProtectedCanary"},
	"fuzz-diagnostics": {"github.com/eleven-am/golem/go/cmd/golem:FuzzP8DiagnosticEncodingIsClosedAndBounded"},
	"mutation-crash": {
		"github.com/eleven-am/golem/go/internal/p8oracle/failure:TestP8PublisherCDCAndMigrationCrashRecovery",
		"github.com/eleven-am/golem/go/internal/p8oracle/failure:TestP8GracefulAndForcedShutdownSubprocessMatrix",
	},
	"quality-docs-abi": {
		"github.com/eleven-am/golem/go/cmd/golem:TestP8DocumentationCommandCorpus",
		"github.com/eleven-am/golem/go/cmd/golem:TestP8EveryPublicSnippetTypeChecks",
		"github.com/eleven-am/golem/go/examples/social/cmd/social:TestP8ExternalSocialApplicationGenerateCheckBuildAndRun",
		"github.com/eleven-am/golem/go/internal/compatibility:TestP8PublicGoAPIDiffGate",
		"github.com/eleven-am/golem/go/cmd/golem:TestGeneratePublishesThenCheckIsReadOnlyAndDeterministic",
	},
}

func AuditRequiredTestInventory(contents []byte) []Violation {
	var inventory RequiredTestInventory
	if err := json.Unmarshal(contents, &inventory); err != nil || inventory.Version != 1 {
		return []Violation{{Code: "P8_WORKFLOW_INVENTORY_INVALID"}}
	}
	violations := []Violation{}
	for setName, expected := range canonicalRequiredTests {
		set, ok := inventory.Sets[setName]
		if !ok {
			violations = append(violations, Violation{Code: "P8_WORKFLOW_INVENTORY_SET_MISSING_" + strings.ToUpper(strings.ReplaceAll(setName, "-", "_"))})
			continue
		}
		present := map[string]bool{}
		for _, identity := range set.Required {
			present[identity] = true
		}
		for _, identity := range expected {
			if !present[identity] {
				violations = append(violations, Violation{Code: "P8_WORKFLOW_REQUIRED_TEST_MISSING"})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Code < violations[j].Code })
	return deduplicate(violations)
}

func ReadRequiredTestSet(path, name string) (RequiredTestSet, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return RequiredTestSet{}, fmt.Errorf("P8_WORKFLOW_INVENTORY_OPEN")
	}
	var inventory RequiredTestInventory
	if err := json.Unmarshal(contents, &inventory); err != nil || inventory.Version != 1 || len(inventory.Sets) == 0 {
		return RequiredTestSet{}, fmt.Errorf("P8_WORKFLOW_INVENTORY_INVALID")
	}
	set, ok := inventory.Sets[name]
	if !ok {
		return RequiredTestSet{}, fmt.Errorf("P8_WORKFLOW_INVENTORY_SET_MISSING")
	}
	seen := map[string]bool{}
	for _, identity := range append(append([]string(nil), set.Required...), set.AllowedSkips...) {
		if !validTestIdentity(identity) || seen[identity] {
			return RequiredTestSet{}, fmt.Errorf("P8_WORKFLOW_INVENTORY_IDENTITY_INVALID")
		}
		seen[identity] = true
	}
	return set, nil
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func AuditTestEvents(profile string, paths, requiredTests, allowedSkips []string, rejectSkips bool) (TestEventAudit, error) {
	result := TestEventAudit{Version: 1, Profile: profile, Status: "FAIL", SourceSHA256: []string{}, Failures: []string{}, Skipped: []string{}}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9+_.-]{0,63}$`).MatchString(profile) || len(paths) == 0 {
		return result, fmt.Errorf("P8_WORKFLOW_EVENT_CONFIG")
	}
	allowed := map[string]bool{}
	for _, identity := range allowedSkips {
		if !validTestIdentity(identity) {
			return result, fmt.Errorf("P8_WORKFLOW_EVENT_CONFIG")
		}
		allowed[identity] = true
	}
	passedTests := map[string]bool{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return result, fmt.Errorf("P8_WORKFLOW_EVENT_OPEN")
		}
		hash := sha256.New()
		reader := io.TeeReader(file, hash)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var event testEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				_ = file.Close()
				return result, fmt.Errorf("P8_WORKFLOW_EVENT_INVALID")
			}
			switch event.Action {
			case "pass":
				if event.Test == "" {
					result.PackagesPass++
				} else {
					result.TestsPass++
					passedTests[eventIdentity(event)] = true
				}
			case "fail":
				result.Failures = append(result.Failures, eventIdentity(event))
			case "skip":
				result.Skipped = append(result.Skipped, eventIdentity(event))
			}
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			return result, fmt.Errorf("P8_WORKFLOW_EVENT_READ")
		}
		_ = file.Close()
		result.SourceSHA256 = append(result.SourceSHA256, hex.EncodeToString(hash.Sum(nil)))
	}
	sort.Strings(result.Failures)
	sort.Strings(result.Skipped)
	for _, required := range requiredTests {
		if !passedTests[required] {
			result.Failures = append(result.Failures, "missing:"+required)
		}
	}
	sort.Strings(result.Failures)
	unallowedSkips := 0
	if rejectSkips {
		for _, identity := range result.Skipped {
			if !allowed[identity] {
				unallowedSkips++
			}
		}
	}
	if result.PackagesPass == 0 || len(result.Failures) != 0 || unallowedSkips != 0 {
		return result, fmt.Errorf("P8_WORKFLOW_EVENT_GATE_FAILED")
	}
	result.Status = "PASS"
	return result, nil
}

func validTestIdentity(identity string) bool {
	packageName, testName, ok := strings.Cut(identity, ":")
	return ok && strings.HasPrefix(packageName, "github.com/eleven-am/golem/go/") && (strings.HasPrefix(testName, "Test") || strings.HasPrefix(testName, "Fuzz"))
}

func eventIdentity(event testEvent) string {
	if event.Test == "" {
		return event.Package
	}
	return event.Package + ":" + event.Test
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func scalar(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func walk(node *yaml.Node, visit func(*yaml.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for _, child := range node.Content {
		walk(child, visit)
	}
}

func deduplicate(values []Violation) []Violation {
	result := make([]Violation, 0, len(values))
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1].Code != value.Code {
			result = append(result, value)
		}
	}
	return result
}
