package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/migration/explain"
)

func TestSwitchingToKernelModeRemovesOnlyTheGraphQLLayer(t *testing.T) {
	t.Parallel()
	module := writeSingleProviderModule(t)
	createInitialReviewedMigration(t, module)
	runP8Command(t, module, "generate", "--schema", ".", "--app-out", "./app")
	manifestPath := filepath.Join(module, filepath.FromSlash(manifest.DefaultPath))
	graphqlManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	migrationsBefore := treeSnapshot(t, filepath.Join(module, "migrations"))
	databasePath := filepath.Join(t.TempDir(), "kernel.db")
	runP8Command(t, module, "migration", "apply", "--provider", "sqlite", "--dsn", databasePath)

	setKernelMode(t, module, true)
	generated := runGenerateJSON(t, module, "generate")
	stale := append([]string(nil), generated.Stale...)
	sort.Strings(stale)
	want := []string{
		"app/golemgqlgen/zz_golem_graphql_exec.gen.go",
		"app/golemgqlgen/zz_golem_graphql_models.gen.go",
		"app/golemgqlgen/zz_golem_graphql_resolvers.gen.go",
		"app/zz_golem_graphql.gen.go",
		"app/zz_golem_graphql.schema.graphqls",
	}
	if !reflect.DeepEqual(stale, want) {
		t.Fatalf("stale after switching to kernel mode = %v, want %v", stale, want)
	}
	for _, path := range want {
		if _, err := os.Stat(filepath.Join(module, filepath.FromSlash(path))); !os.IsNotExist(err) {
			t.Fatalf("kernel generation left %s on disk: %v", path, err)
		}
	}
	kernelManifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	kernelManifest, err := manifest.Parse(kernelManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range kernelManifest.Artifacts {
		if entry.Kind == manifest.ArtifactGraphQLGo || entry.Kind == manifest.ArtifactGraphQLSDL {
			t.Fatalf("kernel manifest lists GraphQL artifact %s", entry.Path)
		}
	}
	if checked := runGenerateJSON(t, module, "check"); !checked.Checked || len(checked.Changed) != 0 || len(checked.Stale) != 0 {
		t.Fatalf("kernel check = %#v", checked)
	}

	var stdout, stderr bytes.Buffer
	if code := runGolem(t, module, []string{"migration", "plan", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("migration plan code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var plan migrationPlanJSON
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Status != string(explain.StatusNoChanges) {
		t.Fatalf("switching modes left migration work pending: %s", stdout.String())
	}
	if after := treeSnapshot(t, filepath.Join(module, "migrations")); !reflect.DeepEqual(migrationsBefore, after) {
		t.Fatal("switching modes changed reviewed migration history")
	}
	doctor := assertDoctorState(t, module, "sqlite", databasePath, doctorState{capabilities: "pass", history: "current", schema: "current", generation: "current"})
	if !containsDoctorDiagnostic(doctor.Diagnostics, "GOLEM_DOCTOR_GENERATION_CURRENT", "info") {
		t.Fatalf("kernel doctor diagnostics = %#v", doctor.Diagnostics)
	}

	setKernelMode(t, module, false)
	restored := runGenerateJSON(t, module, "generate")
	if len(restored.Stale) != 0 {
		t.Fatalf("switching back to GraphQL reported stale artifacts %v", restored.Stale)
	}
	restoredManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredManifest, graphqlManifest) {
		t.Fatal("switching back to GraphQL did not restore the original manifest byte for byte")
	}
}

func setKernelMode(t *testing.T, module string, kernel bool) {
	t.Helper()
	filename := filepath.Join(module, "schema.go")
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	graphql := []byte("golem.SchemaName(schema, \"social_cli\")\n")
	kernelDeclaration := []byte("golem.SchemaName(schema, \"social_cli\")\n\tgolem.WithoutGraphQL(schema)\n")
	if kernel {
		content = bytes.Replace(content, graphql, kernelDeclaration, 1)
	} else {
		content = bytes.Replace(content, kernelDeclaration, graphql, 1)
	}
	if bytes.Contains(content, []byte("WithoutGraphQL")) != kernel {
		t.Fatalf("schema mode was not switched:\n%s", content)
	}
	if err := os.WriteFile(filename, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGenerateJSON(t *testing.T, module, command string) generateOutput {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := runGolem(t, module, []string{command, "--schema", ".", "--app-out", "./app"}, &stdout, &stderr); code != 0 {
		t.Fatalf("%s code=%d stdout=%s stderr=%s", command, code, stdout.String(), stderr.String())
	}
	var output generateOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	return output
}
