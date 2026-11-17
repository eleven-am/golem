package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type invalidationArgs struct {
	Seed   int32
	Label  golem.InputValue[string]
	Values []int32
}
type invalidationRecording struct {
	args    invalidationArgs
	calls   int
	failure error
}

// Schema actors are named values; recording state is test instrumentation,
// rather than changing the binder's actor argument into a different pointer type.
type invalidationActor struct{ recording *invalidationRecording }

func invalidationRowsBinder(_ context.Context, actor invalidationActor, args invalidationArgs) (golem.Predicate[pageBindingModel], error) {
	actor.recording.calls++
	actor.recording.args = args
	if len(args.Values) > 0 {
		args.Values[0] = 99
	}
	return golem.All[pageBindingModel](), actor.recording.failure
}
func invalidationWrongBinder(context.Context, invalidationActor, invalidationArgs) (golem.Predicate[pageBindingModel], error) {
	return golem.All[pageBindingModel](), nil
}

func invalidationCompilation(t *testing.T) (compilerir.CompilationIR, golem.SchemaBundle, GeneratedInvalidationBindingSpec, golem.ModelDescriptor[pageBindingModel]) {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	compilation, err := policyschema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	model := generatedTestModel(t, compilation.Model, compilerir.ModelID(fmtID(fixture.User)))
	actor := compilerir.GoNamedTypeIR{PackagePath: reflect.TypeFor[invalidationActor]().PkgPath(), Name: "invalidationActor"}
	// Reuse the fixture's models as imported model packages; this schema's
	// actor and operation binders belong to the current test package.
	compilation.Model.Schema.PackagePath = actor.PackagePath
	compilation.Model.Schema.Actor = actor
	integer := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}
	operation := compilerir.CustomOperationContractIR{ExtensionID: "invalidation-test", Operation: compilerir.CustomOperationQuery, Name: "invalidationState", Capability: compilerir.CustomOperationCallerOnly,
		Resolver: compilerir.AttachedMethodIR{PackagePath: actor.PackagePath, Name: "invalidationResolver", Kind: "customquery"}, Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"},
		Arguments: []compilerir.GraphQLArgumentContractIR{{Name: "seed", Type: integer}, {Name: "label", PresenceAware: true, Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}}, {Name: "values", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &integer, Nullable: true}}},
	}
	dependency := compilerir.CustomInvalidationDependencyIR{ID: compilerir.CustomInvalidationDependencyID(operation.ExtensionID, "position"), Name: "position", Kind: compilerir.CustomInvalidationReadableRows, ModelID: model.ID, Binder: compilerir.AttachedMethodIR{PackagePath: actor.PackagePath, Name: "invalidationRowsBinder", Kind: "invalidation", Actor: &actor}, Fields: []compilerir.FieldID{compilerir.FieldID(fmtID(fixture.UserName))}}
	operation.Invalidation = &compilerir.CustomInvalidationIR{SubscriptionField: "invalidationStateChanges", Dependencies: []compilerir.CustomInvalidationDependencyIR{dependency}}
	// The policy fixture has no GraphQL projection. Author its conventional
	// names, then use the real contract/extension owners to normalize it.
	for index := range compilation.Contract.Models {
		entry := &compilation.Contract.Models[index]
		logical := generatedTestModel(t, compilation.Model, entry.ModelID)
		entry.GraphQLName = logical.LogicalName
		for field := range entry.Fields {
			entry.Fields[field].GraphQLName = graphqlcontract.LowerCamel(generatedTestFieldByID(t, logical, entry.Fields[field].FieldID).GoName)
		}
	}
	if diagnostics := graphqlcontract.Normalize(&compilation, nil); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := graphqlextension.Normalize(&compilation, nil, []graphqlextension.CustomOperationDeclaration{{Operation: operation}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	bundle := generatedTestBundle(t, compilation)
	return compilation, bundle, GeneratedInvalidationBindingSpec{OperationID: string(operation.ExtensionID), DependencyID: string(dependency.ID), BinderPackage: actor.PackagePath, BinderName: dependency.Binder.Name}, pageBindingDescriptor(t, compilation, model)
}

func fmtID[T ~[16]byte](value T) string { return fmt.Sprintf("%x", value) }

func TestGeneratedInvalidationBinderOwnsArgumentsAndActualFunctionIdentity(t *testing.T) {
	_, bundle, spec, descriptor := invalidationCompilation(t)
	binding, err := BindGeneratedInvalidationRows(bundle, spec, descriptor, invalidationRowsBinder)
	if err != nil {
		t.Fatal(err)
	}
	recording := &invalidationRecording{}
	actor := invalidationActor{recording: recording}
	values := []int32{7}
	bound, err := binding.Bind(context.Background(), actor, map[string]any{"seed": int32(4), "values": values})
	if err != nil {
		t.Fatal(err)
	}
	if recording.calls != 1 || recording.args.Seed != 4 || values[0] != 7 {
		t.Fatalf("calls=%d seed=%d original=%v", recording.calls, recording.args.Seed, values)
	}
	if recording.args.Label.IsSet() {
		t.Fatal("omitted argument became present")
	}
	if bound.Kind() != invalidationcontract.ReadableRows || bound.ModelID() != descriptor.Metadata().ModelID() {
		t.Fatal("wrong bound source")
	}
	if binding.Identity().BinderName != spec.BinderPackage+"."+spec.BinderName {
		t.Fatal("function identity lost")
	}
	if _, err := BindGeneratedInvalidationRows(bundle, spec, descriptor, invalidationWrongBinder); err == nil {
		t.Fatal("different actual function accepted under forged spec")
	}
	for _, change := range []func(*GeneratedInvalidationBindingSpec){func(s *GeneratedInvalidationBindingSpec) { s.OperationID = "wrong" }, func(s *GeneratedInvalidationBindingSpec) { s.DependencyID = "wrong" }, func(s *GeneratedInvalidationBindingSpec) { s.BinderPackage = "wrong" }, func(s *GeneratedInvalidationBindingSpec) { s.BinderName = "wrong" }} {
		bad := spec
		change(&bad)
		if _, err := BindGeneratedInvalidationRows(bundle, bad, descriptor, invalidationRowsBinder); err == nil {
			t.Fatal("forged spec accepted")
		}
	}
	recording.failure = errors.New("binder failure")
	if _, err := binding.Bind(context.Background(), actor, map[string]any{"seed": int32(1)}); !errors.Is(err, recording.failure) {
		t.Fatalf("error=%v", err)
	}
}

func TestInvalidationSubscriptionCompilesDistinctRootAndCoercesOwnedArguments(t *testing.T) {
	compilation, _, _, _ := invalidationCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, errs := gqlparser.LoadQuery(schema, `subscription Watch($seed:Int!){ changed:invalidationStateChanges(seed:$seed,values:[1,2]) }`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := compiler.Compile(query, query.Operations[0], map[string]any{"seed": int32(7)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event != nil || result.Invalidation == nil || result.Invalidation.ResponseName != "changed" || result.Invalidation.OperationID != "invalidation-test" || result.Invalidation.Arguments["seed"] != int32(7) {
		t.Fatalf("result=%+v", result)
	}
	if _, present := result.Invalidation.Arguments["label"]; present {
		t.Fatal("omitted input marker leaked into runtime supplied map")
	}
	if !reflect.DeepEqual(result.Invalidation.Arguments["values"], []any{int32(1), int32(2)}) {
		t.Fatalf("inline Int list was not coerced: %#v", result.Invalidation.Arguments["values"])
	}
}

type InvalidationFilter struct {
	Limit int32
	Mode  invalidationMode
}
type invalidationMode string

func TestInvalidationSubscriptionReusesCustomInputAndEnumCoercion(t *testing.T) {
	compilation, _, _, _ := invalidationCompilation(t)
	operation := compilation.Contract.CustomOperations[0]
	compilation.Contract.CustomOperations = nil
	const enumID compilerir.EnumID = "0000000000000000000000000000f071"
	const valueID compilerir.EnumValueID = "0000000000000000000000000000f072"
	compilation.Model.Enums = append(compilation.Model.Enums, compilerir.EnumIR{ID: enumID, LogicalName: "InvalidationMode", Go: compilerir.GoNamedTypeIR{PackagePath: reflect.TypeFor[invalidationMode]().PkgPath(), Name: "invalidationMode"}, Values: []compilerir.EnumValueIR{{ID: valueID, GoName: "InvalidationPublic", WireValue: "wire-ready"}}})
	compilation.Contract.Enums = append(compilation.Contract.Enums, compilerir.EnumContractIR{EnumID: enumID, GraphQLName: "InvalidationMode", Values: []compilerir.EnumValueContractIR{{ValueID: valueID, GraphQLName: "PUBLIC"}}})
	compilation.Contract.GraphQLTypes = append(compilation.Contract.GraphQLTypes, compilerir.GraphQLTypeDefinitionIR{Kind: compilerir.GraphQLTypeInputObject, Name: "InvalidationFilter", Go: compilerir.GoNamedTypeIR{PackagePath: reflect.TypeFor[InvalidationFilter]().PkgPath(), Name: "InvalidationFilter"}, Fields: []compilerir.GraphQLObjectFieldIR{
		{Name: "limit", GoName: "Limit", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}},
		{Name: "mode", GoName: "Mode", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeEnum, Name: "InvalidationMode"}},
	}})
	operation.Arguments = append(operation.Arguments, compilerir.GraphQLArgumentContractIR{Name: "filter", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "InvalidationFilter"}})
	if diagnostics := graphqlcontract.Normalize(&compilation, nil); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := graphqlextension.Normalize(&compilation, nil, []graphqlextension.CustomOperationDeclaration{{Operation: operation}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	const inputs = `(seed:7,values:[1,2],filter:{limit:3,mode:PUBLIC})`
	subscription, errs := gqlparser.LoadQuery(schema, `subscription { invalidationStateChanges`+inputs+` }`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	query, errs := gqlparser.LoadQuery(schema, `{ invalidationState`+inputs+` }`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	observed, err := compiler.Compile(subscription, subscription.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	read, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(observed.Invalidation.Arguments, read.Custom[0].Arguments) {
		t.Fatalf("subscription=%#v query=%#v", observed.Invalidation.Arguments, read.Custom[0].Arguments)
	}
	want := map[string]any{"limit": int32(3), "mode": "wire-ready"}
	if !reflect.DeepEqual(observed.Invalidation.Arguments["filter"], want) {
		t.Fatalf("filter=%#v", observed.Invalidation.Arguments["filter"])
	}
	variable, errs := gqlparser.LoadQuery(schema, `subscription($seed:Int!,$values:[Int!],$filter:InvalidationFilter!){ invalidationStateChanges(seed:$seed,values:$values,filter:$filter) }`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	rawFilter := map[string]any{"limit": int32(3), "mode": "PUBLIC"}
	rawValues := []any{int32(1), int32(2)}
	fromVariables, err := compiler.Compile(variable, variable.Operations[0], map[string]any{"seed": int32(7), "values": rawValues, "filter": rawFilter})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromVariables.Invalidation.Arguments, observed.Invalidation.Arguments) {
		t.Fatalf("variable=%#v literal=%#v", fromVariables.Invalidation.Arguments, observed.Invalidation.Arguments)
	}
	rawFilter["limit"], rawValues[0] = int32(99), int32(99)
	if !reflect.DeepEqual(fromVariables.Invalidation.Arguments["filter"], want) || !reflect.DeepEqual(fromVariables.Invalidation.Arguments["values"], []any{int32(1), int32(2)}) {
		t.Fatal("coerced subscription retained mutable variable inputs")
	}
	for name, supplied := range map[string]map[string]any{
		"bad seed":                      {"seed": "bad", "filter": map[string]any{"limit": 3, "mode": "PUBLIC"}},
		"bad list element":              {"seed": 7, "values": []any{1, "bad"}, "filter": map[string]any{"limit": 3, "mode": "PUBLIC"}},
		"unknown enum":                  {"seed": 7, "filter": map[string]any{"limit": 3, "mode": "UNKNOWN"}},
		"wire enum is not public input": {"seed": 7, "filter": map[string]any{"limit": 3, "mode": "wire-ready"}},
		"unknown input field":           {"seed": 7, "filter": map[string]any{"limit": 3, "mode": "PUBLIC", "extra": true}},
		"missing required input":        {"seed": 7, "filter": map[string]any{"mode": "PUBLIC"}},
		"wrong input shape":             {"seed": 7, "filter": "wrong"},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := compiler.Compile(variable, variable.Operations[0], supplied)
			if err == nil || result.Invalidation != nil {
				t.Fatalf("malformed input accepted: %+v %v", result, err)
			}
		})
	}
}

func TestInvalidationBundleRequiresCapabilityEvenThroughExecutorWrapperAndOmittedHTTPBlock(t *testing.T) {
	compilation, bundle, _, _ := invalidationCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	executor := &testExecutor{}
	server, err := NewServer(document.SDL, Config[int]{Bundle: bundle, ContractFingerprint: bundle.Contract().Fingerprint(), PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := bundle.Contract().Fingerprint().String()
	for _, test := range []struct {
		caps string
		want handshakeRefusal
	}{{"", refusedCapability}, {`,"capabilities":[]`, refusedCapability}, {`,"capabilities":null`, refusedCapability}, {`,"capabilities":["unknown"]`, refusedCapability}, {`,"capabilities":["operation-invalidation-v1"]`, ""}} {
		block := json.RawMessage(`{"protocol":1,"contract":"` + fingerprint + `","manifestVersion":3` + test.caps + `}`)
		if got := server.checkHandshake(block); got != test.want {
			t.Fatalf("%s got=%q want=%q", block, got, test.want)
		}
	}
	if failure := server.handshakeFailure(nil, false); failure == nil || failure.Extensions["code"] != codeProtocolMismatch {
		t.Fatal("missing HTTP block bypassed capability")
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewBufferString(`{"query":"{ invalidationState(seed:1) }"}`)))
	if executor.called {
		t.Fatal("old client reached executor")
	}
	bad := Config[int]{Bundle: bundle, PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(context.Context, error) {}}
	if _, err := NewServer(document.SDL, bad, executor); err == nil {
		t.Fatal("wrong active contract fingerprint accepted")
	}
}

type invalidationWaitProbe struct {
	calls, closed int
	terminal      error
}

func (s *invalidationWaitProbe) Wait(context.Context) error { s.calls++; return s.terminal }
func (s *invalidationWaitProbe) Close() error               { s.closed++; return nil }
func TestInvalidationResponseStreamHasNoDataOutcome(t *testing.T) {
	terminal := errors.New("terminal-resync")
	source := &invalidationWaitProbe{terminal: terminal}
	stream := &invalidationResponseStream{source: source}
	result, err := stream.Recv(context.Background())
	if !errors.Is(err, terminal) || result.Data != nil || source.calls != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	source.terminal = nil
	if result, err := stream.Recv(context.Background()); err == nil || result.Data != nil {
		t.Fatal("successful Wait manufactured a data packet")
	}
	_ = stream.Close()
	if err := stream.Close(); err != nil || source.closed != 1 {
		t.Fatal("source not closed")
	}
}
