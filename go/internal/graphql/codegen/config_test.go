package codegen

import (
	"bytes"
	"context"
	"errors"
	goast "go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gqlconfig "github.com/99designs/gqlgen/codegen/config"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func TestRenderSemanticSearchBindingUsesGeneratedCallerAndReturnsRows(t *testing.T) {
	compilation := ir.CompilationIR{
		Model:    ir.ModelIR{Schema: ir.SchemaIdentityIR{PackagePath: "example.test/app"}, Models: []ir.ModelDeclIR{{ID: "record", LogicalName: "Record", Go: ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "Record"}}}},
		Contract: ir.ContractIR{Models: []ir.ModelContractIR{{ModelID: "record", GraphQLName: "Record", GraphQLPlural: "Records", Exposed: true, Limits: ir.LimitContractIR{DefaultPageSize: 25, MaxPageSize: 250}}}},
	}
	payload, _ := semanticcontract.Encode(semanticcontract.Index{Name: "content", Space: "content", Dimensions: 3, Fields: []string{"field"}, Metric: "cosine"})
	compilation.Model.Extensions = []ir.ProviderExtensionIR{{ID: "semantic", Provider: ir.SQLite, Version: 1, Owner: "record", Kind: semanticcontract.IndexKind, Payload: payload}}
	if diagnostics := graphqlextension.AddSemanticSearchOperations(&compilation); len(diagnostics) != 0 {
		t.Fatalf("semantic diagnostics = %#v", diagnostics)
	}
	bindings, err := renderCustomBindings(&compilation, func(path, preferred string) string {
		if path == "example.test/app" {
			return ""
		}
		return preferred
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("semantic bindings = %#v", bindings)
	}
	var searchBinding, similarBinding string
	for _, binding := range bindings {
		switch {
		case strings.Contains(binding, "caller.Records.SearchContent"):
			searchBinding = binding
		case strings.Contains(binding, "caller.Records.SimilarContent"):
			similarBinding = binding
		}
	}
	if searchBinding == "" || similarBinding == "" {
		t.Fatalf("semantic bindings = %#v", bindings)
	}
	for _, fragment := range []string{"*Caller[P]", "Query string", "Take *int32", "Where *golem.Predicate[Record]", "args.Take == nil", "[]golem.SemanticResult[Record]", "return caller.Records.SearchContent", "GeneratedCustomPredicateArgument"} {
		if !strings.Contains(searchBinding, fragment) {
			t.Fatalf("semantic search binding missing %q:\n%s", fragment, searchBinding)
		}
	}
	for _, fragment := range []string{"*Caller[P]", "Source golem.UniqueSelectorValue[Record]", "Take *int32", "Where *golem.Predicate[Record]", "[]golem.SemanticResult[Record]", "return caller.Records.SimilarContent(ctx, args.Source, take, where...)", "GeneratedCustomSelectorArgument"} {
		if !strings.Contains(similarBinding, fragment) {
			t.Fatalf("semantic similar binding missing %q:\n%s", fragment, similarBinding)
		}
	}
	if strings.Contains(similarBinding, "Query string") {
		t.Fatalf("similar binding retained the search query argument:\n%s", similarBinding)
	}
}

func TestRenderSemanticSearchBindingLeavesPagingToTheRuntimeOperationCompiler(t *testing.T) {
	operation := ir.CustomOperationContractIR{Operation: ir.CustomOperationQuery, Name: "searchRecordsByContent", Resolver: ir.AttachedMethodIR{Name: "content", Kind: "customquery"}}
	model := ir.ModelDeclIR{LogicalName: "Record", Go: ir.GoNamedTypeIR{Name: "Record"}}
	contract := ir.ModelContractIR{Limits: ir.LimitContractIR{DefaultPageSize: 1500, MaxPageSize: 2000}}
	resolver, err := renderSemanticSearchResolver(operation, model, contract, func(string, string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"args.Take == nil", "take := int(*args.Take)"} {
		if !strings.Contains(resolver, fragment) {
			t.Fatalf("semantic maximum missing %q:\n%s", fragment, resolver)
		}
	}
}

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
		if first.Files[index].Filename == ExecutableFilename {
			if bytes.Contains(first.Files[index].Source, []byte("//go:embed")) || !bytes.Contains(first.Files[index].Source, []byte(`{Name: "../zz_golem_graphql.schema.graphqls", Input:`)) {
				t.Fatalf("generated executable does not own an inline final-layout SDL source")
			}
		}
		if first.Files[index].Filename == ExecutableFilename && (bytes.Contains(first.Files[index].Source, []byte("//go:embed")) || bytes.Contains(first.Files[index].Source, []byte("sourceData("))) {
			t.Fatalf("generated executable depends on an unowned child SDL artifact:\n%s", first.Files[index].Source)
		}
	}
	if first.Filename != GoFilename || len(first.Source) == 0 || first.SDLFingerprint == "" {
		t.Fatalf("incomplete generated result: %#v", first)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), first.Filename, first.Source, parser.AllErrors); err != nil {
		t.Fatalf("generated adapter is not valid Go: %v\n%s", err, first.Source)
	}
	source := string(first.Source)
	for _, required := range []string{GQLGenVersion, GraphQLABIVersion, string(compiled.ContractFingerprint), request.GenerationDigest, request.GeneratorVersion, request.TemplateABIVersion, `Kind: "mutation", Field: "createPost"`, `Kind: "query", Field: "posts"`, "type GraphQLLimits = golemgraphql.Limits", "type GraphQLConfig[P any] struct", "type GraphQLServer struct", "func (app *App[P]) GraphQL", "NewGeneratedExecutor", "golemGeneratedGraphQLBeginCaller", "app.ForPrincipal", "NewCallerMutationExecution", "ExecuteFrozenAnalytics", "CallerExecuteFrozenAnalytics", "GolemGraphQLCallerCapability", "CallerMutationModel[P, Actor](GolemGeneratedPostDescriptor)", "bundle.Contract().Fingerprint()"} {
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

func TestPinnedGQLGenRestoresWorkingDirectoryAfterSuccessAndFailure(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	moduleDirectory, err := filepath.Abs(filepath.Join(originalDirectory, "../../.."))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outside, err = filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}

	compiled := compile.Compile(context.Background(), compile.Config{Dir: filepath.Join(moduleDirectory, "internal/compiler/compile/testdata/social"), Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Emit(Request{
		PackageName: "social", AppImportPath: compiled.Compilation.Model.Schema.PackagePath, ModuleDir: moduleDirectory,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compiled.Compilation.Model.Schema.Actor,
		GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err != nil {
		t.Fatalf("successful pinned generation: %v", err)
	}
	assertWorkingDirectory(t, outside)

	failure := NewConfig()
	failure.Exec.Filename = filepath.Join(outside, "failure", "exec.go")
	failure.Model.Filename = filepath.Join(outside, "failure", "models.go")
	failure.Resolver.Filename = filepath.Join(outside, "failure", "resolvers.go")
	if err := runPinnedGQLGen(failure, moduleDirectory); err == nil {
		t.Fatal("incomplete gqlgen configuration unexpectedly succeeded")
	}
	assertWorkingDirectory(t, outside)
	for _, name := range []string{"generated.go", "models_gen.go"} {
		if _, err := os.Stat(filepath.Join(moduleDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("pinned generation leaked module-root artifact %s: %v", name, err)
		}
	}
}

func TestPrivatePinnedGQLGenRestoresProcessStateAfterSuccessErrorAndPanic(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	originalWorkspace, workspacePresent := os.LookupEnv("GOWORK")
	originalFlags, flagsPresent := os.LookupEnv("GOFLAGS")
	t.Cleanup(func() {
		_ = os.Chdir(originalDirectory)
		_ = restoreEnvironment("GOWORK", originalWorkspace, workspacePresent)
		_ = restoreEnvironment("GOFLAGS", originalFlags, flagsPresent)
	})
	outside := t.TempDir()
	outside, err = filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GOWORK", "off"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GOFLAGS", "-tags=p8_private_restore"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		generate func(*gqlconfig.Config) error
		wantErr  bool
	}{
		{name: "success", generate: func(*gqlconfig.Config) error { return nil }},
		{name: "error", generate: func(*gqlconfig.Config) error { return errors.New("expected") }, wantErr: true},
		{name: "panic", generate: func(*gqlconfig.Config) error { panic("expected") }, wantErr: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := runPinnedGQLGenPrivateWith(NewConfig(), private, test.generate)
			if (err != nil) != test.wantErr {
				t.Fatalf("private generation error=%v wantErr=%t", err, test.wantErr)
			}
			assertWorkingDirectory(t, outside)
			if got := os.Getenv("GOWORK"); got != "off" {
				t.Fatalf("GOWORK=%q after private generation", got)
			}
			if got := os.Getenv("GOFLAGS"); got != "-tags=p8_private_restore" {
				t.Fatalf("GOFLAGS=%q after private generation", got)
			}
		})
	}
}

func TestPrivateGQLGenModuleSourceCoversVersionMainReplaceAndFork(t *testing.T) {
	tests := []struct {
		name, module string
		resolved     moduleResolution
		contains     []string
	}{
		{name: "versioned", module: "example.test/framework", resolved: moduleResolution{Path: "example.test/framework", Version: "v1.2.3"}, contains: []string{"example.test/framework v1.2.3"}},
		{name: "workspace-main", module: "example.test/framework", resolved: moduleResolution{Path: "example.test/framework", Dir: "/tmp/framework"}, contains: []string{"example.test/framework v0.0.0", `replace example.test/framework => "/tmp/framework"`}},
		{name: "local-replace", module: "example.test/framework", resolved: moduleResolution{Path: "example.test/framework", Version: "v0.0.0", Replace: &struct {
			Path    string `json:"Path"`
			Version string `json:"Version"`
			Dir     string `json:"Dir"`
		}{Dir: "/tmp/fork"}}, contains: []string{"example.test/framework v0.0.0", `replace example.test/framework => "/tmp/fork"`}},
		{name: "module-replace", module: "example.test/framework", resolved: moduleResolution{Path: "example.test/framework", Version: "v1.0.0", Replace: &struct {
			Path    string `json:"Path"`
			Version string `json:"Version"`
			Dir     string `json:"Dir"`
		}{Path: "example.test/fork", Version: "v1.1.0"}}, contains: []string{"example.test/framework v1.0.0", "replace example.test/framework => example.test/fork v1.1.0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, err := privateGQLGenModuleSource(test.module, test.resolved)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range append(test.contains, "github.com/99designs/gqlgen "+GQLGenVersion, "github.com/vektah/gqlparser/v2 "+gqlParserVersion) {
				if !strings.Contains(string(source), want) {
					t.Fatalf("private module missing %q:\n%s", want, source)
				}
			}
		})
	}
}

func TestPrivateGQLGenResolutionUsesRequestWorkspaceEnvironment(t *testing.T) {
	framework, err := filepath.Abs(filepath.Join("../../.."))
	if err != nil {
		t.Fatal(err)
	}
	consumer := t.TempDir()
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte("module example.test/consumer\n\ngo 1.25.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "go.work")
	work := "go 1.25.0\n\nuse (\n\t" + filepath.ToSlash(framework) + "\n\t" + filepath.ToSlash(consumer) + "\n)\n"
	if err := os.WriteFile(workspace, []byte(work), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveConsumerModule(consumer, []string{"GOWORK=" + workspace}, "github.com/eleven-am/golem/go")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != "" || resolved.Dir == "" {
		t.Fatalf("workspace main resolution=%+v", resolved)
	}
	private := t.TempDir()
	request := Request{GolemImportPath: DefaultGolemImportPath, GraphQLImportPath: DefaultGraphQLImportPath, Env: []string{"GOWORK=" + workspace}}
	if err := writePrivateGQLGenModule(private, request, consumer); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(private, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "replace github.com/eleven-am/golem/go => ") || !strings.Contains(string(source), filepath.ToSlash(resolved.Dir)) {
		t.Fatalf("workspace resolution missing from private module:\n%s", source)
	}
}

func TestGraphQLCodegenRejectsSplitFrameworkImportModules(t *testing.T) {
	_, err := Emit(Request{
		PackageName: "app", AppImportPath: "example.test/app", ModuleDir: t.TempDir(), SDL: "scalar String\ntype Query { ok: String }",
		ContractFingerprint: "contract", Actor: ir.GoNamedTypeIR{PackagePath: "example.test/app", Name: "Actor"},
		GolemImportPath: "example.test/one/golem", GraphQLImportPath: "example.test/two/graphql",
		GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err == nil || !strings.Contains(err.Error(), "one module") {
		t.Fatalf("split framework imports error=%v", err)
	}
}

func assertWorkingDirectory(t *testing.T, want string) {
	t.Helper()
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("working directory = %q, want %q", got, want)
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

func TestGeneratedGraphQLBindsTypedEventsAndNativeSubscriptionResolver(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	var post *ir.ModelDeclIR
	for index := range compiled.Compilation.Model.Models {
		if compiled.Compilation.Model.Models[index].Go.Name == "Post" {
			post = &compiled.Compilation.Model.Models[index]
			break
		}
	}
	if post == nil || post.PrimaryKey == nil {
		t.Fatal("Post model or primary key is absent")
	}
	for index := range compiled.Compilation.Contract.Models {
		contract := &compiled.Compilation.Contract.Models[index]
		if contract.ModelID != post.ID {
			continue
		}
		shape, err := ir.BuildEventSchemaShape(*post, compiled.Compilation.Model.Enums, post.PrimaryKey.Fields)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := ir.EventSchemaFingerprint(shape)
		if err != nil {
			t.Fatal(err)
		}
		contract.Subscriptions = true
		contract.Roots.Events = "postEvents"
		contract.Event = &ir.EventContractIR{PayloadTypeName: "PostEvent", Schema: shape, SchemaFingerprint: fingerprint}
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Emit(Request{
		PackageName: "social", AppImportPath: compiled.Compilation.Model.Schema.PackagePath,
		SDL: document.SDL, ContractFingerprint: compiled.ContractFingerprint, Actor: compiled.Compilation.Model.Schema.Actor,
		Compilation: compiled.Compilation, GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := string(result.Source)
	for _, required := range []string{"WebSocketInit", "Shutdown(ctx context.Context)", "SubscribeFrozenEvents", "CallerFrozenReadEvents", "AdaptGeneratedEventStream", "NewGeneratedEvent", `Kind: "subscription", Field: "postEvents"`} {
		if !strings.Contains(adapter, required) {
			t.Errorf("generated P7 adapter missing %q", required)
		}
	}
	var resolvers string
	for _, file := range result.Files {
		if file.Filename == ResolversFilename {
			resolvers = string(file.Source)
		}
	}
	if !strings.Contains(resolvers, "ResolvePreparedSubscriptionRoot") || strings.Contains(resolvers, `panic("not implemented")`) {
		t.Fatalf("generated subscription resolver is not bound:\n%s", resolvers)
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
	fork := filepath.Join(t.TempDir(), "framework")
	if err := os.MkdirAll(filepath.Join(fork, "graphql"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fork, "go.mod"), []byte("module example.test/golem\n\ngo 1.25.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fork, "graphql", "graphql.go"), []byte("package graphql\ntype PreparedObject map[string]any\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	consumer := t.TempDir()
	consumerModule := "module example.test/app\n\ngo 1.25.0\n\nrequire example.test/golem v0.0.0\nreplace example.test/golem => " + filepath.ToSlash(fork) + "\n"
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(consumerModule), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Emit(Request{
		PackageName: "social", AppImportPath: "example.test/golem/app", Actor: ir.GoNamedTypeIR{PackagePath: "example.test/golem/app", Name: "Actor"}, SDL: "type Query { ok: Boolean }", ContractFingerprint: "digest",
		ModuleDir: consumer, Env: []string{"GOWORK=off"}, GolemImportPath: "example.test/golem/golem", GenerationDigest: "generation", GeneratorVersion: "generator", TemplateABIVersion: "template",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Source)
	if !strings.Contains(source, `"example.test/golem/golem"`) || !strings.Contains(source, `golemgraphql "example.test/golem/graphql"`) {
		t.Fatalf("generated imports do not follow the configured public module:\n%s", source)
	}
}
