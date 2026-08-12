package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/migration"
)

type migrationPlanJSON struct {
	FormatVersion uint16 `json:"formatVersion"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	Providers     []struct {
		Provider  string `json:"provider"`
		Artifacts []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
		Phases []struct {
			Ordinal    uint32 `json:"ordinal"`
			Operations []struct {
				ID              string   `json:"id"`
				Kind            string   `json:"kind"`
				Risk            string   `json:"risk"`
				Effect          string   `json:"effect"`
				Dependencies    []string `json:"dependencies"`
				TransactionMode string   `json:"transactionMode"`
				Approval        struct {
					Required bool `json:"required"`
					Present  bool `json:"present"`
				} `json:"approval"`
				ReviewedCompanion *struct {
					Path                string `json:"path"`
					SHA256              string `json:"sha256"`
					PostconditionDigest string `json:"postconditionDigest"`
				} `json:"reviewedCompanion,omitempty"`
				Warnings []string `json:"warnings"`
			} `json:"operations"`
		} `json:"phases"`
	} `json:"providers"`
	Warnings   []string `json:"warnings"`
	Guarantees struct {
		AppliesChanges        bool `json:"appliesChanges"`
		UsesReviewedTypedPlan bool `json:"usesReviewedTypedPlan"`
		ZeroDowntime          bool `json:"zeroDowntime"`
		DurationEstimated     bool `json:"durationEstimated"`
	} `json:"guarantees"`
}

func TestMigrationPlanProspectiveMatchesMigrationNewWithoutWriting(t *testing.T) {
	module := writeSocialModule(t, false)
	before := treeSnapshot(t, module)
	prospective := runMigrationPlanJSON(t, module)
	if after := treeSnapshot(t, module); !reflect.DeepEqual(before, after) {
		t.Fatal("prospective plan changed the module tree")
	}
	expected := migrationPlanOperationIDs(prospective)
	createInitialReviewedMigration(t, module)
	actual := reviewedOperationIDs(t, module, "0001_initial")
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("prospective operation IDs differ from migration new\nplan=%v\nnew=%v", expected, actual)
	}
}

func TestMigrationPlanReviewedVerifiesHistoryAndEveryArtifactBeforeRendering(t *testing.T) {
	module := writeSocialModule(t, false)
	createInitialReviewedMigration(t, module)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "plan", "--migration", "0001_initial", "--provider", "postgresql", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("reviewed plan code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	sqlPath := filepath.Join(module, "migrations", "sqlite", "0001_initial.sql")
	content, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sqlPath, append(content, []byte("-- tampered sibling\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	tamperedTree, err := snapshotMigrationPlanTree(module)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "plan", "--migration", "0001_initial", "--provider", "postgresql", "--json"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("provider filter hid sibling tamper code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if err := verifyMigrationPlanTree(module, tamperedTree); err != nil {
		t.Fatal("reviewed error path changed the module tree")
	}

	resealed := writeSocialModule(t, false)
	createInitialReviewedMigration(t, resealed)
	rewriteReviewedSQLWithValidChecksums(t, resealed, "sqlite", "0001_initial", []byte("-- checksum-valid but not deterministic provider SQL\n"))
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), resealed, []string{"migration", "plan", "--migration", "0001_initial", "--provider", "postgresql", "--json"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "differs from deterministic rendering") {
		t.Fatalf("rendered before full provider verification code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestMigrationPlanTextAndJSONShareOneCanonicalTypedReport(t *testing.T) {
	module := writeSocialModule(t, false)
	document := runMigrationPlanJSON(t, module)
	var textOut, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "plan"}, &textOut, &stderr); code != 0 {
		t.Fatalf("text plan code=%d stdout=%s stderr=%s", code, textOut.String(), stderr.String())
	}
	for _, ids := range migrationPlanOperationIDs(document) {
		for _, id := range ids {
			if !strings.Contains(textOut.String(), id) {
				t.Fatalf("text renderer omitted canonical operation %s", id)
			}
		}
	}
	for _, provider := range document.Providers {
		if !strings.Contains(strings.ToLower(textOut.String()), provider.Provider) {
			t.Fatalf("text renderer omitted provider %s", provider.Provider)
		}
	}
}

func TestMigrationPlanExplainsEveryOperationRiskEffectDependencyAndApproval(t *testing.T) {
	module := writeSocialModule(t, false)
	document := runMigrationPlanJSON(t, module)
	count := 0
	for _, provider := range document.Providers {
		for _, phase := range provider.Phases {
			for _, operation := range phase.Operations {
				count++
				if operation.ID == "" || operation.Kind == "" || operation.Risk == "" || operation.Effect == "" || operation.TransactionMode == "" || operation.Dependencies == nil {
					t.Fatalf("operation lacks typed explanation: %#v", operation)
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("initial prospective command reported no operations")
	}
}

func TestMigrationPlanExplainsSafeWideningAndReviewedBackfillWithoutClaims(t *testing.T) {
	module := writePostgreSQLWideningModule(t)
	createInitialReviewedMigration(t, module)
	schemaPath := filepath.Join(module, "schema.go")
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.Replace(content, []byte("type=varchar(16)"), []byte("type=varchar(32)"), 1)
	if err := os.WriteFile(schemaPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	widening := runMigrationPlanJSON(t, module)
	foundPreserving := false
	for _, provider := range widening.Providers {
		for _, phase := range provider.Phases {
			for _, operation := range phase.Operations {
				foundPreserving = foundPreserving || operation.Effect == "valuePreserving"
			}
		}
	}
	if !foundPreserving || widening.Guarantees.ZeroDowntime || widening.Guarantees.DurationEstimated {
		t.Fatalf("safe widening explanation invented or omitted claims: %#v", widening.Guarantees)
	}

	backfillModule, migrationID := createReviewedBackfill(t)
	backfill := runReviewedMigrationPlanJSON(t, backfillModule, migrationID)
	foundManual := false
	for _, provider := range backfill.Providers {
		for _, phase := range provider.Phases {
			for _, operation := range phase.Operations {
				if operation.Effect == "manualDataTransform" {
					foundManual = operation.ReviewedCompanion != nil && operation.ReviewedCompanion.PostconditionDigest != ""
				}
			}
		}
	}
	if !foundManual || backfill.Guarantees.ZeroDowntime || backfill.Guarantees.DurationEstimated {
		t.Fatal("reviewed backfill explanation omitted typed companion or invented operational claims")
	}
}

func TestMigrationPlanNoChangesIsDeterministicAndReadOnly(t *testing.T) {
	module := writeSocialModule(t, false)
	createInitialReviewedMigration(t, module)
	for name, content := range map[string]string{
		"runtime.db-wal": "wal-canary", "runtime.db-shm": "shm-canary", ".golem-generation.lock": "lock-canary", ".plan-temp-canary": "temp-canary",
	} {
		if err := os.WriteFile(filepath.Join(module, name), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("schema.go", filepath.Join(module, "schema-link-canary")); err != nil {
		t.Fatal(err)
	}
	before, err := snapshotMigrationPlanTree(module)
	if err != nil {
		t.Fatal(err)
	}
	first := runMigrationPlanBytes(t, module, []string{"--json"})
	second := runMigrationPlanBytes(t, module, []string{"--json"})
	if !bytes.Equal(first, second) || !bytes.Contains(first, []byte(`"status":"NO_CHANGES"`)) {
		t.Fatalf("no-change plan is not deterministic: %s / %s", first, second)
	}
	text := runMigrationPlanBytes(t, module, nil)
	if !bytes.Contains(text, []byte("No files or databases were modified.")) {
		t.Fatalf("no-change text omitted the explicit read-only outcome:\n%s", text)
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "plan"}, refusingWriter{}, &stderr); code != 1 {
		t.Fatalf("refused output exit=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "plan"}, shortWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "output could not be written") {
		t.Fatalf("short output exit=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "plan"}, panicWriter{}, &stderr); code != 1 {
		t.Fatalf("panicked output exit=%d stderr=%s", code, stderr.String())
	}
	if err := verifyMigrationPlanTree(module, before); err != nil {
		t.Fatal("success/refusal/panic changed bytes, modes, symlinks, WAL/SHM, locks, or temp nodes")
	}
}

func TestMigrationPlanNeverPrintsSQLValuesDSNsPhysicalNamesOrAbsolutePaths(t *testing.T) {
	module, migrationID := createReviewedBackfill(t)
	t.Setenv("GOLEM_POSTGRES_DSN", "postgres://plan-secret-canary")
	encoded := runMigrationPlanBytes(t, module, []string{"--migration", migrationID, "--json"})
	for _, forbidden := range []string{module, "postgres://plan-secret-canary", "unknown", `UPDATE `, `users`, `social_cli`} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("plan disclosed forbidden value %q in %s", forbidden, encoded)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside-plan-secret-canary.sql")
	redacted := redactMigrationPlanError(module, errors.New("read "+outside+": postgres://user:secret@host/private"))
	if strings.Contains(redacted.Error(), outside) || strings.Contains(redacted.Error(), "user:secret") {
		t.Fatalf("adversarial error path was not redacted: %s", redacted)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "plan", "--migrations", outside}, &stdout, &stderr); code != 1 || strings.Contains(stderr.String(), outside) {
		t.Fatalf("invalid root disclosed argument code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestMigrationPlanRejectsTamperPendingDraftUnknownKindAndInvalidFlags(t *testing.T) {
	module := writeSocialModule(t, false)
	for _, args := range [][]string{
		{"--migration", "0001_initial", "--schema", "."},
		{"--migration", "0001_initial", "--root", "DefineSchema"},
		{"--migration="}, {"--provider="}, {"--provider", "mysql"}, {"--dsn", "secret"}, {"--approve", "operation"}, {"--show-sql"}, {"--format", "yaml"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runMigrationPlan(context.Background(), module, args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("invalid flags accepted args=%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
	createInitialReviewedMigration(t, module)
	rewriteManifestWithUnknownOperation(t, module, "sqlite")
	var unknownOut, unknownErr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "plan", "--migration", "0001_initial", "--provider", "postgresql", "--json"}, &unknownOut, &unknownErr); code != 1 || unknownOut.Len() != 0 {
		t.Fatalf("unknown sibling operation rendered code=%d stdout=%s stderr=%s", code, unknownOut.String(), unknownErr.String())
	}

	backfillModule := writePostgreSQLWideningModule(t)
	createInitialReviewedMigration(t, backfillModule)
	schemaPath := filepath.Join(backfillModule, "schema.go")
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	content = addRequiredSlugField(t, content)
	if err := os.WriteFile(schemaPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), backfillModule, []string{"migration", "new", "--name", "required_slug"}, &stdout, &stderr); code != 0 {
		t.Fatalf("create pending code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var pending migrationNewOutput
	if err := json.Unmarshal(stdout.Bytes(), &pending); err != nil || !pending.Pending {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), backfillModule, []string{"migration", "plan", "--migration", string(pending.MigrationID)}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "not immutable reviewed history") {
		t.Fatalf("pending draft rendered code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	invalid := writeSocialModule(t, true)
	invalidBefore, err := snapshotMigrationPlanTree(invalid)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), invalid, []string{"migration", "plan", "--json"}, &stdout, &stderr); code != 1 || !bytes.Contains(stdout.Bytes(), []byte(`"formatVersion": 1`)) || !bytes.Contains(stdout.Bytes(), []byte(`"diagnostics"`)) || strings.Contains(stdout.String(), invalid) {
		t.Fatalf("closed diagnostic path code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if err := verifyMigrationPlanTree(invalid, invalidBefore); err != nil {
		t.Fatal("prospective build error changed the module tree")
	}
	for _, diagnosticCase := range []struct {
		args   []string
		canary string
	}{
		{[]string{"--root", "SuperSecretRootCanary"}, "SuperSecretRootCanary"},
		{[]string{"--schema", filepath.Join(t.TempDir(), "absolute-loader-secret-canary")}, "absolute-loader-secret-canary"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(context.Background(), invalid, append([]string{"migration", "plan", "--json"}, diagnosticCase.args...), &stdout, &stderr); code != 1 || !bytes.Contains(stdout.Bytes(), []byte(`"diagnostics"`)) || strings.Contains(stdout.String(), diagnosticCase.canary) || strings.Contains(stderr.String(), diagnosticCase.canary) {
			t.Fatalf("diagnostic canary leaked args=%v code=%d stdout=%s stderr=%s", diagnosticCase.args, code, stdout.String(), stderr.String())
		}
	}
}

func TestMigrationPlanOwnedBuildWorkspaceStaysOutsideModuleAndCleansEveryExit(t *testing.T) {
	module := writeSocialModule(t, false)
	before, err := snapshotMigrationPlanTree(module)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", module)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "plan"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "must be outside") {
		t.Fatalf("module-contained TMPDIR accepted code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if err := verifyMigrationPlanTree(module, before); err != nil {
		t.Fatal("contained TMPDIR refusal changed the module tree")
	}

	externalRoot := t.TempDir()
	t.Setenv("TMPDIR", externalRoot)
	originalFactory := makeMigrationPlanBuildWorkspace
	defer func() { makeMigrationPlanBuildWorkspace = originalFactory }()
	for _, testCase := range []struct {
		name        string
		firstPanics bool
	}{
		{"cleanup error retries", false},
		{"cleanup panic retries", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			attempts := 0
			var owned string
			makeMigrationPlanBuildWorkspace = func(moduleDir string) (migrationPlanBuildWorkspace, error) {
				workspace, createErr := newMigrationPlanBuildWorkspaceAt(moduleDir, externalRoot, func(target string) error {
					attempts++
					if attempts == 1 {
						if testCase.firstPanics {
							panic("cleanup panic canary")
						}
						return errors.New("cleanup refusal canary")
					}
					return os.RemoveAll(target)
				})
				owned = workspace.directory
				return workspace, createErr
			}
			stdout.Reset()
			stderr.Reset()
			if code := run(context.Background(), module, []string{"migration", "plan"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
				t.Fatalf("cleanup failure code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if attempts != 2 {
				t.Fatalf("cleanup attempts=%d want 2", attempts)
			}
			if _, err := os.Lstat(owned); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned workspace survived cleanup retry: %v", err)
			}
		})
	}
}

func TestMigrationPlanActiveWorkspaceIsCopiedAndReadOnly(t *testing.T) {
	module := writeSocialModule(t, false)
	goModPath := filepath.Join(module, "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	replaceLine := "replace github.com/eleven-am/golem/go => " + filepath.ToSlash(commandModuleRoot(t)) + "\n"
	goMod = bytes.Replace(goMod, []byte(replaceLine), nil, 1)
	if err := os.WriteFile(goModPath, goMod, 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := t.TempDir()
	workspacePath := filepath.Join(workspaceRoot, "go.work")
	workspace := "go 1.25.0\n\nuse (\n\t" + filepath.ToSlash(module) + "\n\t" + filepath.ToSlash(commandModuleRoot(t)) + "\n)\n"
	if err := os.WriteFile(workspacePath, []byte(workspace), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", workspacePath)
	for _, sum := range []string{"", "example.invalid/unused v0.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"} {
		sumPath := workspacePath + ".sum"
		if sum == "" {
			_ = os.Remove(sumPath)
		} else if err := os.WriteFile(sumPath, []byte(sum), 0o640); err != nil {
			t.Fatal(err)
		}
		moduleBefore, err := snapshotMigrationPlanTree(module)
		if err != nil {
			t.Fatal(err)
		}
		workspaceBefore, err := snapshotMigrationPlanTree(workspaceRoot)
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), module, []string{"migration", "plan", "--json"}, &stdout, &stderr)
		if code != 0 && code != 1 {
			t.Fatalf("active-workspace plan returned usage status code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		if err := verifyMigrationPlanTree(module, moduleBefore); err != nil {
			t.Fatal("active-workspace plan changed the module")
		}
		if err := verifyMigrationPlanTree(workspaceRoot, workspaceBefore); err != nil {
			t.Fatal("active-workspace plan changed go.work or go.work.sum")
		}
	}
}

func TestMigrationPlanPublicJSONFormatIsClosedVersionedAndBounded(t *testing.T) {
	module := writeSocialModule(t, false)
	encoded := runMigrationPlanBytes(t, module, []string{"--json"})
	if len(encoded) == 0 || len(encoded) > 16<<20 {
		t.Fatalf("public JSON size=%d", len(encoded))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	expected := []string{"formatVersion", "guarantees", "mode", "providers", "status", "warnings"}
	if !reflect.DeepEqual(keys, expected) || string(raw["formatVersion"]) != "1" {
		t.Fatalf("open or unversioned report keys=%v version=%s", keys, raw["formatVersion"])
	}
}

func TestMigrationPlanFreshExternalModuleCommandCorpus(t *testing.T) {
	module := writeSocialModule(t, false)
	createInitialReviewedMigration(t, module)
	before := treeSnapshot(t, module)
	for _, args := range [][]string{
		{"--migration", "0001_initial"},
		{"--migration", "0001_initial", "--json"},
		{"--migration", "0001_initial", "--provider", "sqlite", "--json"},
		{"--migration", "0001_initial", "--provider", "postgresql"},
	} {
		output := runMigrationPlanBytes(t, module, args)
		if len(output) == 0 {
			t.Fatalf("empty external command output args=%v", args)
		}
	}
	if after := treeSnapshot(t, module); !reflect.DeepEqual(before, after) {
		t.Fatal("offline external command corpus changed its clean module")
	}
}

type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("output canary") }

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return len(value) - 1, nil
}

func runMigrationPlanJSON(t *testing.T, module string) migrationPlanJSON {
	t.Helper()
	return decodeMigrationPlanJSON(t, runMigrationPlanBytes(t, module, []string{"--json"}))
}

func runReviewedMigrationPlanJSON(t *testing.T, module, migrationID string) migrationPlanJSON {
	t.Helper()
	return decodeMigrationPlanJSON(t, runMigrationPlanBytes(t, module, []string{"--migration", migrationID, "--json"}))
}

func runMigrationPlanBytes(t *testing.T, module string, args []string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := append([]string{"migration", "plan"}, args...)
	if code := run(context.Background(), module, command, &stdout, &stderr); code != 0 {
		t.Fatalf("migration plan args=%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("migration plan args=%v stderr=%s", args, stderr.String())
	}
	return append([]byte(nil), stdout.Bytes()...)
}

func decodeMigrationPlanJSON(t *testing.T, encoded []byte) migrationPlanJSON {
	t.Helper()
	var result migrationPlanJSON
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode migration plan: %v\n%s", err, encoded)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("migration plan JSON has trailing input: %v", err)
	}
	return result
}

func migrationPlanOperationIDs(document migrationPlanJSON) map[string][]string {
	result := map[string][]string{}
	for _, provider := range document.Providers {
		for _, phase := range provider.Phases {
			for _, operation := range phase.Operations {
				result[provider.Provider] = append(result[provider.Provider], operation.ID)
			}
		}
	}
	return result
}

func reviewedOperationIDs(t *testing.T, module, migrationID string) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, provider := range []string{"sqlite", "postgresql"} {
		encoded, err := os.ReadFile(filepath.Join(module, "migrations", provider, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var history migration.Manifest
		if err := json.Unmarshal(encoded, &history); err != nil {
			t.Fatal(err)
		}
		for _, entry := range history.Entries {
			if string(entry.ID) != migrationID {
				continue
			}
			for _, phase := range entry.Phases {
				result[provider] = append(result[provider], operationIDs(entry, phase.Operations)...)
			}
		}
	}
	return result
}

func operationIDs(entry migration.ManifestEntry, ids []migration.OperationID) []string {
	result := make([]string, len(ids))
	for index := range ids {
		result[index] = string(ids[index])
	}
	return result
}

func writePostgreSQLWideningModule(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	repository := commandModuleRoot(t)
	files := map[string]string{
		"go.mod": "module example.test/secure\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(repository) + "\n",
		"schema.go": `package secure

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }
type User struct {
	_ struct{} ` + "`golem:\"model;id=secure.User;table=privacy_table_canary\"`" + `
	ID int64 ` + "`db:\"id\" golem:\"id=secure.User.ID;pk\"`" + `
	Name *string ` + "`db:\"name\" golem:\"id=secure.User.Name;type=varchar(16)\"`" + `
}
func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "privacy_namespace_canary")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.PostgreSQL)
}
func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) { _, _ = rules, actor }
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func createReviewedBackfill(t *testing.T) (string, string) {
	t.Helper()
	module := writePostgreSQLWideningModule(t)
	createInitialReviewedMigration(t, module)
	schemaPath := filepath.Join(module, "schema.go")
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	content = addRequiredSlugField(t, content)
	if err := os.WriteFile(schemaPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "required_slug"}, &stdout, &stderr); code != 0 {
		t.Fatalf("create pending code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var pending migrationNewOutput
	if err := json.Unmarshal(stdout.Bytes(), &pending); err != nil || !pending.Pending {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	reviewedPath := filepath.Join(module, "reviewed.sql")
	if err := os.WriteFile(reviewedPath, []byte("UPDATE \"privacy_namespace_canary\".\"privacy_table_canary\" SET \"slug\" = 'unknown' WHERE \"slug\" IS NULL;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "backfill", "attach", "--migration", string(pending.MigrationID), "--field", "User.Slug", "--file", reviewedPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("attach pending code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	return module, string(pending.MigrationID)
}

func addRequiredSlugField(t *testing.T, content []byte) []byte {
	t.Helper()
	needle := []byte("\tName *string `db:\"name\" golem:\"id=secure.User.Name;type=varchar(16)\"`")
	replacement := append(append([]byte(nil), needle...), []byte("\n\tSlug string `db:\"slug\" golem:\"id=secure.User.Slug\"`")...)
	result := bytes.Replace(content, needle, replacement, 1)
	if bytes.Equal(result, content) {
		t.Fatal("required-slug fixture did not contain the optional name field")
	}
	return result
}

func rewriteReviewedSQLWithValidChecksums(t *testing.T, module, provider, migrationID string, replacement []byte) {
	t.Helper()
	manifestPath := filepath.Join(module, "migrations", provider, "manifest.json")
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var history migration.Manifest
	if err := json.Unmarshal(encoded, &history); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for entryIndex := range history.Entries {
		entry := &history.Entries[entryIndex]
		for fileIndex := range entry.Files {
			file := &entry.Files[fileIndex]
			content, readErr := os.ReadFile(filepath.Join(module, filepath.FromSlash(file.Path)))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(entry.ID) == migrationID && strings.HasSuffix(file.Path, ".sql") {
				content = append([]byte(nil), replacement...)
				if writeErr := os.WriteFile(filepath.Join(module, filepath.FromSlash(file.Path)), content, 0o644); writeErr != nil {
					t.Fatal(writeErr)
				}
				file.SHA256 = migration.Checksum(content)
			}
			files[file.Path] = content
		}
		for _, companion := range entry.Manual {
			content, readErr := os.ReadFile(filepath.Join(module, filepath.FromSlash(companion.File.Path)))
			if readErr != nil {
				t.Fatal(readErr)
			}
			files[companion.File.Path] = content
		}
		entry.ChainHash = ""
		entry.ChainHash = migration.ChainHash(*entry)
		if entryIndex+1 < len(history.Entries) {
			history.Entries[entryIndex+1].ParentChainHash = entry.ChainHash
		}
	}
	manifestBytes, err := migration.EncodeManifest(history, files)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(module, "migrations", workflowPublicationFilenameForTest())
	publicationBytes, err := os.ReadFile(publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	var publication codegenmanifest.Manifest
	if err := json.Unmarshal(publicationBytes, &publication); err != nil {
		t.Fatal(err)
	}
	for index := range publication.Artifacts {
		artifact := &publication.Artifacts[index]
		if artifact.Path == filepath.ToSlash(filepath.Join("migrations", provider, migrationID+".sql")) {
			artifact.ContentSHA256 = codegenmanifest.ContentHash(replacement)
		}
		if artifact.Path == filepath.ToSlash(filepath.Join("migrations", provider, "manifest.json")) {
			artifact.ContentSHA256 = codegenmanifest.ContentHash(manifestBytes)
		}
	}
	publicationBytes, err = json.MarshalIndent(publication, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	publicationBytes = append(publicationBytes, '\n')
	if err := os.WriteFile(publicationPath, publicationBytes, 0o644); err != nil {
		t.Fatal(err)
	}
}

func workflowPublicationFilenameForTest() string { return ".golem-publication.json" }

func rewriteManifestWithUnknownOperation(t *testing.T, module, provider string) {
	t.Helper()
	manifestPath := filepath.Join(module, "migrations", provider, "manifest.json")
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var history migration.Manifest
	if err := json.Unmarshal(encoded, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Entries) == 0 || len(history.Entries[0].Operations) == 0 {
		t.Fatal("unknown-operation fixture has no operation")
	}
	history.Entries[0].Operations[0].Kind = migration.OperationKind("futureUnknownKind")
	history.Entries[0].ChainHash = ""
	history.Entries[0].ChainHash = migration.ChainHash(history.Entries[0])
	encoded, err = json.MarshalIndent(history, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(module, "migrations", workflowPublicationFilenameForTest())
	publicationBytes, err := os.ReadFile(publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	var publication codegenmanifest.Manifest
	if err := json.Unmarshal(publicationBytes, &publication); err != nil {
		t.Fatal(err)
	}
	manifestRelative := filepath.ToSlash(filepath.Join("migrations", provider, "manifest.json"))
	for index := range publication.Artifacts {
		if publication.Artifacts[index].Path == manifestRelative {
			publication.Artifacts[index].ContentSHA256 = codegenmanifest.ContentHash(encoded)
		}
	}
	publicationBytes, err = json.MarshalIndent(publication, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	publicationBytes = append(publicationBytes, '\n')
	if err := os.WriteFile(publicationPath, publicationBytes, 0o644); err != nil {
		t.Fatal(err)
	}
}
