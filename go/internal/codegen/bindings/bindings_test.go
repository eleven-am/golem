package bindings

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"golang.org/x/tools/go/packages"
)

const validPackage = "github.com/eleven-am/golem/go/internal/codegen/bindings/testdata/valid"
const invalidPackage = "github.com/eleven-am/golem/go/internal/codegen/bindings/testdata/invalid"
const actorPackage = "github.com/eleven-am/golem/go/internal/codegen/bindings/testdata/actor"
const systemPackage = "github.com/eleven-am/golem/go/internal/codegen/bindings/testdata/systemmodel"
const capabilityInvalidPackage = "github.com/eleven-am/golem/go/internal/codegen/bindings/testdata/capabilityinvalid"

func TestDiscoverAndEmitTypedBindingsCompile(t *testing.T) {
	compilation := bindingCompilation(validPackage, true)
	spec := modelcodegen.PackageSpec{ImportPath: validPackage, PackageName: "valid", Directory: fixtureDir(t, "valid")}
	bootstrap, err := modelcodegen.Emit(modelcodegen.Request{Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	result := DiscoverAndEmit(context.Background(), DiscoveryRequest{Dir: moduleDir(t), Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}, ModelBootstrap: bootstrap, GenerationDigest: strings.Repeat("a", 64)})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %#v", result.Diagnostics)
	}
	if len(result.Entries) != 5 || len(result.Methods) != 5 || len(result.Files) != 1 {
		t.Fatalf("result: entries=%#v methods=%#v files=%d", result.Entries, result.Methods, len(result.Files))
	}
	source := string(result.Files[0].Source)
	for _, fragment := range []string{"type PostCreateRequest = golem.CreateHookRequest[Post]", "func golemBuildPostPolicy", "func golemInvokePostBeforeCreate", "func GolemGeneratedBindings()", "golem.GeneratedStampedPackageBindings(golem.SchemaDigest{0xaa", "Golem generation digest:"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("generated source missing %q:\n%s", fragment, source)
		}
	}
	for _, forbidden := range []string{"func init(", "reflect.", "map[string]"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("generated source contains forbidden %q", forbidden)
		}
	}
	compileOverlay(t, validPackage, append(modelFiles(bootstrap), result.Files...))
}

func TestDiscoverRejectsMalformedRecognizedMethods(t *testing.T) {
	compilation := bindingCompilation(invalidPackage, true)
	spec := modelcodegen.PackageSpec{ImportPath: invalidPackage, PackageName: "invalid", Directory: fixtureDir(t, "invalid")}
	bootstrap, err := modelcodegen.Emit(modelcodegen.Request{Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	result := DiscoverAndEmit(context.Background(), DiscoveryRequest{Dir: moduleDir(t), Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}, ModelBootstrap: bootstrap})
	want := map[string]bool{"P1_BINDING_POLICY_SIGNATURE": false, "P1_BINDING_HOOK_SIGNATURE": false, "P1_BINDING_HOOK_FORBIDDEN": false, "P1_BINDING_POLICY_REQUIRED": false}
	for _, diagnostic := range result.Diagnostics {
		if _, ok := want[diagnostic.Code]; ok {
			want[diagnostic.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("missing %s in %#v", code, result.Diagnostics)
		}
	}
}

func TestBootstrapTypecheckRejectsIllegalPolicyCapabilities(t *testing.T) {
	compilation := capabilityInvalidCompilation()
	spec := modelcodegen.PackageSpec{ImportPath: capabilityInvalidPackage, PackageName: "capabilityinvalid", Directory: fixtureDir(t, "capabilityinvalid")}
	bootstrap, err := modelcodegen.Emit(modelcodegen.Request{Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	result := DiscoverAndEmit(context.Background(), DiscoveryRequest{Dir: moduleDir(t), Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}, ModelBootstrap: bootstrap})
	var messages []string
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != "P1_BINDING_TYPECHECK" {
			t.Errorf("diagnostic code=%q; want P1_BINDING_TYPECHECK: %#v", diagnostic.Code, diagnostic)
		}
		messages = append(messages, diagnostic.Message)
	}
	joined := strings.Join(messages, "\n")
	for _, stable := range []string{
		"Metadata.GTE",
		"Published.GT",
		"Status.Contains",
		"ID.IsNull",
		"Owner.Some",
		"Others.ID.Eq(actor.ID)",
	} {
		if !strings.Contains(joined, stable) {
			t.Errorf("missing stable typecheck substring %q in:\n%s", stable, joined)
		}
	}
}

func TestSeparateActorPackageLoadsForSystemOnlyModel(t *testing.T) {
	modelID := ir.ModelID("70000000000000000000000000000000")
	compilation := ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Schema: ir.SchemaIdentityIR{StableName: "system", Actor: ir.GoNamedTypeIR{PackagePath: actorPackage, Name: "Actor"}}, Models: []ir.ModelDeclIR{{ID: modelID, Go: ir.GoNamedTypeIR{PackagePath: systemPackage, Name: "Audit"}, LogicalName: "Audit", Fields: []ir.FieldIR{{ID: "71000000000000000000000000000000", GoName: "ID", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}}}}}}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{ModelID: modelID, Exposed: false}}}}
	spec := modelcodegen.PackageSpec{ImportPath: systemPackage, PackageName: "systemmodel", Directory: fixtureDir(t, "systemmodel")}
	bootstrap, err := modelcodegen.Emit(modelcodegen.Request{Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	result := DiscoverAndEmit(context.Background(), DiscoveryRequest{Dir: moduleDir(t), Compilation: compilation, Packages: []modelcodegen.PackageSpec{spec}, ModelBootstrap: bootstrap})
	if len(result.Diagnostics) != 0 || len(result.Files) != 1 {
		t.Fatalf("system-only discovery = %#v", result)
	}
	if !strings.Contains(string(result.Files[0].Source), "actorpkg.Actor") {
		t.Fatalf("separate actor type missing from accessor:\n%s", result.Files[0].Source)
	}
}

func TestEmitDeterministicUnderShuffle(t *testing.T) {
	compilation := bindingCompilation(validPackage, false)
	entries := []Entry{
		{ModelID: compilation.Model.Models[0].ID, PackagePath: validPackage, Receiver: "User", Method: "DefinePolicy", Kind: BindingPolicy},
		{ModelID: compilation.Model.Models[1].ID, PackagePath: validPackage, Receiver: "Post", Method: "BeforeCreate", Kind: BindingHook, Operation: OperationCreate, Phase: PhaseBefore},
	}
	spec := []modelcodegen.PackageSpec{{ImportPath: validPackage, PackageName: "valid"}}
	first, err := Emit(Request{Compilation: compilation, Packages: spec, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(compilation.Model.Models, func(i, j int) bool { return i > j })
	entries[0], entries[1] = entries[1], entries[0]
	second, err := Emit(Request{Compilation: compilation, Packages: spec, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first[0].Source, second[0].Source) {
		t.Fatalf("shuffle changed source\n%s\n%s", first[0].Source, second[0].Source)
	}
}

func bindingCompilation(packagePath string, exposed bool) ir.CompilationIR {
	actor := ir.GoNamedTypeIR{PackagePath: packagePath, Name: "Actor"}
	userID, postID := ir.ModelID("10000000000000000000000000000000"), ir.ModelID("20000000000000000000000000000000")
	field := func(id, name string) ir.FieldIR {
		return ir.FieldIR{ID: ir.FieldID(id), GoName: name, LogicalName: name, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: ir.SQLIdentifier(strings.ToLower(name)), Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}}
	}
	models := []ir.ModelDeclIR{{ID: userID, Go: ir.GoNamedTypeIR{PackagePath: packagePath, Name: "User"}, LogicalName: "User", Fields: []ir.FieldIR{field("11000000000000000000000000000000", "ID")}}}
	contracts := []ir.ModelContractIR{{ModelID: userID, Exposed: exposed}}
	if packagePath == validPackage {
		models = append(models, ir.ModelDeclIR{ID: postID, Go: ir.GoNamedTypeIR{PackagePath: packagePath, Name: "Post"}, LogicalName: "Post", Fields: []ir.FieldIR{field("21000000000000000000000000000000", "ID"), field("22000000000000000000000000000000", "AuthorID")}})
		contracts = append(contracts, ir.ModelContractIR{ModelID: postID, Exposed: exposed})
	}
	return ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Schema: ir.SchemaIdentityIR{StableName: "binding", Actor: actor}, Models: models}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: contracts}}
}

func capabilityInvalidCompilation() ir.CompilationIR {
	modelNames := []string{"JSONFailure", "BoolFailure", "EnumFailure", "NonNullFailure", "RelationFailure", "CrossFailure", "Other"}
	models := make([]ir.ModelDeclIR, 0, len(modelNames))
	contracts := make([]ir.ModelContractIR, 0, len(modelNames))
	modelIDs := map[string]ir.ModelID{}
	for index, name := range modelNames {
		modelID := ir.ModelID(fmt.Sprintf("%032x", 1000+index))
		modelIDs[name] = modelID
		models = append(models, ir.ModelDeclIR{ID: modelID, Go: ir.GoNamedTypeIR{PackagePath: capabilityInvalidPackage, Name: name}, LogicalName: name})
		contracts = append(contracts, ir.ModelContractIR{ModelID: modelID, Exposed: true})
	}
	field := func(index int, name string, logical ir.LogicalTypeIR) ir.FieldIR {
		return ir.FieldIR{ID: ir.FieldID(fmt.Sprintf("%032x", 2000+index)), GoName: name, LogicalName: name, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: ir.SQLIdentifier(strings.ToLower(name)), Type: logical}}
	}
	models[0].Fields = []ir.FieldIR{field(0, "Metadata", ir.LogicalTypeIR{Kind: ir.TypeJSON})}
	models[1].Fields = []ir.FieldIR{field(1, "Published", ir.LogicalTypeIR{Kind: ir.TypeBool})}
	enumID := ir.EnumID(fmt.Sprintf("%032x", 3000))
	models[2].Fields = []ir.FieldIR{field(2, "Status", ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: &enumID})}
	models[3].Fields = []ir.FieldIR{field(3, "ID", ir.LogicalTypeIR{Kind: ir.TypeInt64})}
	relationID := ir.RelationID(fmt.Sprintf("%032x", 4000))
	relationFieldID := ir.FieldID(fmt.Sprintf("%032x", 2004))
	models[4].Fields = []ir.FieldIR{{ID: relationFieldID, GoName: "Owner", LogicalName: "Owner", Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: relationID, Role: ir.RelationSource, Kind: ir.RelationBelongsTo}}}
	models[5].Fields = []ir.FieldIR{field(5, "ID", ir.LogicalTypeIR{Kind: ir.TypeInt64})}
	models[6].Fields = []ir.FieldIR{field(6, "ID", ir.LogicalTypeIR{Kind: ir.TypeInt64})}
	return ir.CompilationIR{
		Model: ir.ModelIR{
			FormatVersion: ir.ModelFormatVersion,
			Schema:        ir.SchemaIdentityIR{StableName: "capability-invalid", Actor: ir.GoNamedTypeIR{PackagePath: capabilityInvalidPackage, Name: "Actor"}},
			Models:        models,
			Enums:         []ir.EnumIR{{ID: enumID, Go: ir.GoNamedTypeIR{PackagePath: capabilityInvalidPackage, Name: "Status"}}},
			Relations:     []ir.RelationIR{{ID: relationID, SourceModel: modelIDs["RelationFailure"], TargetModel: modelIDs["Other"], SourceField: relationFieldID}},
		},
		Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: contracts},
	}
}

func moduleDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}
func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(moduleDir(t), "internal", "codegen", "bindings", "testdata", name)
}

func modelFiles(result modelcodegen.Result) []File {
	files := make([]File, len(result.Files))
	for index, file := range result.Files {
		files[index] = File{ImportPath: file.ImportPath, PackageName: file.PackageName, Path: file.Path, Source: file.Source}
	}
	return files
}

func compileOverlay(t *testing.T, pattern string, files []File) {
	t.Helper()
	overlay := map[string][]byte{}
	fresh := map[string]bool{}
	for _, file := range files {
		absolute, _ := filepath.Abs(file.Path)
		overlay[absolute], fresh[absolute] = file.Source, true
	}
	config := &packages.Config{Dir: moduleDir(t), Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps, Overlay: overlay}
	config.ParseFile = func(fset *token.FileSet, filename string, source []byte) (*ast.File, error) {
		absolute, _ := filepath.Abs(filename)
		if !fresh[absolute] && generated(source) {
			packageName := "valid"
			return parser.ParseFile(fset, filename, "package "+packageName+"\n", parser.AllErrors)
		}
		return parser.ParseFile(fset, filename, source, parser.AllErrors)
	}
	loaded, err := packages.Load(config, pattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range loaded {
		if len(pkg.Errors) != 0 {
			t.Fatalf("prospective bindings do not compile: %#v", pkg.Errors)
		}
	}
}
