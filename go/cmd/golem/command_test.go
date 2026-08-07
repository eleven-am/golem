package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/publication"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestUsageErrorsExitTwoWithoutOutput(t *testing.T) {
	for _, arguments := range [][]string{nil, {"unknown"}, {"generate", "--schema", "."}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), t.TempDir(), arguments, &stdout, &stderr); code != 2 {
			t.Fatalf("arguments=%v code=%d stderr=%s", arguments, code, stderr.String())
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("arguments=%v stdout=%q stderr=%q", arguments, stdout.String(), stderr.String())
		}
	}
}

func TestInspectSocialGoldenAndDeterminism(t *testing.T) {
	root := commandModuleRoot(t)
	args := []string{"inspect", "--schema", "./cmd/golem/testdata/social"}
	var first, second, firstErr, secondErr bytes.Buffer
	if code := run(context.Background(), root, args, &first, &firstErr); code != 0 {
		t.Fatalf("first inspect code=%d stderr=%s", code, firstErr.String())
	}
	if code := run(context.Background(), root, args, &second, &secondErr); code != 0 {
		t.Fatalf("second inspect code=%d stderr=%s", code, secondErr.String())
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("inspect output changed across identical runs")
	}
	// A compact golden pins the complete normalized JSON byte stream while the
	// structural assertions below explain the contract it represents.
	sum := sha256.Sum256(first.Bytes())
	if got, want := hex.EncodeToString(sum[:]), "28d6db51acb7e16dbfa1bb97902faf73a444061a713749719733f294f9cd7571"; got != want {
		t.Fatalf("inspect golden digest = %s; want %s", got, want)
	}
	var output inspectOutput
	if err := json.Unmarshal(first.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.FormatVersion != 2 || output.Contract.FormatVersion != ir.ContractFormatVersion || output.Contract.GraphQLABIVersion != 2 || len(output.Model.Models) != 6 || len(output.Model.Relations) != 8 || len(output.Contract.Models) != 6 || len(output.Contract.Methods) != 6 || len(output.Providers) != 2 || len(output.Policies) != 6 || len(output.PolicyOperators) != 41 {
		t.Fatalf("inspect output is incomplete: %#v", output)
	}
	for _, entry := range output.PolicyOperators {
		if entry.ID == 0 || entry.Name == "" || len(entry.DeclaredProviders) != 2 || len(entry.AgreementProviders) != 2 || !entry.TwoValued {
			t.Fatalf("inspect operator inventory is incomplete: %#v", entry)
		}
	}
	if output.ModelFingerprint == "" || output.ContractFingerprint == "" || output.GenerationDigest == "" {
		t.Fatal("inspect omitted fingerprints")
	}
	names := make([]string, 0, len(output.Model.Models))
	models := make(map[string]ir.ModelDeclIR, len(output.Model.Models))
	for _, model := range output.Model.Models {
		names = append(names, model.Go.Name)
		models[model.Go.Name] = model
		if model.ID == "" || model.PrimaryKey == nil || len(model.Fields) == 0 {
			t.Fatalf("model lacks stable identity/key/fields: %#v", model)
		}
		for _, field := range model.Fields {
			if field.ID == "" || field.Kind == "" || field.Scalar != nil && field.Scalar.Type.Kind == "" {
				t.Fatalf("field lacks exact normalized metadata: %#v", field)
			}
		}
	}
	sort.Strings(names)
	if expected := []string{"Comment", "Friendship", "Post", "PostTag", "Tag", "User"}; !reflect.DeepEqual(names, expected) {
		t.Fatalf("social model inventory = %v; want %v", names, expected)
	}
	if len(models["Friendship"].PrimaryKey.Fields) != 2 || len(models["PostTag"].PrimaryKey.Fields) != 2 {
		t.Fatal("social composite primary keys were not preserved")
	}
	friendshipIndex := models["Friendship"].Indexes
	if len(friendshipIndex) != 1 || len(friendshipIndex[0].Keys) != 2 || friendshipIndex[0].Keys[0].Column == nil || friendshipIndex[0].Keys[1].Column == nil || *friendshipIndex[0].Keys[0].Column == *friendshipIndex[0].Keys[1].Column {
		t.Fatalf("ordered friendship index was not preserved: %#v", friendshipIndex)
	}
	recursive := false
	for _, relation := range output.Model.Relations {
		recursive = recursive || relation.Name == "ReplyTree" && relation.SourceModel == relation.TargetModel
	}
	if !recursive {
		t.Fatal("recursive Comment relation is absent")
	}
	for _, contract := range output.Contract.Models {
		if !contract.Exposed || contract.GraphQLName == "" || len(contract.Fields) == 0 || contract.Roots.RelationGroupBy == "" {
			t.Fatalf("incomplete exposure contract: %#v", contract)
		}
		if contract.Aggregation != nil || contract.ScopedReads {
			t.Fatalf("plain social fixture unexpectedly enabled P6 analytics or scoped reads: %#v", contract)
		}
	}
	foundDefault, foundNullable, foundIndex := false, false, false
	for _, model := range output.Model.Models {
		foundIndex = foundIndex || len(model.Indexes) != 0
		for _, field := range model.Fields {
			if field.Scalar != nil {
				foundDefault = foundDefault || field.Scalar.Default != nil
				foundNullable = foundNullable || field.Scalar.Nullable
			}
		}
	}
	if !foundDefault || !foundNullable || !foundIndex {
		t.Fatalf("inspect omitted defaults/nullability/indexes: default=%v nullable=%v index=%v", foundDefault, foundNullable, foundIndex)
	}
	if output.Providers[0].Provider.Provider != ir.PostgreSQL || output.Providers[1].Provider.Provider != ir.SQLite {
		t.Fatalf("providers are not canonical: %#v", output.Providers)
	}
	for _, provider := range output.Providers {
		if provider.Fingerprint == "" || provider.SystemFingerprint == "" {
			t.Fatalf("inspect provider output omitted application or system fingerprint: %#v", provider)
		}
		if len(provider.Schema.System.Objects) != 4 {
			t.Fatalf("inspect provider output omitted a system object: %#v", provider.Schema.System)
		}
		foundOutbox, foundUpsertGuard := false, false
		for _, object := range provider.Schema.System.Objects {
			foundOutbox = foundOutbox || object.Kind == physical.SystemOutbox && physical.IsOutboxSystemObjectV1(object)
			foundUpsertGuard = foundUpsertGuard || object.Kind == physical.SystemUpsertGuard && physical.IsUpsertGuardSystemObjectV1(object)
		}
		if !foundOutbox || !foundUpsertGuard {
			t.Fatalf("inspect provider output omitted P4 system objects: %#v", provider.Schema.System)
		}
	}
	if _, err := os.Stat(filepath.Join(root, manifest.DefaultPath)); !os.IsNotExist(err) {
		t.Fatalf("inspect wrote a generated manifest: %v", err)
	}
}

func TestSingleProviderAndDefaultSchemaPattern(t *testing.T) {
	module := writeSingleProviderModule(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"inspect"}, &stdout, &stderr); code != 0 {
		t.Fatalf("inspect code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var inspected inspectOutput
	if err := json.Unmarshal(stdout.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected.Providers) != 1 || inspected.Providers[0].Provider.Provider != ir.SQLite {
		t.Fatalf("single-provider inspect lowered %#v", inspected.Providers)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestMigrationNewIncrementalApplyAndAlreadyCurrent(t *testing.T) {
	module := writeSingleProviderModule(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "initial"}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial migration code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var initial migrationNewOutput
	if err := json.Unmarshal(stdout.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.MigrationID != "0001_initial" || !reflect.DeepEqual(initial.Providers, []ir.Provider{ir.SQLite}) {
		t.Fatalf("initial migration output = %#v", initial)
	}
	for _, name := range []string{
		"migrations/.golem-publication.json",
		"migrations/models/0001_initial.before.snapshot.json",
		"migrations/models/0001_initial.after.snapshot.json",
		"migrations/sqlite/0001_initial.sql",
		"migrations/sqlite/0001_initial.before.snapshot.json",
		"migrations/sqlite/0001_initial.after.snapshot.json",
		"migrations/sqlite/manifest.json",
	} {
		if _, err := os.Stat(filepath.Join(module, filepath.FromSlash(name))); err != nil {
			t.Fatalf("missing migration artifact %s: %v", name, err)
		}
	}

	schemaPath := filepath.Join(module, "schema.go")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	schema = bytes.Replace(schema,
		[]byte("\tID int64 `db:\"id\" golem:\"id=social.User.ID;pk\"`"),
		[]byte("\tID int64 `db:\"id\" golem:\"id=social.User.ID;pk\"`\n\tEmail *string `db:\"email\" golem:\"id=social.User.Email\"`"), 1)
	if err := os.WriteFile(schemaPath, schema, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"generate", "check"} {
		stdout.Reset()
		stderr.Reset()
		if code := run(context.Background(), module, []string{command, "--app-out", "./app"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "differs from the reviewed migration head") {
			t.Fatalf("pending-schema %s code=%d stdout=%s stderr=%s", command, code, stdout.String(), stderr.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "add_email"}, &stdout, &stderr); code != 0 {
		t.Fatalf("incremental migration code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var incremental migrationNewOutput
	if err := json.Unmarshal(stdout.Bytes(), &incremental); err != nil || incremental.MigrationID != "0002_add_email" {
		t.Fatalf("incremental migration output=%#v err=%v", incremental, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generation after reviewed migration code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	concurrentDatabase := filepath.Join(t.TempDir(), "concurrent.db")
	type commandResult struct {
		code   int
		stdout string
		stderr string
	}
	results := make([]commandResult, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			var commandOut, commandErr bytes.Buffer
			arguments := []string{"migration", "apply", "--provider", "sqlite", "--dsn", concurrentDatabase}
			results[index] = commandResult{code: run(context.Background(), module, arguments, &commandOut, &commandErr), stdout: commandOut.String(), stderr: commandErr.String()}
		}(index)
	}
	wait.Wait()
	for index, result := range results {
		if result.code != 0 {
			t.Fatalf("concurrent apply %d code=%d stdout=%s stderr=%s", index, result.code, result.stdout, result.stderr)
		}
	}
	var concurrentOut, concurrentErr bytes.Buffer
	concurrentArgs := []string{"migration", "apply", "--provider", "sqlite", "--dsn", concurrentDatabase}
	if code := run(context.Background(), module, concurrentArgs, &concurrentOut, &concurrentErr); code != 0 {
		t.Fatalf("concurrent final verification code=%d stderr=%s", code, concurrentErr.String())
	}
	var concurrentCurrent migrationApplyOutput
	if err := json.Unmarshal(concurrentOut.Bytes(), &concurrentCurrent); err != nil || len(concurrentCurrent.Applied) != 0 {
		t.Fatalf("concurrent final output=%#v err=%v", concurrentCurrent, err)
	}

	database := filepath.Join(t.TempDir(), "application.db")
	stdout.Reset()
	stderr.Reset()
	applyArgs := []string{"migration", "apply", "--provider", "sqlite", "--dsn", database}
	if code := run(context.Background(), module, applyArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("apply code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var applied migrationApplyOutput
	if err := json.Unmarshal(stdout.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Provider != ir.SQLite || !reflect.DeepEqual(applied.Applied, []migration.MigrationID{"0001_initial", "0002_add_email"}) {
		t.Fatalf("apply output = %#v", applied)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, applyArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("already-current apply code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &applied); err != nil || len(applied.Applied) != 0 {
		t.Fatalf("already-current apply output=%#v err=%v", applied, err)
	}
}

func TestMigrationHistoryTamperFailsBeforeDatabaseWorkAndCheckReadsHistory(t *testing.T) {
	module := writeSingleProviderModule(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "initial"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"check", "--app-out", "./app", "--migrations", "migrations"}, &stdout, &stderr); code != 0 {
		t.Fatalf("check reviewed history code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	sqlPath := filepath.Join(module, "migrations", "sqlite", "0001_initial.sql")
	content, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sqlPath, append(content, []byte("-- rewritten\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(t.TempDir(), "must-not-exist.db")
	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), module, []string{"migration", "apply", "--provider", "sqlite", "--dsn", database}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "rewritten") {
		t.Fatalf("tampered apply code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database was touched before history verification: %v", err)
	}
}

func TestMigrationNewRequiresExactApprovalAndPublishesNothingOnFailure(t *testing.T) {
	module := writeSingleProviderModule(t)
	schemaPath := filepath.Join(module, "schema.go")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	withEmail := bytes.Replace(schema,
		[]byte("\tID int64 `db:\"id\" golem:\"id=social.User.ID;pk\"`"),
		[]byte("\tID int64 `db:\"id\" golem:\"id=social.User.ID;pk\"`\n\tEmail *string `db:\"email\" golem:\"id=social.User.Email\"`"), 1)
	if err := os.WriteFile(schemaPath, withEmail, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "initial"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if err := os.WriteFile(schemaPath, schema, 0o644); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, filepath.Join(module, "migrations"))
	stdout.Reset()
	stderr.Reset()
	args := []string{"migration", "new", "--name", "drop_email"}
	if code := run(context.Background(), module, args, &stdout, &stderr); code != 1 {
		t.Fatalf("missing approval code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	parts := strings.Fields(stderr.String())
	if len(parts) == 0 || !strings.Contains(stderr.String(), "requires --approve") {
		t.Fatalf("missing approval diagnostic = %q", stderr.String())
	}
	operationID := parts[len(parts)-1]
	if after := treeSnapshot(t, filepath.Join(module, "migrations")); !reflect.DeepEqual(before, after) {
		t.Fatal("failed approval check partially published migration history")
	}
	stdout.Reset()
	stderr.Reset()
	unknown := append(append([]string(nil), args...), "--approve", "unknown")
	if code := run(context.Background(), module, unknown, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "requires --approve") {
		t.Fatalf("unknown approval code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if after := treeSnapshot(t, filepath.Join(module, "migrations")); !reflect.DeepEqual(before, after) {
		t.Fatal("unknown approval partially published migration history")
	}
	stdout.Reset()
	stderr.Reset()
	approved := append(append([]string(nil), args...), "--approve", operationID)
	if code := run(context.Background(), module, approved, &stdout, &stderr); code != 0 {
		t.Fatalf("approved migration code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var output migrationNewOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.MigrationID != "0002_drop_email" {
		t.Fatalf("approved migration output=%#v err=%v", output, err)
	}
}

func TestCustomMigrationRootCarriesReviewedRenameIntoGeneration(t *testing.T) {
	module := writeSingleProviderModule(t)
	var stdout, stderr bytes.Buffer
	initialArgs := []string{"migration", "new", "--name", "initial", "--migrations", "reviewed/schema"}
	if code := run(context.Background(), module, initialArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("custom-root initial code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	schemaPath := filepath.Join(module, "schema.go")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	replacements := [][2]string{
		{"type User struct {", "type Member struct {"},
		{"golem:\"model;id=social.User;table=users\"", "golem:\"model;table=members;renameFrom=social.User\""},
		{"golem:\"id=social.User.ID;pk\"", "golem:\"pk;renameFrom=social.User.ID\""},
		{"golem.Model[User](schema)", "golem.Model[Member](schema)"},
		{"func (User) DefinePolicy", "func (Member) DefinePolicy"},
		{"golem.Rules[User]", "golem.Rules[Member]"},
	}
	for _, replacement := range replacements {
		next := bytes.Replace(schema, []byte(replacement[0]), []byte(replacement[1]), 1)
		if bytes.Equal(next, schema) {
			t.Fatalf("rename fixture did not contain %q", replacement[0])
		}
		schema = next
	}
	if err := os.WriteFile(schemaPath, schema, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./app"}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "P1_RENAME_HISTORY_REQUIRED") {
		t.Fatalf("default-root rename generation code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	renameArgs := []string{"migration", "new", "--name", "rename_member", "--migrations", "reviewed/schema"}
	if code := run(context.Background(), module, renameArgs, &stdout, &stderr); code != 0 {
		parts := strings.Fields(stderr.String())
		if code != 1 || len(parts) == 0 || !strings.Contains(stderr.String(), "requires --approve") {
			t.Fatalf("custom-root rename migration code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		renameArgs = append(renameArgs, "--approve", parts[len(parts)-1])
		stdout.Reset()
		stderr.Reset()
		if approvedCode := run(context.Background(), module, renameArgs, &stdout, &stderr); approvedCode != 0 {
			t.Fatalf("approved custom-root rename migration code=%d stdout=%s stderr=%s", approvedCode, stdout.String(), stderr.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	generateArgs := []string{"generate", "--app-out", "./app", "--migrations", "reviewed/schema"}
	if code := run(context.Background(), module, generateArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("custom-root rename generation code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestGenerateAndCheckRejectOrphanedDefaultMigrationRoot(t *testing.T) {
	for _, command := range []string{"generate", "check"} {
		t.Run(command, func(t *testing.T) {
			module := writeSingleProviderModule(t)
			orphan := filepath.Join(module, "migrations", "sqlite", "0001_orphan.sql")
			if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(orphan, []byte("SELECT 1;\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), module, []string{command, "--app-out", "./app"}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), "nonempty but has no") {
				t.Fatalf("orphaned %s code=%d stdout=%s stderr=%s", command, code, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(module, "app")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("orphaned history %s published generated output: %v", command, err)
			}
		})
	}
}

func TestInspectIgnoresPriorRegistryLocation(t *testing.T) {
	module := writeSocialModule(t, false)
	var before, after, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"inspect"}, &before, &stderr); code != 0 {
		t.Fatalf("inspect before generation code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./application"}, &bytes.Buffer{}, &stderr); code != 0 {
		t.Fatalf("generate code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(context.Background(), module, []string{"inspect"}, &after, &stderr); code != 0 {
		t.Fatalf("inspect after generation code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatal("prior application registry location changed normalized inspect output")
	}
}

func TestGenerateMovesRegistryBetweenGeneratedOnlyPackages(t *testing.T) {
	module := writeSocialModule(t, false)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./first"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first generate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	oldRegistry := filepath.Join(module, "first", "zz_golem_registry.gen.go")
	if _, err := os.Stat(oldRegistry); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./second"}, &stdout, &stderr); code != 0 {
		t.Fatalf("moved generate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(oldRegistry); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale registry remains after app-out move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(module, "second", "zz_golem_registry.gen.go")); err != nil {
		t.Fatalf("new registry was not published: %v", err)
	}
}

func TestGeneratePublishesThenCheckIsReadOnlyAndDeterministic(t *testing.T) {
	module := writeSocialModule(t, false)
	args := []string{"generate", "--schema", ".", "--app-out", "./app"}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, args, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var generated generateOutput
	if err := json.Unmarshal(stdout.Bytes(), &generated); err != nil {
		t.Fatal(err)
	}
	if generated.GenerationDigest == "" || len(generated.Changed) == 0 || generated.Checked {
		t.Fatalf("generate output = %#v", generated)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(module, filepath.FromSlash(manifest.DefaultPath)))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := manifest.Parse(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.GenerationDigest != generated.GenerationDigest {
		t.Fatal("published manifest digest differs from command result")
	}
	before := artifactContents(t, module, parsed)

	stdout.Reset()
	stderr.Reset()
	checkArgs := []string{"check", "--schema", ".", "--app-out", "./app"}
	if code := run(context.Background(), module, checkArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("check code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var checked generateOutput
	if err := json.Unmarshal(stdout.Bytes(), &checked); err != nil || !checked.Checked || len(checked.Changed) != 0 || len(checked.Stale) != 0 {
		t.Fatalf("check output=%#v error=%v", checked, err)
	}
	if after := artifactContents(t, module, parsed); !reflect.DeepEqual(before, after) {
		t.Fatal("journal-free check modified manifest-owned files")
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, args, &stdout, &stderr); code != 0 {
		t.Fatalf("repeat generate code=%d stderr=%s", code, stderr.String())
	}
	var repeated generateOutput
	if err := json.Unmarshal(stdout.Bytes(), &repeated); err != nil || repeated.GenerationDigest != generated.GenerationDigest || len(repeated.Changed) != 0 || len(repeated.Stale) != 0 {
		t.Fatalf("repeat generation = %#v, %v", repeated, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(module, "migrations", "*")); len(matches) != 0 {
		t.Fatalf("ordinary generation touched migration history: %v", matches)
	}
}

func TestDiagnosticsAndFailedCheckDoNotPublishPartialOutput(t *testing.T) {
	t.Run("diagnostics", func(t *testing.T) {
		module := writeSocialModule(t, true)
		before := treeSnapshot(t, module)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), module, []string{"generate", "--schema", ".", "--app-out", "./app"}, &stdout, &stderr)
		if code != 1 || !bytes.Contains(stdout.Bytes(), []byte("P1_BINDING_POLICY_SIGNATURE")) {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		if after := treeSnapshot(t, module); !reflect.DeepEqual(before, after) {
			t.Fatal("diagnostic generation modified the module")
		}
	})
	t.Run("stale check", func(t *testing.T) {
		module := writeSocialModule(t, false)
		var stdout, stderr bytes.Buffer
		generateArgs := []string{"generate", "--schema", ".", "--app-out", "./app"}
		if code := run(context.Background(), module, generateArgs, &stdout, &stderr); code != 0 {
			t.Fatal(stderr.String())
		}
		target := filepath.Join(module, "app", "zz_golem_registry.gen.go")
		tampered := []byte("// Code generated by golem. DO NOT EDIT.\npackage app\n")
		if err := os.WriteFile(target, tampered, 0o644); err != nil {
			t.Fatal(err)
		}
		before := treeSnapshot(t, module)
		stdout.Reset()
		stderr.Reset()
		code := run(context.Background(), module, []string{"check", "--schema", ".", "--app-out", "./app"}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "generated artifacts are stale") {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		if after := treeSnapshot(t, module); !reflect.DeepEqual(before, after) {
			t.Fatal("failed journal-free check modified module files")
		}
	})
}

func TestInspectDoesNotBypassProviderExtensionFailure(t *testing.T) {
	root := commandModuleRoot(t)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), root, []string{"inspect", "--schema", "./cmd/golem/testdata/extension"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "is PostgreSQL-scoped") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, manifest.DefaultPath)); !os.IsNotExist(err) {
		t.Fatalf("failed provider lowering wrote a manifest: %v", err)
	}
}

func TestCheckRecoversInterruptedPublicationBeforeOutputValidation(t *testing.T) {
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.test/recovery\n\ngo 1.23.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := func(body string) manifest.Result {
		t.Helper()
		result, err := manifest.Build(manifest.Request{
			ModelFingerprint: ir.Fingerprint("model"), ContractFingerprint: ir.Fingerprint("contract"),
			Artifacts: []manifest.Artifact{{
				Path: "generated/value.gen.go", Kind: manifest.ArtifactModelGo,
				Content: []byte(manifest.GeneratedHeader + "\npackage generated\n" + body), GeneratedHeader: manifest.GeneratedHeader,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if _, err := publication.Apply(context.Background(), publication.Request{ModuleDir: module, Prospective: build("const Value = 1\n")}); err != nil {
		t.Fatal(err)
	}
	injected := false
	_, err := publication.Apply(context.Background(), publication.Request{
		ModuleDir: module, Prospective: build("const Value = 2\n"),
		Inject: func(step publication.Step, _ string) error {
			if !injected && step == publication.StepJournaled {
				injected = true
				return errors.New("simulated crash")
			}
			return nil
		},
	})
	if err == nil || !injected {
		t.Fatalf("interrupted publication was not created: %v", err)
	}

	var stdout, stderr bytes.Buffer
	outside := filepath.Join(filepath.Dir(module), "outside")
	code := run(context.Background(), module, []string{"check", "--schema", ".", "--app-out", outside}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "must be inside module") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(module, ".golem", "generation-journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal was not recovered before output validation: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(module, "generated", "value.gen.go"))
	if err != nil || !bytes.Contains(content, []byte("Value = 1")) {
		t.Fatalf("recovery did not restore the old generation: content=%q err=%v", content, err)
	}
}

func TestCheckRoutesRecoveryForAnotherPublicationOwner(t *testing.T) {
	module := writeSingleProviderModule(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"generate", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"migration", "new", "--name", "initial"}, &stdout, &stderr); code != 0 {
		t.Fatalf("migration new code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	other, err := manifest.Build(manifest.Request{
		ModelFingerprint:    ir.Fingerprint(strings.Repeat("a", 64)),
		ContractFingerprint: ir.Fingerprint(strings.Repeat("b", 64)),
		Artifacts: []manifest.Artifact{{
			Path: "other/value.gen.go", Kind: manifest.ArtifactModelGo,
			Content: []byte(manifest.GeneratedHeader + "\npackage other\n"), GeneratedHeader: manifest.GeneratedHeader,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	injected := false
	_, err = publication.Apply(context.Background(), publication.Request{
		ModuleDir: module, ManifestPath: "other/manifest.json", Prospective: other,
		Inject: func(step publication.Step, _ string) error {
			if !injected && step == publication.StepJournaled {
				injected = true
				return errors.New("simulated unrelated publication crash")
			}
			return nil
		},
	})
	if err == nil || !injected {
		t.Fatalf("interrupted unrelated publication was not created: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), module, []string{"check", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatalf("check did not route recovery code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(module, ".golem", "generation-journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("routed command recovery left journal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(module, "other", "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back unrelated inventory remains: %v", err)
	}
}

func writeSocialModule(t *testing.T, invalidBinding bool) string {
	t.Helper()
	directory := t.TempDir()
	repository := commandModuleRoot(t)
	files := map[string]string{
		"go.mod": "module example.test/social\n\ngo 1.23.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(repository) + "\n",
	}
	actorType := "Actor"
	if invalidBinding {
		actorType = "string"
	}
	files["schema.go"] = `package social

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type User struct {
	_ struct{} ` + "`golem:\"model;id=social.User;table=users\"`" + `
	ID int64 ` + "`db:\"id\" golem:\"id=social.User.ID;pk\"`" + `
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "social_cli")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor ` + actorType + `) {
	_ = rules
	_ = actor
}
`
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		absolute := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(files[name]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func writeSingleProviderModule(t *testing.T) string {
	t.Helper()
	directory := writeSocialModule(t, false)
	filename := filepath.Join(directory, "schema.go")
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.Replace(content, []byte("golem.Providers(schema, golem.SQLite, golem.PostgreSQL)"), []byte("golem.Providers(schema, golem.SQLite)"), 1)
	if err := os.WriteFile(filename, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return directory
}

func artifactContents(t *testing.T, module string, value manifest.Manifest) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, entry := range value.Artifacts {
		content, err := os.ReadFile(filepath.Join(module, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Path] = string(content)
	}
	content, err := os.ReadFile(filepath.Join(module, filepath.FromSlash(manifest.DefaultPath)))
	if err != nil {
		t.Fatal(err)
	}
	result[manifest.DefaultPath] = string(content)
	return result
}

func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func commandModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
