// Package codegen owns the pinned gqlgen integration. Application modules do
// not carry gqlgen configuration and do not run a second generator.
package codegen

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/format"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	"github.com/vektah/gqlparser/v2/ast"
)

// GQLGenVersion participates in P5 generation identity and is changed only
// with the GraphQL golden, compile, execution, and determinism gates.
const GQLGenVersion = "v0.17.70"

const (
	GraphQLABIVersion        = "p8-graphql-abi-v5"
	GoFilename               = "zz_golem_graphql.gen.go"
	SDLFilename              = "zz_golem_graphql.schema.graphqls"
	DefaultGolemImportPath   = "github.com/eleven-am/golem/go/golem"
	DefaultGraphQLImportPath = "github.com/eleven-am/golem/go/graphql"
)

// NewConfig returns a fresh upstream configuration that Golem will populate
// entirely in memory. Keeping construction here prevents ambient gqlgen.yml
// files from becoming schema authority.
func NewConfig() *config.Config { return config.DefaultConfig() }

type Request struct {
	PackageName         string
	AppImportPath       string
	ModuleDir           string
	Env                 []string
	SDL                 string
	ContractFingerprint ir.Fingerprint
	Actor               ir.GoNamedTypeIR
	MutationModels      []MutationModel
	Compilation         *ir.CompilationIR
	GolemImportPath     string
	GraphQLImportPath   string
	RuntimeImportPath   string
	GenerationDigest    string
	GeneratorVersion    string
	TemplateABIVersion  string
}

type MutationModel struct {
	PackagePath string
	GoName      string
}

type Result struct {
	Filename       string
	Source         []byte
	Files          []GeneratedFile
	SDLFingerprint ir.Fingerprint
}

// Emit produces Golem's deterministic application-package server adapter. It
// assembles the public caller-only GraphQL executor with the generated App but
// contains no policy decision, SQL access, hook, or transaction behavior.
func Emit(request Request) (Result, error) {
	if !token.IsIdentifier(request.PackageName) {
		return Result{}, fmt.Errorf("GraphQL codegen requires a legal package name")
	}
	if request.AppImportPath == "" || request.Actor.PackagePath == "" || !token.IsIdentifier(request.Actor.Name) || request.SDL == "" || request.ContractFingerprint == "" || request.GenerationDigest == "" || request.GeneratorVersion == "" || request.TemplateABIVersion == "" {
		return Result{}, fmt.Errorf("GraphQL codegen requires SDL and complete generation identity")
	}
	if request.GolemImportPath == "" {
		request.GolemImportPath = DefaultGolemImportPath
	}
	if request.GraphQLImportPath == "" {
		request.GraphQLImportPath = DefaultGraphQLImportPath
		if request.GolemImportPath != DefaultGolemImportPath {
			request.GraphQLImportPath = strings.TrimSuffix(request.GolemImportPath, "/golem") + "/graphql"
		}
	}
	golemModulePath, golemSuffix := strings.CutSuffix(request.GolemImportPath, "/golem")
	graphqlModulePath, graphqlSuffix := strings.CutSuffix(request.GraphQLImportPath, "/graphql")
	if !golemSuffix || !graphqlSuffix || golemModulePath == "" || golemModulePath != graphqlModulePath {
		return Result{}, fmt.Errorf("GraphQL codegen requires golem and graphql imports from one module")
	}
	if request.RuntimeImportPath == "" {
		request.RuntimeImportPath = strings.TrimSuffix(request.GolemImportPath, "/golem") + "/runtime"
	}
	mutationModels := append([]MutationModel(nil), request.MutationModels...)
	sort.Slice(mutationModels, func(i, j int) bool {
		if mutationModels[i].PackagePath != mutationModels[j].PackagePath {
			return mutationModels[i].PackagePath < mutationModels[j].PackagePath
		}
		return mutationModels[i].GoName < mutationModels[j].GoName
	})
	for index, model := range mutationModels {
		if model.PackagePath == "" || !token.IsIdentifier(model.GoName) || index > 0 && mutationModels[index-1] == model {
			return Result{}, fmt.Errorf("GraphQL codegen has an invalid or duplicate mutation model")
		}
	}
	gqlgenConfig := NewConfig()
	gqlgenConfig.Sources = []*ast.Source{{Name: SDLFilename, Input: request.SDL}}
	if err := gqlgenConfig.LoadSchema(); err != nil {
		return Result{}, fmt.Errorf("validate generated GraphQL SDL: %w", err)
	}
	schema := gqlgenConfig.Schema
	executableFiles, err := generateExecutable(request, schema)
	if err != nil {
		return Result{}, err
	}
	digest := sha256.Sum256([]byte(request.SDL))
	sdlFingerprint := ir.Fingerprint(fmt.Sprintf("%x", digest[:]))

	var roots []root
	appendRoots := func(operation string, definition *ast.Definition) {
		if definition == nil {
			return
		}
		for _, field := range definition.Fields {
			roots = append(roots, root{operation: operation, name: field.Name})
		}
	}
	appendRoots("query", schema.Query)
	appendRoots("mutation", schema.Mutation)
	appendRoots("subscription", schema.Subscription)
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].operation != roots[j].operation {
			return roots[i].operation < roots[j].operation
		}
		return roots[i].name < roots[j].name
	})

	var body bytes.Buffer
	fmt.Fprintf(&body, "// Code generated by golem. DO NOT EDIT.\n\npackage %s\n\n", request.PackageName)
	aliases := map[string]string{
		"context": "context", "fmt": "fmt", "net/http": "http",
		request.GolemImportPath: "golem", request.GraphQLImportPath: "golemgraphql", request.RuntimeImportPath: "golemruntime",
		request.AppImportPath + "/" + ExecutablePackageDirectory: "golemgqlgen",
	}
	additional := map[string]string{}
	qualify := func(path, preferred string) string {
		if path == request.AppImportPath {
			return ""
		}
		if alias := aliases[path]; alias != "" {
			return alias
		}
		alias := preferred
		used := map[string]bool{}
		for _, existing := range aliases {
			used[existing] = true
		}
		for suffix := 2; used[alias]; suffix++ {
			alias = preferred + strconv.Itoa(suffix)
		}
		aliases[path], additional[path] = alias, alias
		return alias
	}
	actorAlias := qualify(request.Actor.PackagePath, "golemactor")
	actorType := request.Actor.Name
	if actorAlias != "" {
		actorType = actorAlias + "." + actorType
	}
	descriptors := make([]string, len(mutationModels))
	for index, model := range mutationModels {
		alias := qualify(model.PackagePath, "golemmodels")
		descriptors[index] = "GolemGenerated" + model.GoName + "Descriptor"
		if alias != "" {
			descriptors[index] = alias + "." + descriptors[index]
		}
	}
	computedBindings, err := renderComputedBindings(request.Compilation, qualify)
	if err != nil {
		return Result{}, err
	}
	customBindings, err := renderCustomBindings(request.Compilation, qualify)
	if err != nil {
		return Result{}, err
	}
	eventBindings, err := renderEventBindings(request.Compilation, qualify)
	if err != nil {
		return Result{}, err
	}
	body.WriteString("import (\n")
	body.WriteString("\t\"context\"\n")
	body.WriteString("\t\"encoding/json\"\n")
	body.WriteString("\t\"fmt\"\n")
	body.WriteString("\t\"net/http\"\n\n")
	fmt.Fprintf(&body, "\t\"%s\"\n", request.GolemImportPath)
	fmt.Fprintf(&body, "\tgolemgraphql \"%s\"\n", request.GraphQLImportPath)
	fmt.Fprintf(&body, "\tgolemruntime \"%s\"\n", request.RuntimeImportPath)
	fmt.Fprintf(&body, "\tgolemgqlgen \"%s/%s\"\n", request.AppImportPath, ExecutablePackageDirectory)
	paths := make([]string, 0, len(additional))
	for path := range additional {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fmt.Fprintf(&body, "\t%s %s\n", additional[path], strconv.Quote(path))
	}
	body.WriteString(")\n\n")
	fmt.Fprintf(&body, "const golemGeneratedGraphQLABI = %s\n", strconv.Quote(GraphQLABIVersion))
	fmt.Fprintf(&body, "const golemGeneratedGQLGenVersion = %s\n", strconv.Quote(GQLGenVersion))
	fmt.Fprintf(&body, "const golemGeneratedGraphQLGenerationDigest = %s\n", strconv.Quote(request.GenerationDigest))
	fmt.Fprintf(&body, "const golemGeneratedGraphQLGeneratorVersion = %s\n", strconv.Quote(request.GeneratorVersion))
	fmt.Fprintf(&body, "const golemGeneratedGraphQLTemplateABI = %s\n", strconv.Quote(request.TemplateABIVersion))
	fmt.Fprintf(&body, "const golemGeneratedGraphQLContractFingerprint = %s\n", strconv.Quote(string(request.ContractFingerprint)))
	fmt.Fprintf(&body, "const golemGeneratedGraphQLSDLFingerprint = %s\n", strconv.Quote(string(sdlFingerprint)))
	fmt.Fprintf(&body, "const golemGeneratedGraphQLSDL = %s\n\n", strconv.Quote(request.SDL))
	body.WriteString("type golemGeneratedGraphQLRoot struct { Kind string; Field string }\n\n")
	body.WriteString("var golemGeneratedGraphQLRoots = [...]golemGeneratedGraphQLRoot{\n")
	for _, value := range roots {
		fmt.Fprintf(&body, "\t{Kind: %s, Field: %s},\n", strconv.Quote(value.operation), strconv.Quote(value.name))
	}
	body.WriteString("}\n\n")
	body.WriteString("type GraphQLLimits = golemgraphql.Limits\n\n")
	body.WriteString("type GraphQLConfig[P any] struct {\n")
	body.WriteString("\tPrincipalFromContext func(context.Context) (P, bool)\n")
	body.WriteString("\tLimits GraphQLLimits\n")
	body.WriteString("\tIntrospection bool\n")
	body.WriteString("\tWebSocketInit func(context.Context, json.RawMessage) (context.Context, error)\n")
	body.WriteString("\tReportInternalError func(context.Context, error)\n")
	body.WriteString("}\n\n")
	body.WriteString("type GraphQLServer struct {\n")
	body.WriteString("\thandler http.Handler\n")
	body.WriteString("\tsdl string\n")
	body.WriteString("\tcontractFingerprint golem.SchemaDigest\n")
	body.WriteString("\tshutdown func(context.Context) error\n")
	body.WriteString("}\n\n")
	body.WriteString("func (server *GraphQLServer) Shutdown(ctx context.Context) error {\n")
	body.WriteString("\tif server == nil || server.shutdown == nil { return nil }\n")
	body.WriteString("\treturn server.shutdown(ctx)\n")
	body.WriteString("}\n\n")
	body.WriteString("func (server *GraphQLServer) Handler() http.Handler {\n")
	body.WriteString("\tif server == nil { return nil }\n")
	body.WriteString("\treturn server.handler\n")
	body.WriteString("}\n\n")
	body.WriteString("func (server *GraphQLServer) SDL() string {\n")
	body.WriteString("\tif server == nil { return \"\" }\n")
	body.WriteString("\treturn server.sdl\n")
	body.WriteString("}\n\n")
	body.WriteString("func (server *GraphQLServer) ContractFingerprint() golem.SchemaDigest {\n")
	body.WriteString("\tif server == nil { return golem.SchemaDigest{} }\n")
	body.WriteString("\treturn server.contractFingerprint\n")
	body.WriteString("}\n\n")
	body.WriteString("type golemGeneratedGraphQLCaller[P any] struct {\n")
	body.WriteString("\tpublic *Caller[P]\n")
	fmt.Fprintf(&body, "\texecution *golemruntime.CallerMutationExecution[P, %s]\n", actorType)
	body.WriteString("}\n\n")
	body.WriteString("func (*Caller[P]) GolemGraphQLCallerCapability() {}\n\n")
	body.WriteString("func (caller *golemGeneratedGraphQLCaller[P]) GolemGraphQLCallerCapability() {}\n\n")
	body.WriteString("func (caller *golemGeneratedGraphQLCaller[P]) GolemGraphQLCustomCallerValue() any {\n")
	body.WriteString("\tif caller == nil { return nil }; return caller.public\n")
	body.WriteString("}\n\n")
	body.WriteString("func (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {\n")
	body.WriteString("\treturn caller.execution.ExecuteFrozenRead(ctx, request)\n")
	body.WriteString("}\n\n")
	body.WriteString("func (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenAnalytics(ctx context.Context, request golem.FrozenAnalyticsRequest) ([][]golem.RuntimeAnalyticsCell, error) {\n")
	body.WriteString("\tif caller == nil { return nil, fmt.Errorf(\"GraphQL caller is unavailable\") }\n")
	body.WriteString("\treturn golemruntime.CallerExecuteFrozenAnalytics(ctx, caller.public.runtime, request)\n")
	body.WriteString("}\n\n")
	body.WriteString("func (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenMutation(ctx context.Context, request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {\n")
	body.WriteString("\treturn caller.execution.ExecuteFrozenMutation(ctx, request)\n")
	body.WriteString("}\n\n")
	if len(eventBindings) != 0 {
		body.WriteString("func (caller *golemGeneratedGraphQLCaller[P]) SubscribeFrozenEvents(ctx context.Context, request golem.FrozenReadRequest, entitySelected bool) (golemgraphql.EventStream, error) {\n")
		body.WriteString("\tif caller == nil || caller.public == nil { return nil, fmt.Errorf(\"GraphQL caller is unavailable\") }\n")
		body.WriteString("\tswitch request.ModelID() {\n")
		for _, binding := range eventBindings {
			fmt.Fprintf(&body, "\tcase %s.Metadata().ModelID():\n", binding.descriptor)
			fmt.Fprintf(&body, "\t\tstream, err := golemruntime.CallerFrozenReadEvents[P, %s, %s](ctx, caller.public.runtime, request, entitySelected)\n", actorType, binding.eventType)
			body.WriteString("\t\tif err != nil { return nil, err }\n")
			fmt.Fprintf(&body, "\t\treturn golemgraphql.AdaptGeneratedEventStream(stream, func(event %s) (golemgraphql.GeneratedEvent, error) {\n", binding.eventType)
			body.WriteString("\t\t\tidentity := event.ID()\n")
			body.WriteString("\t\t\tvar entity *golem.RuntimeModelRow\n")
			body.WriteString("\t\t\tif typed, present := event.Entity(); present { runtimeEntity := golem.RuntimeModelRowFromTyped(typed); entity = &runtimeEntity }\n")
			fmt.Fprintf(&body, "\t\t\treturn golemgraphql.NewGeneratedEvent(event.Metadata(), []any{%s}, entity)\n", strings.Join(binding.identity, ", "))
			body.WriteString("\t\t})\n")
		}
		body.WriteString("\tdefault:\n\t\treturn nil, fmt.Errorf(\"GraphQL subscription model is unavailable\")\n\t}\n}\n\n")
	}
	body.WriteString("func (app *App[P]) golemGeneratedGraphQLBeginCaller(ctx context.Context, principal P) (golemgraphql.CallerExecution, error) {\n")
	body.WriteString("\tif app == nil || app.runtime == nil { return nil, fmt.Errorf(\"GraphQL application is unavailable\") }\n")
	body.WriteString("\tcaller, err := app.ForPrincipal(ctx, principal)\n")
	body.WriteString("\tif err != nil { return nil, err }\n")
	body.WriteString("\texecution, err := golemruntime.NewCallerMutationExecution(caller.runtime")
	for _, descriptor := range descriptors {
		fmt.Fprintf(&body, ",\n\t\tgolemruntime.CallerMutationModel[P, %s](%s)", actorType, descriptor)
	}
	body.WriteString(",\n\t)\n")
	body.WriteString("\tif err != nil { return nil, err }\n")
	body.WriteString("\treturn &golemGeneratedGraphQLCaller[P]{public: caller, execution: execution}, nil\n")
	body.WriteString("}\n\n")
	body.WriteString("func (app *App[P]) GraphQL(config GraphQLConfig[P]) (*GraphQLServer, error) {\n")
	body.WriteString("\tif app == nil || app.runtime == nil { return nil, fmt.Errorf(\"GraphQL application is unavailable\") }\n")
	body.WriteString("\tbundle := GolemGeneratedSchemaBundle()\n")
	body.WriteString("\tgolemGeneratedExecutable := golemgqlgen.NewExecutableSchema(golemgqlgen.Config{Resolvers: &golemgqlgen.Resolver{}})\n")
	for index, binding := range computedBindings {
		fmt.Fprintf(&body, "\tgolemGeneratedComputedBinding%d, err := %s\n", index, binding)
		body.WriteString("\tif err != nil { return nil, err }\n")
	}
	for index, binding := range customBindings {
		fmt.Fprintf(&body, "\tgolemGeneratedCustomBinding%d, err := %s\n", index, binding)
		body.WriteString("\tif err != nil { return nil, err }\n")
	}
	body.WriteString("\texecutor, err := golemgraphql.NewGeneratedExecutor(golemgraphql.GeneratedExecutorConfig[P]{\n")
	body.WriteString("\t\tBundle: bundle, Limits: config.Limits, BeginCaller: app.golemGeneratedGraphQLBeginCaller, ReportInternalError: config.ReportInternalError,\n")
	if len(computedBindings) != 0 {
		body.WriteString("\t\tComputedBindings: []golemgraphql.ComputedBinding{\n")
		for index := range computedBindings {
			fmt.Fprintf(&body, "\t\t\tgolemGeneratedComputedBinding%d,\n", index)
		}
		body.WriteString("\t\t},\n")
	}
	if request.Compilation != nil && len(request.Compilation.Contract.CustomOperations) != 0 {
		body.WriteString("\t\tCustomBindings: []golemgraphql.CustomBinding{\n")
		for index := range customBindings {
			fmt.Fprintf(&body, "\t\t\tgolemGeneratedCustomBinding%d,\n", index)
		}
		body.WriteString("\t\t},\n")
	}
	body.WriteString("\t})\n")
	body.WriteString("\tif err != nil { return nil, err }\n")
	body.WriteString("\tserver, err := golemgraphql.NewServer(golemGeneratedGraphQLSDL, golemgraphql.Config[P]{\n")
	body.WriteString("\t\tPrincipalFromContext: config.PrincipalFromContext, Limits: config.Limits, Introspection: config.Introspection, WebSocketInit: config.WebSocketInit, EventLimits: app.runtime.EventLimits(),\n")
	body.WriteString("\t\tContractFingerprint: bundle.Contract().Fingerprint(), ReportInternalError: config.ReportInternalError, ExecutableSchema: golemGeneratedExecutable, Observer: app.observer, Provider: app.provider,\n")
	body.WriteString("\t}, executor)\n")
	body.WriteString("\tif err != nil { return nil, err }\n")
	body.WriteString("\treturn &GraphQLServer{handler: server.Handler(), sdl: server.SDL(), contractFingerprint: server.ContractFingerprint(), shutdown: server.Shutdown}, nil\n")
	body.WriteString("}\n")
	source, err := format.Source(body.Bytes())
	if err != nil {
		return Result{}, fmt.Errorf("format generated GraphQL adapter: %w", err)
	}
	return Result{Filename: GoFilename, Source: source, Files: executableFiles, SDLFingerprint: sdlFingerprint}, nil
}

func renderComputedBindings(compilation *ir.CompilationIR, qualify func(string, string) string) ([]string, error) {
	if compilation == nil {
		return nil, nil
	}
	models := make(map[ir.ModelID]ir.ModelDeclIR, len(compilation.Model.Models))
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	var bindings []string
	contracts := append([]ir.ModelContractIR(nil), compilation.Contract.Models...)
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].ModelID < contracts[j].ModelID })
	for _, contract := range contracts {
		model, ok := models[contract.ModelID]
		if !ok {
			return nil, fmt.Errorf("GraphQL computed binding model %s is absent", contract.ModelID)
		}
		modelAlias := qualify(model.Go.PackagePath, "golemmodels")
		prefix := ""
		if modelAlias != "" {
			prefix = modelAlias + "."
		}
		fields := make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
		for _, field := range model.Fields {
			fields[field.ID] = field
		}
		computed := append([]ir.ComputedFieldContractIR(nil), contract.Computed...)
		sort.Slice(computed, func(i, j int) bool { return computed[i].ExtensionID < computed[j].ExtensionID })
		for _, field := range computed {
			descriptor := prefix + "GolemGenerated" + model.Go.Name + "Descriptor"
			if field.Batch == nil {
				resolver, err := renderCallable(field.Resolver, qualify)
				if err != nil {
					return nil, err
				}
				bindings = append(bindings, fmt.Sprintf("golemgraphql.BindGeneratedComputed(%s, %s, %s)", strconv.Quote(string(field.ExtensionID)), descriptor, resolver))
				continue
			}
			key, ok := fields[field.Batch.KeyField]
			if !ok || key.Scalar == nil {
				return nil, fmt.Errorf("GraphQL computed batch key %s is absent", field.Batch.KeyField)
			}
			loader, err := renderCallable(field.Batch.Loader, qualify)
			if err != nil {
				return nil, err
			}
			codec := "nil"
			if field.Batch.CacheKey != nil {
				codec, err = renderCallable(*field.Batch.CacheKey, qualify)
				if err != nil {
					return nil, err
				}
			}
			bindings = append(bindings, fmt.Sprintf("golemgraphql.BindGeneratedBatchedComputed(%s, %s, %s%s.%s, %s, %s)", strconv.Quote(string(field.ExtensionID)), descriptor, prefix, plural(model.LogicalName), key.GoName, loader, codec))
		}
	}
	return bindings, nil
}

func renderCustomBindings(compilation *ir.CompilationIR, qualify func(string, string) string) ([]string, error) {
	if compilation == nil {
		return nil, nil
	}
	models := map[string]ir.ModelDeclIR{}
	contracts := map[string]ir.ModelContractIR{}
	for _, contract := range compilation.Contract.Models {
		for _, model := range compilation.Model.Models {
			if model.ID == contract.ModelID {
				models[contract.GraphQLName] = model
				contracts[contract.GraphQLName] = contract
				break
			}
		}
	}
	operations := append([]ir.CustomOperationContractIR(nil), compilation.Contract.CustomOperations...)
	sort.Slice(operations, func(i, j int) bool { return operations[i].ExtensionID < operations[j].ExtensionID })
	bindings := make([]string, 0, len(operations))
	for _, operation := range operations {
		var resolver string
		var err error
		if graphqlextension.IsSemanticSearchOperation(*compilation, operation) {
			modelName := resultModelName(operation.Result)
			model, ok := models[modelName]
			if !ok {
				return nil, fmt.Errorf("GraphQL semantic search result model %s is absent", modelName)
			}
			contract := contracts[modelName]
			resolver, err = renderSemanticSearchResolver(operation, model, contract, qualify)
		} else if graphqlextension.IsSemanticSimilarOperation(*compilation, operation) {
			modelName := resultModelName(operation.Result)
			model, ok := models[modelName]
			if !ok {
				return nil, fmt.Errorf("GraphQL semantic similar result model %s is absent", modelName)
			}
			contract := contracts[modelName]
			resolver, err = renderSemanticSimilarResolver(operation, model, contract, qualify)
		} else {
			resolver, err = renderCallable(operation.Resolver, qualify)
		}
		if err != nil {
			return nil, err
		}
		constructor := "BindGeneratedCustomQueryContract"
		if operation.Operation == ir.CustomOperationMutation {
			constructor = "BindGeneratedCustomMutationContract"
		}
		arguments := fmt.Sprintf("bundle, golemgraphql.CustomBindingSpec{ExtensionID: %s, ResolverPackage: %s, ResolverName: %s}, %s", strconv.Quote(string(operation.ExtensionID)), strconv.Quote(operation.Resolver.PackagePath), strconv.Quote(operation.Resolver.Name), resolver)
		if modelName := resultModelName(operation.Result); modelName != "" {
			model, ok := models[modelName]
			if !ok {
				return nil, fmt.Errorf("GraphQL custom result model %s is absent", modelName)
			}
			alias := qualify(model.Go.PackagePath, "golemmodels")
			prefix := ""
			if alias != "" {
				prefix = alias + "."
			}
			constructor = strings.Replace(constructor, "Contract", "ModelContract", 1)
			arguments = fmt.Sprintf("bundle, golemgraphql.CustomBindingSpec{ExtensionID: %s, ResolverPackage: %s, ResolverName: %s}, %sGolemGenerated%sDescriptor, %s", strconv.Quote(string(operation.ExtensionID)), strconv.Quote(operation.Resolver.PackagePath), strconv.Quote(operation.Resolver.Name), prefix, model.Go.Name, resolver)
		}
		for _, argument := range operation.Arguments {
			conversion := "nil"
			if argument.Type.Kind == ir.GraphQLTypePredicate || argument.Type.Kind == ir.GraphQLTypeSelector || argument.Type.Kind == ir.GraphQLTypeCreateInput || argument.Type.Kind == ir.GraphQLTypeUpdateInput || argument.Type.Kind == ir.GraphQLTypeUpdateManyInput {
				model, ok := models[argument.Type.Name]
				if !ok {
					return nil, fmt.Errorf("GraphQL custom argument model %s is absent", argument.Type.Name)
				}
				alias := qualify(model.Go.PackagePath, "golemmodels")
				prefix := ""
				if alias != "" {
					prefix = alias + "."
				}
				descriptor := prefix + "GolemGenerated" + model.Go.Name + "Descriptor"
				switch argument.Type.Kind {
				case ir.GraphQLTypePredicate:
					conversion = "golemgraphql.GeneratedCustomPredicateArgument(" + descriptor + ")"
				case ir.GraphQLTypeSelector:
					conversion = "golemgraphql.GeneratedCustomSelectorArgument(" + descriptor + ")"
				case ir.GraphQLTypeCreateInput:
					conversion = "golemgraphql.GeneratedCustomMutationInputArgument(" + descriptor + ", golem.RuntimeMutationCreateInput)"
				case ir.GraphQLTypeUpdateInput:
					conversion = "golemgraphql.GeneratedCustomMutationInputArgument(" + descriptor + ", golem.RuntimeMutationUpdateInput)"
				case ir.GraphQLTypeUpdateManyInput:
					conversion = "golemgraphql.GeneratedCustomMutationInputArgument(" + descriptor + ", golem.RuntimeMutationUpdateManyInput)"
				}
			}
			arguments += ", " + conversion
		}
		bindings = append(bindings, fmt.Sprintf("golemgraphql.%s(%s)", constructor, arguments))
	}
	return bindings, nil
}

func renderSemanticSearchResolver(operation ir.CustomOperationContractIR, model ir.ModelDeclIR, _ ir.ModelContractIR, qualify func(string, string) string) (string, error) {
	if operation.Operation != ir.CustomOperationQuery || operation.Resolver.Name == "" || operation.Resolver.Kind != "customquery" {
		return "", fmt.Errorf("GraphQL semantic search operation %s is invalid", operation.Name)
	}
	exported, ok := semanticcontract.ExportedIndexName(operation.Resolver.Name)
	if !ok {
		return "", fmt.Errorf("GraphQL semantic search index %q cannot form a Go method", operation.Resolver.Name)
	}
	alias := qualify(model.Go.PackagePath, "golemmodels")
	modelType := model.Go.Name
	if alias != "" {
		modelType = alias + "." + modelType
	}
	return fmt.Sprintf("func(ctx context.Context, caller *Caller[P], args struct { Query string; Take *int32; Where *golem.Predicate[%[1]s] }) ([]golem.SemanticResult[%[1]s], error) { if args.Take == nil { return nil, fmt.Errorf(\"semantic search take is unavailable\") }; take := int(*args.Take); where := make([]golem.Predicate[%[1]s], 0, 1); if args.Where != nil { where = append(where, *args.Where) }; return caller.%[2]s.Search%[3]s(ctx, args.Query, take, where...) }", modelType, plural(model.LogicalName), exported), nil
}

func renderSemanticSimilarResolver(operation ir.CustomOperationContractIR, model ir.ModelDeclIR, _ ir.ModelContractIR, qualify func(string, string) string) (string, error) {
	if operation.Operation != ir.CustomOperationQuery || operation.Resolver.Name == "" || operation.Resolver.Kind != "customquery" {
		return "", fmt.Errorf("GraphQL semantic similar operation %s is invalid", operation.Name)
	}
	exported, ok := semanticcontract.ExportedIndexName(operation.Resolver.Name)
	if !ok {
		return "", fmt.Errorf("GraphQL semantic similar index %q cannot form a Go method", operation.Resolver.Name)
	}
	alias := qualify(model.Go.PackagePath, "golemmodels")
	modelType := model.Go.Name
	if alias != "" {
		modelType = alias + "." + modelType
	}
	return fmt.Sprintf("func(ctx context.Context, caller *Caller[P], args struct { Source golem.UniqueSelectorValue[%[1]s]; Take *int32; Where *golem.Predicate[%[1]s] }) ([]golem.SemanticResult[%[1]s], error) { if args.Take == nil { return nil, fmt.Errorf(\"semantic search take is unavailable\") }; take := int(*args.Take); where := make([]golem.Predicate[%[1]s], 0, 1); if args.Where != nil { where = append(where, *args.Where) }; return caller.%[2]s.Similar%[3]s(ctx, args.Source, take, where...) }", modelType, plural(model.LogicalName), exported), nil
}

type eventBinding struct {
	descriptor string
	eventType  string
	identity   []string
}

func renderEventBindings(compilation *ir.CompilationIR, qualify func(string, string) string) ([]eventBinding, error) {
	if compilation == nil {
		return nil, nil
	}
	contracts := make(map[ir.ModelID]ir.ModelContractIR, len(compilation.Contract.Models))
	for _, contract := range compilation.Contract.Models {
		contracts[contract.ModelID] = contract
	}
	models := append([]ir.ModelDeclIR(nil), compilation.Model.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	var result []eventBinding
	for _, model := range models {
		contract, ok := contracts[model.ID]
		if !ok || !contract.Exposed || !contract.Subscriptions {
			continue
		}
		if contract.Event == nil || model.PrimaryKey == nil || len(model.PrimaryKey.Fields) == 0 {
			return nil, fmt.Errorf("GraphQL event binding model %s is incomplete", model.ID)
		}
		alias := qualify(model.Go.PackagePath, "golemmodels")
		prefix := ""
		if alias != "" {
			prefix = alias + "."
		}
		fields := make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
		for _, field := range model.Fields {
			fields[field.ID] = field
		}
		binding := eventBinding{descriptor: prefix + "GolemGenerated" + model.Go.Name + "Descriptor", eventType: prefix + model.Go.Name + "Event"}
		if len(model.PrimaryKey.Fields) == 1 {
			binding.identity = []string{"identity"}
		} else {
			for _, fieldID := range model.PrimaryKey.Fields {
				field, present := fields[fieldID]
				if !present || field.Scalar == nil || field.GoName == "" {
					return nil, fmt.Errorf("GraphQL event binding model %s identity field %s is absent", model.ID, fieldID)
				}
				binding.identity = append(binding.identity, "identity."+field.GoName+"()")
			}
		}
		result = append(result, binding)
	}
	return result, nil
}

func resultModelName(value ir.GraphQLTypeIR) string {
	if value.Kind == ir.GraphQLTypeModel {
		return value.Name
	}
	if value.Kind == ir.GraphQLTypeList && value.Element != nil {
		return resultModelName(*value.Element)
	}
	return ""
}

func renderCallable(value ir.AttachedMethodIR, qualify func(string, string) string) (string, error) {
	if value.PackagePath == "" || value.Name == "" {
		return "", fmt.Errorf("GraphQL attached callable is incomplete")
	}
	alias := qualify(value.PackagePath, "golemresolver")
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	if value.Receiver != (ir.GoNamedTypeIR{}) {
		receiverAlias := qualify(value.Receiver.PackagePath, "golemresolver")
		receiver := value.Receiver.Name
		if receiverAlias != "" {
			receiver = receiverAlias + "." + receiver
		}
		return "new(" + receiver + ")." + value.Name, nil
	}
	return prefix + value.Name, nil
}

func plural(name string) string {
	if strings.HasSuffix(name, "y") && len(name) > 1 && !strings.ContainsRune("aeiouAEIOU", rune(name[len(name)-2])) {
		return name[:len(name)-1] + "ies"
	}
	for _, suffix := range []string{"s", "x", "z", "ch", "sh"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			return name + "es"
		}
	}
	return name + "s"
}

type root struct {
	operation string
	name      string
}
