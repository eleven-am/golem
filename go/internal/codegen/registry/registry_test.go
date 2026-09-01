package registry

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func TestEmitShellMatchesFinalCallerABI(t *testing.T) {
	actor := ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "Actor"}
	model := ir.ModelIR{
		FormatVersion: ir.ModelFormatVersion,
		Schema: ir.SchemaIdentityIR{
			ID: "example.test/schema", StableName: "test", PackagePath: "example.test/app", RootFunction: "DefineSchema", Actor: actor,
		},
		Models: []ir.ModelDeclIR{
			{
				ID: "example.test/User", CanonicalIdentity: "example.test/User",
				Go: ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "User"}, LogicalName: "User",
			},
			{
				ID: "example.test/AuditLog", CanonicalIdentity: "example.test/AuditLog",
				Go: ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "AuditLog"}, LogicalName: "AuditLog",
			},
		},
	}
	contract := ir.ContractIR{FormatVersion: ir.ContractFormatVersion}
	modelFingerprint, err := ir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := ir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	app := modelcodegen.PackageSpec{ImportPath: "example.test/app", PackageName: "app"}
	final, err := Emit(Request{
		AppPackage: app, ModelPackages: []modelcodegen.PackageSpec{app}, Actor: actor,
		GenerationDigest: strings.Repeat("0", 64), GeneratorVersion: "test-generator", TemplateABIVersion: "test-template",
		Schema: SchemaInput{Model: model, Contract: contract, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint},
	})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := EmitShell(ShellRequest{AppPackage: app, Actor: actor, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := EmitShell(ShellRequest{AppPackage: app, Actor: actor, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shell.Source, repeated.Source) {
		t.Fatal("registry bootstrap is not deterministic")
	}
	want := callerABI(t, final.Source)
	got := callerABI(t, shell.Source)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("registry bootstrap caller ABI differs from final registry\nbootstrap: %v\nfinal:     %v\n\n%s", got, want, shell.Source)
	}
	wantExports := []string{"type Caller", "type CallerAuditLogClient", "type CallerTx", "type CallerTxAuditLogClient", "type CallerTxUserClient", "type CallerUserClient"}
	if gotExports := registryTopLevelExports(t, shell.Source); fmt.Sprint(gotExports) != fmt.Sprint(wantExports) {
		t.Fatalf("registry bootstrap exposed unexpected top-level declarations\ngot:  %v\nwant: %v\n\n%s", gotExports, wantExports, shell.Source)
	}
	assertRegistryShellClosed(t, shell.Source, []string{"context", modelcodegen.DefaultGolemImportPath, strings.TrimSuffix(modelcodegen.DefaultGolemImportPath, "/golem") + "/queryplan", strings.TrimSuffix(modelcodegen.DefaultGolemImportPath, "/golem") + "/queue"})
	for _, forbidden := range []string{"System", "App", "Config", "sqlx", "database/sql", "golemruntime", " runtime ", " DB ", " Tx "} {
		if bytes.Contains(shell.Source, []byte(forbidden)) {
			t.Fatalf("registry bootstrap leaked forbidden capability %q:\n%s", forbidden, shell.Source)
		}
	}
}

func assertRegistryShellClosed(t *testing.T, source []byte, wantImports []string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "registry.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var imports []string
	for _, item := range file.Imports {
		path, err := strconv.Unquote(item.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		imports = append(imports, path)
	}
	sort.Strings(imports)
	sort.Strings(wantImports)
	if fmt.Sprint(imports) != fmt.Sprint(wantImports) {
		t.Fatalf("registry bootstrap import capability differs: got %v want %v", imports, wantImports)
	}
	for _, declaration := range file.Decls {
		switch value := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if !callerTypeName(typeSpec.Name.Name) {
					t.Fatalf("registry bootstrap contains non-caller type %s", typeSpec.Name.Name)
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("registry bootstrap caller type %s is not a struct", typeSpec.Name.Name)
				}
				if typeSpec.Name.Name != "Caller" && typeSpec.Name.Name != "CallerTx" && len(structure.Fields.List) != 0 {
					t.Fatalf("registry bootstrap client %s contains capabilities", typeSpec.Name.Name)
				}
				for _, field := range structure.Fields.List {
					if len(field.Names) == 0 {
						t.Fatalf("registry bootstrap %s contains embedded capability %s", typeSpec.Name.Name, registryExpr(t, field.Type))
					}
					for _, name := range field.Names {
						if !ast.IsExported(name.Name) {
							t.Fatalf("registry bootstrap %s contains hidden capability %s", typeSpec.Name.Name, name.Name)
						}
					}
				}
			}
		case *ast.FuncDecl:
			if value.Recv == nil {
				t.Fatalf("registry bootstrap contains package function %s", value.Name.Name)
			}
		}
	}
}

func callerABI(t *testing.T, source []byte) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "registry.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, declaration := range file.Decls {
		switch value := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || !callerTypeName(typeSpec.Name.Name) {
					continue
				}
				result = append(result, "type "+typeSpec.Name.Name+registryTypeParameters(t, typeSpec.TypeParams))
				if typeSpec.Name.Name != "Caller" && typeSpec.Name.Name != "CallerTx" {
					continue
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("%s is not a struct", typeSpec.Name.Name)
				}
				for _, field := range structure.Fields.List {
					for _, name := range field.Names {
						if ast.IsExported(name.Name) {
							result = append(result, "field "+typeSpec.Name.Name+"."+name.Name+" "+registryExpr(t, field.Type))
						}
					}
				}
			}
		case *ast.FuncDecl:
			if value.Recv == nil || len(value.Recv.List) != 1 {
				continue
			}
			receiver := registryExpr(t, value.Recv.List[0].Type)
			base := strings.TrimPrefix(receiver, "*")
			if index := strings.IndexByte(base, '['); index >= 0 {
				base = base[:index]
			}
			if !callerTypeName(base) {
				continue
			}
			result = append(result, "method "+receiver+"."+value.Name.Name+registryFieldList(t, value.Type.Params)+" "+registryFieldList(t, value.Type.Results))
		}
	}
	sort.Strings(result)
	return result
}

func callerTypeName(name string) bool {
	return name == "Caller" || name == "CallerTx" || strings.HasPrefix(name, "Caller") && strings.HasSuffix(name, "Client")
}

func registryTypeParameters(t *testing.T, fields *ast.FieldList) string {
	t.Helper()
	if fields == nil {
		return ""
	}
	value := registryFieldList(t, fields)
	return "[" + strings.TrimSuffix(strings.TrimPrefix(value, "("), ")") + "]"
}

func registryTopLevelExports(t *testing.T, source []byte) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "registry.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, declaration := range file.Decls {
		switch value := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				switch item := spec.(type) {
				case *ast.TypeSpec:
					if ast.IsExported(item.Name.Name) {
						result = append(result, "type "+item.Name.Name)
					}
				case *ast.ValueSpec:
					for _, name := range item.Names {
						if ast.IsExported(name.Name) {
							result = append(result, value.Tok.String()+" "+name.Name)
						}
					}
				}
			}
		case *ast.FuncDecl:
			if value.Recv == nil && ast.IsExported(value.Name.Name) {
				result = append(result, "func "+value.Name.Name)
			}
		}
	}
	sort.Strings(result)
	return result
}

func registryFieldList(t *testing.T, fields *ast.FieldList) string {
	t.Helper()
	if fields == nil {
		return "()"
	}
	values := make([]string, 0, len(fields.List))
	for _, field := range fields.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			values = append(values, registryExpr(t, field.Type))
		}
	}
	return "(" + strings.Join(values, ",") + ")"
}

func registryExpr(t *testing.T, expression ast.Expr) string {
	t.Helper()
	var result bytes.Buffer
	if err := format.Node(&result, token.NewFileSet(), expression); err != nil {
		t.Fatal(err)
	}
	return result.String()
}

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
	for _, fragment := range []string{"func golemGeneratedGenerationDigest() golem.SchemaDigest", "func GolemGeneratedApplicationBindings() (golem.ApplicationBindings[actorpkg.Actor], error)", "golem.GeneratedApplicationBindings(golemGeneratedGenerationDigest(),", "models.GolemGeneratedBindings()", "models2.GolemGeneratedBindings()", "func GolemGeneratedApplicationDescriptors() (golem.ApplicationDescriptors, error)", "golem.GeneratedApplicationDescriptors(golemGeneratedGenerationDigest(),", "models.GolemGeneratedDescriptors()", "models2.GolemGeneratedDescriptors()", "func GolemGeneratedSchemaBundle()", "*provider.Database", "Embeddings", "embedding.Registry", "Queue", "*golemruntime.QueueConfig", "engineConfig.Queue = config.Queue", "func (app *App[P]) RunQueueWorker", "func (app *App[P]) Enqueue", "func (app *App[P]) QueueOperator", "func (transaction *CallerTx[P]) Enqueue", "func (transaction *SystemTx[P]) Enqueue", "ReadLimits", "MutationLimits", "EventLimits", "EventTransport", "Observer", "CDCAdapters", "ReportEventOperator", "HistoricalEventBundles", "AfterCommitError", "AuditPrincipal", "ReportScopedQuery", "engineConfig.Embeddings = config.Embeddings", "engineConfig.ReadLimits = config.ReadLimits", "engineConfig.MutationLimits = config.MutationLimits", "engineConfig.EventLimits = config.EventLimits", "engineConfig.EventTransport = config.EventTransport", "engineConfig.Observer = config.Observer", "engineConfig.CDCAdapters = config.CDCAdapters", "engineConfig.ReportEventOperator = config.ReportEventOperator", "engineConfig.AfterCommitError = config.AfterCommitError", "engineConfig.AuditPrincipal = config.AuditPrincipal", "engineConfig.ReportScopedQuery = config.ReportScopedQuery", "func (app *App[P]) RunEventPublisher", "func (app *App[P]) RefreshSemanticIndexes", "func (app *App[P]) EventCapabilities", "func (app *App[P]) EventOperator", "func (app *App[P]) EventLimits", "type CallerTx[P any] struct", "type SystemTx[P any] struct", "func (caller *Caller[P]) Transaction(", "func (system System[P]) Transaction(", "SnapshotPrincipal", "SnapshotActor", "engineConfig.SnapshotActor = config.SnapshotActor", "Golem generation digest:"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("source missing %q:\n%s", fragment, source)
		}
	}
	for _, forbidden := range []string{"DB *sqlx.DB", "Provider golem.Provider", "GolemRuntimeTestOpenRaw"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("source contains forbidden legacy runtime surface %q:\n%s", forbidden, source)
		}
	}
	if strings.Contains(source, "func init(") {
		t.Fatal("registry emitted global init registration")
	}
}

func TestEmitSemanticIndexesAsTypedCallerAndSystemMethods(t *testing.T) {
	modelID := ir.ModelID("10000000000000000000000000000000")
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related_posts", Space: "content", Dimensions: 3, Fields: []string{"20000000000000000000000000000000"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	model := ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{{ID: modelID, CanonicalIdentity: string(modelID), Go: ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "Post"}, LogicalName: "Post"}}, Extensions: []ir.ProviderExtensionIR{
		{ID: "30000000000000000000000000000000", Provider: ir.SQLite, Version: 1, Owner: ir.ObjectID(modelID), Kind: semanticcontract.IndexKind, Payload: payload},
		{ID: "40000000000000000000000000000000", Provider: ir.PostgreSQL, Version: 1, Owner: ir.ObjectID(modelID), Kind: semanticcontract.IndexKind, Payload: payload},
	}}
	contract := ir.ContractIR{FormatVersion: ir.ContractFormatVersion}
	modelFingerprint, _ := ir.ModelFingerprint(model)
	contractFingerprint, _ := ir.ContractFingerprint(contract)
	file, err := Emit(Request{
		AppPackage: modelcodegen.PackageSpec{ImportPath: "example.test/app", PackageName: "app"}, ModelPackages: []modelcodegen.PackageSpec{{ImportPath: "example.test/app", PackageName: "app"}},
		Actor: ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "Actor"}, GenerationDigest: strings.Repeat("a", 64), GeneratorVersion: "test", TemplateABIVersion: "test",
		Schema: SchemaInput{Model: model, Contract: contract, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := string(file.Source)
	for _, fragment := range []string{"func (client CallerPostClient[P]) SearchRelatedPosts(", "golemruntime.CallerSearch", "func (client SystemPostClient[P]) SearchRelatedPosts(", "golemruntime.SystemSearch", `"related_posts"`} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("semantic generated surface missing %q:\n%s", fragment, source)
		}
	}
	for _, obsolete := range []string{"SimilarRelatedPosts(", "CallerSimilar", "SystemSimilar"} {
		if strings.Contains(source, obsolete) {
			t.Fatalf("semantic generated surface retained obsolete %q:\n%s", obsolete, source)
		}
	}
	if strings.Count(source, "SearchRelatedPosts(") != 2 {
		t.Fatalf("provider definitions duplicated semantic method:\n%s", source)
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
	module := fmt.Sprintf("module example.test/app\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => %s\n", moduleDir(t))
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
