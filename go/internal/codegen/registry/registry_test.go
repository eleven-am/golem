package registry

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestEmitApplicationRegistryDeterministic(t *testing.T) {
	request := completeRequest(t, Request{
		AppPackage:    modelcodegen.PackageSpec{ImportPath: "example.test/generated/app", PackageName: "app"},
		ModelPackages: []modelcodegen.PackageSpec{{ImportPath: "example.test/models/z", PackageName: "z"}, {ImportPath: "example.test/models/a", PackageName: "a"}},
		Actor:         ir.GoNamedTypeIR{PackagePath: "example.test/security", Name: "Actor"}, GenerationDigest: strings.Repeat("b", 64),
	})
	first, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	request.ModelPackages[0], request.ModelPackages[1] = request.ModelPackages[1], request.ModelPackages[0]
	second, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Source, second.Source) {
		t.Fatalf("shuffle changed registry\n%s\n%s", first.Source, second.Source)
	}
	source := string(first.Source)
	if strings.Index(source, `"example.test/models/a"`) > strings.Index(source, `"example.test/models/z"`) {
		t.Fatal("model accessors are not ordered by import path")
	}
	for _, fragment := range []string{"func golemGeneratedGenerationDigest() golem.SchemaDigest", "func GolemGeneratedApplicationBindings() (golem.ApplicationBindings[actorpkg.Actor], error)", "golem.GeneratedApplicationBindings(golemGeneratedGenerationDigest(),", "models.GolemGeneratedBindings()", "models2.GolemGeneratedBindings()", "func GolemGeneratedApplicationDescriptors() (golem.ApplicationDescriptors, error)", "golem.GeneratedApplicationDescriptors(golemGeneratedGenerationDigest(),", "models.GolemGeneratedDescriptors()", "models2.GolemGeneratedDescriptors()", "func GolemGeneratedSchemaBundle()", "ReadLimits       golemruntime.ReadLimits", "MutationLimits   golemruntime.MutationLimits", "AfterCommitError func(context.Context, golem.AfterCommitFailure)", "ReadLimits: config.ReadLimits", "MutationLimits: config.MutationLimits", "AfterCommitError: config.AfterCommitError", "type CallerTx[P any] struct", "type SystemTx[P any] struct", "func (caller *Caller[P]) Transaction(", "func (system System[P]) Transaction(", "SnapshotActor", "SnapshotActor: config.SnapshotActor", "Golem generation digest:"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("source missing %q:\n%s", fragment, source)
		}
	}
	if strings.Contains(source, "func init(") {
		t.Fatal("registry emitted global init registration")
	}
}

func TestEmitCompilesWhenActorModelsAndAppSharePackage(t *testing.T) {
	spec := modelcodegen.PackageSpec{ImportPath: "example.test/app", PackageName: "app"}
	file, err := Emit(completeRequest(t, Request{AppPackage: spec, ModelPackages: []modelcodegen.PackageSpec{spec}, Actor: ir.GoNamedTypeIR{PackagePath: spec.ImportPath, Name: "Actor"}, GenerationDigest: strings.Repeat("a", 64)}))
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	if strings.Contains(source, `example.test/app`) || !strings.Contains(source, "ApplicationBindings[Actor]") || !strings.Contains(source, "GolemGeneratedBindings()") {
		t.Fatalf("same-package registry used a self import or lost local identities:\n%s", source)
	}
	compileRegistry(t, map[string]string{
		"app/app.go": `package app
import golem "github.com/eleven-am/golem/go/golem"
type Actor struct{}
func GolemGeneratedBindings() golem.PackageBindings[Actor] {
	return golem.GeneratedPackageBindings[Actor](nil, nil)
}
func GolemGeneratedDescriptors() golem.PackageDescriptors { return golem.GeneratedPackageDescriptors() }
`,
		"app/registry_test.go": `package app
import "testing"
func TestFinalRegistryRejectsUnstampedBootstrapPackages(t *testing.T) {
	if _, err := GolemGeneratedApplicationBindings(); err == nil { t.Fatal("unstamped bindings accepted") }
	if _, err := GolemGeneratedApplicationDescriptors(); err == nil { t.Fatal("unstamped descriptors accepted") }
}
`,
	}, "app/"+Filename, file.Source)
}

func TestEmitCompilesWithSeparateActorAndMultipleModelPackages(t *testing.T) {
	file, err := Emit(completeRequest(t, Request{
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.test/app/generated", PackageName: "generated"},
		ModelPackages: []modelcodegen.PackageSpec{
			{ImportPath: "example.test/app/modelb", PackageName: "modelb"},
			{ImportPath: "example.test/app/modela", PackageName: "modela"},
		},
		Actor: ir.GoNamedTypeIR{PackagePath: "example.test/app/security", Name: "Actor"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	compileRegistry(t, map[string]string{
		"security/actor.go": "package security\ntype Actor struct{}\n",
		"modela/model.go":   packageBindingsSource("modela"),
		"modelb/model.go":   packageBindingsSource("modelb"),
		"generated/doc.go":  "package generated\n",
	}, "generated/"+Filename, file.Source)
}

func TestSchemaBundleEmbedsExactCanonicalDocumentsInProviderOrderAndRejectsMismatches(t *testing.T) {
	model := ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Providers: []ir.Provider{ir.PostgreSQL, ir.SQLite}}
	contract := ir.ContractIR{FormatVersion: ir.ContractFormatVersion}
	modelFingerprint, _ := ir.ModelFingerprint(model)
	contractFingerprint, _ := ir.ContractFingerprint(contract)
	sqlite := normalizedProviderSchema(t, physical.SQLiteManifest(), "main")
	postgres := normalizedProviderSchema(t, physical.PostgreSQLManifest(), "public")
	sqliteFingerprint, _ := physical.PhysicalFingerprint(sqlite)
	postgresFingerprint, _ := physical.PhysicalFingerprint(postgres)
	sqliteSystemFingerprint, _ := physical.SystemFingerprint(sqlite.Provider, sqlite.System)
	postgresSystemFingerprint, _ := physical.SystemFingerprint(postgres.Provider, postgres.System)
	request := Request{
		AppPackage:       modelcodegen.PackageSpec{ImportPath: "example.test/app", PackageName: "app"},
		Actor:            ir.GoNamedTypeIR{PackagePath: "example.test/security", Name: "Actor"},
		GenerationDigest: strings.Repeat("a", 64), GeneratorVersion: "test-generator", TemplateABIVersion: "test-template",
		Schema: SchemaInput{Model: model, Contract: contract, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, Providers: []ProviderInput{
			{Schema: postgres, Fingerprint: ir.Fingerprint(postgresFingerprint.String()), SystemFingerprint: ir.Fingerprint(postgresSystemFingerprint.String())},
			{Schema: sqlite, Fingerprint: ir.Fingerprint(sqliteFingerprint.String()), SystemFingerprint: ir.Fingerprint(sqliteSystemFingerprint.String())},
		}},
	}
	prepared, err := prepareSchemaBundle(request)
	if err != nil {
		t.Fatal(err)
	}
	wantModel, _ := ir.CanonicalModel(model)
	wantContract, _ := ir.CanonicalContract(contract)
	wantSQLite, _ := physical.CanonicalEncode(sqlite)
	wantPostgres, _ := physical.CanonicalEncode(postgres)
	if !bytes.Equal(prepared.model.payload, wantModel) || !bytes.Equal(prepared.contract.payload, wantContract) {
		t.Fatal("logical bundle documents differ from canonical IR encodings")
	}
	if len(prepared.providers) != 2 || prepared.providers[0].provider != ir.PostgreSQL || prepared.providers[1].provider != ir.SQLite {
		t.Fatalf("provider order=%v", []ir.Provider{prepared.providers[0].provider, prepared.providers[1].provider})
	}
	// Provider identity order is lexical and therefore PostgreSQL precedes
	// SQLite, regardless of request order.
	if !bytes.Equal(prepared.providers[0].document.payload, wantPostgres) || !bytes.Equal(prepared.providers[1].document.payload, wantSQLite) {
		t.Fatal("physical bundle documents differ from canonical PhysicalSchema encodings")
	}
	file, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	for _, document := range []preparedDocument{prepared.model, prepared.contract, prepared.providers[0].document, prepared.providers[1].document} {
		if !strings.Contains(source, documentLiteral("golem", document)) {
			t.Fatal("generated registry does not contain an exact canonical document literal")
		}
	}
	for _, provider := range prepared.providers {
		if !strings.Contains(source, digestLiteral("golem", provider.systemFingerprint)) {
			t.Fatal("generated registry does not bind the system schema fingerprint")
		}
	}
	if strings.Index(source, "golem.PostgreSQL") > strings.Index(source, "golem.SQLite") {
		t.Fatal("generated provider bundle order is not deterministic")
	}

	t.Run("model", func(t *testing.T) {
		tampered := request
		tampered.Schema.Model.Schema.StableName = "tampered"
		if _, err := Emit(tampered); err == nil || !strings.Contains(err.Error(), "model blob/fingerprint mismatch") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("contract", func(t *testing.T) {
		tampered := request
		tampered.Schema.Contract.Methods = []ir.AttachedMethodIR{{Name: "Tampered"}}
		if _, err := Emit(tampered); err == nil || !strings.Contains(err.Error(), "contract blob/fingerprint mismatch") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("physical", func(t *testing.T) {
		tampered := request
		tampered.Schema.Providers = append([]ProviderInput(nil), request.Schema.Providers...)
		tampered.Schema.Providers[0].Schema.Namespace.Name = "changed"
		if _, err := Emit(tampered); err == nil || !strings.Contains(err.Error(), "blob/fingerprint mismatch") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("system", func(t *testing.T) {
		tampered := request
		tampered.Schema.Providers = append([]ProviderInput(nil), request.Schema.Providers...)
		tampered.Schema.Providers[0].Schema.System.Objects = append([]physical.SystemObject(nil), request.Schema.Providers[0].Schema.System.Objects...)
		tampered.Schema.Providers[0].Schema.System.Objects[0].Name = "_golem_migrations_changed"
		before, _ := physical.PhysicalFingerprint(request.Schema.Providers[0].Schema)
		after, _ := physical.PhysicalFingerprint(tampered.Schema.Providers[0].Schema)
		if before != after {
			t.Fatal("test mutation unexpectedly changed the application physical fingerprint")
		}
		if _, err := Emit(tampered); err == nil || !strings.Contains(err.Error(), "system blob/fingerprint mismatch") {
			t.Fatalf("error=%v", err)
		}
	})
}

func completeRequest(t *testing.T, request Request) Request {
	t.Helper()
	model := ir.ModelIR{FormatVersion: ir.ModelFormatVersion}
	contract := ir.ContractIR{FormatVersion: ir.ContractFormatVersion}
	request.Schema.Model = model
	request.Schema.Contract = contract
	request.Schema.ModelFingerprint, _ = ir.ModelFingerprint(model)
	request.Schema.ContractFingerprint, _ = ir.ContractFingerprint(contract)
	if request.GenerationDigest == "" {
		request.GenerationDigest = strings.Repeat("0", 64)
	}
	if request.GeneratorVersion == "" {
		request.GeneratorVersion = "test-generator"
	}
	if request.TemplateABIVersion == "" {
		request.TemplateABIVersion = "test-template"
	}
	return request
}

func normalizedProviderSchema(t *testing.T, provider physical.ProviderManifest, namespace physical.PhysicalName) physical.PhysicalSchema {
	t.Helper()
	systemNamespace := physical.PhysicalName("_golem")
	if provider.Provider == ir.SQLite {
		systemNamespace = "main"
	}
	schema, err := physical.Normalize(physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: provider, Namespace: physical.Namespace{Name: namespace},
		System: physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: systemNamespace}, Objects: []physical.SystemObject{
			{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
			{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func packageBindingsSource(packageName string) string {
	return fmt.Sprintf(`package %s
import (
	golem "github.com/eleven-am/golem/go/golem"
	"example.test/app/security"
)
func GolemGeneratedBindings() golem.PackageBindings[security.Actor] {
	return golem.GeneratedPackageBindings[security.Actor](nil, nil)
}
func GolemGeneratedDescriptors() golem.PackageDescriptors { return golem.GeneratedPackageDescriptors() }
`, packageName)
}

func compileRegistry(t *testing.T, handwritten map[string]string, generatedPath string, generated []byte) {
	t.Helper()
	root := t.TempDir()
	module := fmt.Sprintf("module example.test/app\n\ngo 1.23\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", moduleDir(t))
	writeRegistryTestFile(t, filepath.Join(root, "go.mod"), module)
	for path, source := range handwritten {
		writeRegistryTestFile(t, filepath.Join(root, path), source)
	}
	writeRegistryTestFile(t, filepath.Join(root, generatedPath), string(generated))
	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated application registry does not compile: %v\n%s", err, output)
	}
}

func writeRegistryTestFile(t *testing.T, path, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func moduleDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}
