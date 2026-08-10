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
	identity := "github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/linguistic"
	for _, replacement := range []string{"", strings.Replace(identity, "/linguistic", "/renamed", 1)} {
		mutant := strings.ReplaceAll(original, identity, replacement)
		if mutant == original {
			t.Fatal("required identity mutation was a no-op")
		}
		if codes := violationCodes(AuditRequiredTestInventory([]byte(mutant))); !contains(codes, "P8_WORKFLOW_REQUIRED_TEST_MISSING") {
			t.Fatalf("required identity mutation survived: %v", codes)
		}
	}
}

func TestP8WorkflowAuditKillsRequiredProfileSkipAndSupplyChainMutations(t *testing.T) {
	original := string(readHostedWorkflow(t))
	mutations := []struct {
		name, before, after, want string
	}{
		{"minimum-go-downgraded", `1.25.x`, `1.23.x`, "P8_WORKFLOW_GO_MINIMUM_MISSING"},
		{"required-provider-job-skips", `postgres: "15"`, `postgres: "14"`, "P8_WORKFLOW_POSTGRES_15_MISSING"},
		{"skip-detector-removed", "-reject-skips", "-accept-skips", "P8_WORKFLOW_SKIP_DETECTOR_MISSING"},
		{"moving-action", "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683", "actions/checkout@v4", "P8_WORKFLOW_MUTABLE_ACTION"},
		{"race-removed", "go test -json -race", "go test -json", "P8_WORKFLOW_RACE_MISSING"},
		{"write-permission", "contents: read", "contents: write", "P8_WORKFLOW_PERMISSIONS_NOT_LEAST"},
		{"artifact-retention-removed", "retention-days: 14", "retention-days: 1", "P8_WORKFLOW_ARTIFACT_RETENTION_MISSING"},
		{"signer-path-substituted", `${RUNNER_TEMP}/p8-release-allowed-signers`, `${RUNNER_TEMP}/uncontrolled-signers`, "P8_WORKFLOW_SIGNER_FILE_MISSING"},
		{"signer-digest-check-removed", "sha256sum --check --status", "sha256sum --version", "P8_WORKFLOW_SIGNER_DIGEST_CHECK_MISSING"},
		{"tag-protection-removed", "-verify-tag-protection", "-reject-tag-protection", "P8_WORKFLOW_TAG_PROTECTION_MISSING"},
		{"uncontrolled-secret-substitution", "${{ secrets.GOLEM_RELEASE_ALLOWED_SIGNERS_B64 }}", "${{ secrets.UNCONTROLLED_SIGNER }}", "P8_WORKFLOW_SECRET_REFERENCE"},
		{"mutable-service-image", "postgres:15@sha256:6eb0add3b77c081df18aa518ce43df58fdcc40f2e6d868a6fd08038dc7acd425", "postgres:15", "P8_WORKFLOW_MUTABLE_SERVICE_IMAGE"},
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
