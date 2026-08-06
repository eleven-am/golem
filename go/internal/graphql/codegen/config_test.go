package codegen

import (
	"bytes"
	"context"
	goast "go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func TestEmitIsDeterministicValidGoAndCarriesCallerOnlyServerAssembly(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		PackageName: "social", AppImportPath: compiled.Compilation.Model.Schema.PackagePath,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compiled.Compilation.Model.Schema.Actor,
		GenerationDigest: "generation-digest", GeneratorVersion: "generator-version", TemplateABIVersion: "template-abi",
	}
	for _, model := range compiled.Compilation.Model.Models {
		request.MutationModels = append(request.MutationModels, MutationModel{PackagePath: model.Go.PackagePath, GoName: model.Go.Name})
	}
	first, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Clean(filepath.Join(originalDirectory, "../../.."))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	second, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Source) != string(second.Source) || first.SDLFingerprint != second.SDLFingerprint {
		t.Fatal("GraphQL Go adapter generation is not byte deterministic")
	}
	if len(first.Files) != 3 || len(second.Files) != len(first.Files) {
		t.Fatalf("generated gqlgen files = %d/%d", len(first.Files), len(second.Files))
	}
	for index := range first.Files {
		if first.Files[index].Filename != second.Files[index].Filename || !bytes.Equal(first.Files[index].Source, second.Files[index].Source) {
			offset := 0
			for offset < len(first.Files[index].Source) && offset < len(second.Files[index].Source) && first.Files[index].Source[offset] == second.Files[index].Source[offset] {
				offset++
			}
			start := offset - 80
			if start < 0 {
				start = 0
			}
			endFirst, endSecond := offset+160, offset+160
			if endFirst > len(first.Files[index].Source) {
				endFirst = len(first.Files[index].Source)
			}
			if endSecond > len(second.Files[index].Source) {
				endSecond = len(second.Files[index].Source)
			}
			t.Fatalf("generated gqlgen artifact %d is not byte deterministic at %d\nfirst: %q\nsecond:%q", index, offset, first.Files[index].Source[start:endFirst], second.Files[index].Source[start:endSecond])
		}
		if _, err := parser.ParseFile(token.NewFileSet(), first.Files[index].Filename, first.Files[index].Source, parser.AllErrors); err != nil {
			t.Fatalf("generated gqlgen artifact %s is not valid Go: %v\n%s", first.Files[index].Filename, err, first.Files[index].Source)
		}
	}
	if first.Filename != GoFilename || len(first.Source) == 0 || first.SDLFingerprint == "" {
		t.Fatalf("incomplete generated result: %#v", first)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), first.Filename, first.Source, parser.AllErrors); err != nil {
		t.Fatalf("generated adapter is not valid Go: %v\n%s", err, first.Source)
	}
	source := string(first.Source)
	for _, required := range []string{GQLGenVersion, GraphQLABIVersion, string(compiled.ContractFingerprint), request.GenerationDigest, request.GeneratorVersion, request.TemplateABIVersion, `Kind: "mutation", Field: "createPost"`, `Kind: "query", Field: "posts"`, "type GraphQLLimits = golemgraphql.Limits", "type GraphQLConfig[P any] struct", "type GraphQLServer struct", "func (app *App[P]) GraphQL", "NewGeneratedExecutor", "golemGeneratedGraphQLBeginCaller", "app.ForPrincipal", "NewCallerMutationExecution", "GolemGraphQLCallerCapability", "CallerMutationModel[P, Actor](GolemGeneratedPostDescriptor)", "bundle.Contract().Fingerprint()"} {
		if !strings.Contains(source, required) {
			t.Errorf("generated adapter missing %q", required)
		}
	}
	for _, forbidden := range []string{"database/sql", "sqlx", "/internal/graphql/operation", "/internal/read/", "/internal/mutation/", "Authorize", "BeginTx", "Commit(", "ResolveGolemGraphQL"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("generated translation adapter contains forbidden runtime behavior %q", forbidden)
		}
	}
}

func TestGeneratedGraphQLUsesPinnedExecutableSchemaAsActiveServerPath(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Emit(Request{
		PackageName: "social", AppImportPath: compiled.Compilation.Model.Schema.PackagePath,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compiled.Compilation.Model.Schema.Actor,
		GenerationDigest: "generation-digest", GeneratorVersion: "generator-version", TemplateABIVersion: "template-abi",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := string(result.Source)
	for _, required := range []string{"golemgqlgen.NewExecutableSchema(", "ExecutableSchema: golemGeneratedExecutable", "Resolvers: &golemgqlgen.Resolver"} {
		if !strings.Contains(adapter, required) {
			t.Errorf("active gqlgen adapter missing %q", required)
		}
	}
	generated := map[string]string{}
	for _, file := range result.Files {
		generated[file.Filename] = string(file.Source)
	}
	if !strings.Contains(generated[ExecutableFilename], "func NewExecutableSchema(") || !strings.Contains(generated[ExecutableFilename], "func (e *executableSchema) Exec(") {
		t.Error("pinned executable artifact does not implement the active gqlgen executable")
	}
	if strings.Contains(generated[ResolversFilename], `panic("not implemented")`) || !strings.Contains(generated[ResolversFilename], "ResolvePreparedRoot") || !strings.Contains(generated[ResolversFilename], "ResolvePreparedField") {
		t.Error("generated resolver root is not fully bound to prepared Golem results")
	}
}

func TestAUTHORIZE_IN_RESOLVERGeneratedResolversAreCapabilityFreePreparedProjectionOnly(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Emit(Request{
		PackageName: "graphqlextensions", AppImportPath: compiled.Compilation.Model.Schema.PackagePath,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compiled.Compilation.Model.Schema.Actor,
		Compilation: compiled.Compilation, GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err != nil {
		t.Fatal(err)
	}
	var source []byte
	for _, generated := range result.Files {
		if generated.Filename == ResolversFilename {
			source = generated.Source
			break
		}
	}
	if len(source) == 0 {
		t.Fatal("generated resolver artifact is absent")
	}
	file, err := parser.ParseFile(token.NewFileSet(), ResolversFilename, source, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse generated resolvers: %v", err)
	}

	graphqlAliases := map[string]bool{}
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		if path == "context" {
			continue
		}
		if path != "github.com/eleven-am/golem/go/graphql" {
			t.Fatalf("generated resolver artifact imports capability-bearing package %q", path)
		}
		name := "graphql"
		if imported.Name != nil {
			name = imported.Name.Name
		}
		graphqlAliases[name] = true
	}
	if len(graphqlAliases) != 1 {
		t.Fatalf("generated resolver artifact has GraphQL runtime aliases %#v", graphqlAliases)
	}

	resolverTypes := map[string]*goast.StructType{}
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*goast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, item := range generic.Specs {
			typeSpec, ok := item.(*goast.TypeSpec)
			if !ok {
				continue
			}
			structure, ok := typeSpec.Type.(*goast.StructType)
			if ok && (typeSpec.Name.Name == "Resolver" || strings.HasSuffix(typeSpec.Name.Name, "Resolver")) {
				resolverTypes[typeSpec.Name.Name] = structure
			}
		}
	}
	base, ok := resolverTypes["Resolver"]
	if !ok || base.Fields == nil || len(base.Fields.List) != 0 {
		t.Fatalf("generated base resolver carries fields/capabilities: %#v", base)
	}
	for name, structure := range resolverTypes {
		if name == "Resolver" {
			continue
		}
		if structure.Fields == nil || len(structure.Fields.List) != 1 || len(structure.Fields.List[0].Names) != 0 {
			t.Fatalf("generated %s has capability-bearing fields: %#v", name, structure.Fields)
		}
		pointer, ok := structure.Fields.List[0].Type.(*goast.StarExpr)
		if !ok {
			t.Fatalf("generated %s does not solely embed the empty Resolver", name)
		}
		identity, valid := pointer.X.(*goast.Ident)
		if !valid || identity.Name != "Resolver" {
			t.Fatalf("generated %s does not solely embed the empty Resolver", name)
		}
	}

	bound := 0
	for _, declaration := range file.Decls {
		function, ok := declaration.(*goast.FuncDecl)
		if !ok || function.Recv == nil || function.Body == nil {
			continue
		}
		receiver := receiverName(function.Recv.List[0].Type)
		if receiver == "Resolver" {
			continue // schema resolver factories carry only the empty Resolver.
		}
		if len(function.Body.List) != 1 {
			t.Fatalf("resolver %s.%s has executable statements beyond prepared projection", receiver, function.Name.Name)
		}
		returned, ok := function.Body.List[0].(*goast.ReturnStmt)
		if !ok || len(returned.Results) != 1 {
			t.Fatalf("resolver %s.%s is not one return expression", receiver, function.Name.Name)
		}
		call, ok := returned.Results[0].(*goast.CallExpr)
		if !ok {
			t.Fatalf("resolver %s.%s does not return a prepared projection call", receiver, function.Name.Name)
		}
		callee := call.Fun
		switch generic := callee.(type) {
		case *goast.IndexExpr:
			callee = generic.X
		case *goast.IndexListExpr:
			callee = generic.X
		default:
			t.Fatalf("resolver %s.%s calls a non-generic capability", receiver, function.Name.Name)
		}
		selector, ok := callee.(*goast.SelectorExpr)
		if !ok {
			t.Fatalf("resolver %s.%s calls outside the prepared GraphQL projection package", receiver, function.Name.Name)
		}
		qualifier, valid := selector.X.(*goast.Ident)
		if !valid || !graphqlAliases[qualifier.Name] {
			t.Fatalf("resolver %s.%s calls outside the prepared GraphQL projection package", receiver, function.Name.Name)
		}
		wantHelper, wantArguments := "ResolvePreparedField", []string{"ctx", "obj"}
		if receiver == "queryResolver" || receiver == "mutationResolver" {
			wantHelper, wantArguments = "ResolvePreparedRoot", []string{"ctx"}
		}
		if selector.Sel.Name != wantHelper || len(call.Args) != len(wantArguments) {
			t.Fatalf("resolver %s.%s calls %s with %d arguments, want %s%v", receiver, function.Name.Name, selector.Sel.Name, len(call.Args), wantHelper, wantArguments)
		}
		for index, argument := range call.Args {
			identity, ok := argument.(*goast.Ident)
			if !ok || identity.Name != wantArguments[index] {
				t.Fatalf("resolver %s.%s argument %d is not %s", receiver, function.Name.Name, index, wantArguments[index])
			}
		}
		bound++
	}
	if bound == 0 {
		t.Fatal("generated artifact contains no execution resolvers")
	}
}

func TestEmitWiresTypedComputedAndCustomBindingsFromCanonicalContract(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Emit(Request{
		PackageName: "graphqlextensions", AppImportPath: compiled.Compilation.Model.Schema.PackagePath,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compiled.Compilation.Model.Schema.Actor,
		Compilation: compiled.Compilation, GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), result.Filename, result.Source, parser.AllErrors); err != nil {
		t.Fatalf("generated adapter is invalid: %v\n%s", err, result.Source)
	}
	source := string(result.Source)
	for _, required := range []string{"BindGeneratedComputed", "BindGeneratedBatchedComputed", "GreetingCacheKey", "BindGeneratedCustomQueryModel", "BindGeneratedCustomMutationModel", "GeneratedCustomPredicateArgument", "GeneratedCustomMutationInputArgument", "CustomBindings:"} {
		if !strings.Contains(source, required) {
			t.Errorf("generated extension adapter missing %q", required)
		}
	}
}

func TestEmitRejectsInvalidPackageSDLAndFingerprint(t *testing.T) {
	for _, request := range []Request{
		{PackageName: "not-legal", SDL: "type Query { ok: Boolean }", ContractFingerprint: "digest", GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template"},
		{PackageName: "social", SDL: "type Query {", ContractFingerprint: "digest", GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template"},
		{PackageName: "social", SDL: "type Query { ok: Boolean }", GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template"},
	} {
		if _, err := Emit(request); err == nil {
			t.Fatalf("Emit(%#v) unexpectedly succeeded", request)
		}
	}
}

func TestEmitDerivesPublicGraphQLImportFromConfiguredGolemModule(t *testing.T) {
	result, err := Emit(Request{
		PackageName: "social", AppImportPath: "example.test/golem/app", Actor: ir.GoNamedTypeIR{PackagePath: "example.test/golem/app", Name: "Actor"}, SDL: "type Query { ok: Boolean }", ContractFingerprint: "digest",
		GolemImportPath: "example.test/golem/golem", GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Source)
	if !strings.Contains(source, `"example.test/golem/golem"`) || !strings.Contains(source, `golemgraphql "example.test/golem/graphql"`) {
		t.Fatalf("generated imports do not follow the configured public module:\n%s", source)
	}
}
