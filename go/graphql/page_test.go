package graphql

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	graphqlcustom "github.com/eleven-am/golem/go/internal/operationargs"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type pageBindingArgs struct {
	Seed  int32
	Label golem.InputValue[string]
}
type pageBindingTypedCaller struct{ marker string }
type pageBindingModel struct{}

type pageBindingApp struct{ identity golem.DataIncarnation }

func (app pageBindingApp) DataIncarnation() golem.DataIncarnation { return app.identity }

func pageBindingExecutor(t *testing.T, bundle golem.SchemaBundle, binding CustomBinding, caller *pageBindingCaller) Executor[int] {
	t.Helper()
	identity, err := dataincarnation.Parse("11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	// The adapter spy supplies the same admitted-App prerequisites as generated
	// bindings. Durable identity admission and authentication have their own tests.
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: bundle, CustomBindings: []CustomBinding{binding},
		DataIncarnationSource: pageBindingApp{identity: identity},
		AuthenticatePrincipal: func(context.Context, int) error { return nil },
		BeginCaller:           func(context.Context, int) (CallerExecution, error) { return caller, nil },
		ReportInternalError:   func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

// This implements exactly the generated production capability, including the
// ordinary public-Caller provider. Crypto/admission behavior belongs to runtime
// tests; this probe controls only the adapter's owned-input/result boundaries.
type pageBindingCaller struct {
	typed                        *pageBindingTypedCaller
	owned                        map[string]any
	arguments                    map[string]any
	input                        golem.PageInput
	registered                   any
	dispatches, reads, finalized int
	rows                         []golem.RuntimeModelRow
	readErr, finalizeErr         error
	more                         bool
	position                     []byte
	checkBatch                   func([]any, uint32, []byte, bool) error
}

func (*pageBindingCaller) GolemGraphQLCallerCapability()             {}
func (caller *pageBindingCaller) GolemGraphQLCustomCallerValue() any { return caller.typed }
func (caller *pageBindingCaller) ExecuteFrozenRead(context.Context, golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	caller.reads++
	return caller.rows, caller.readErr
}
func (caller *pageBindingCaller) GolemGraphQLDispatchPage(ctx context.Context, id string, resolver any, arguments map[string]any, input golem.PageInput, run func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error)) (golem.RuntimePageResult, error) {
	caller.dispatches++
	if id != "page-binding" || reflect.ValueOf(resolver).Pointer() != reflect.ValueOf(caller.registered).Pointer() {
		return golem.RuntimePageResult{}, errors.New("wrong registered page resolver")
	}
	caller.arguments, caller.input = arguments, input
	owned := caller.owned
	if owned == nil {
		owned = arguments
	}
	take := input.Take
	if take == 0 {
		take = 2
	}
	batch, err := run(ctx, golem.RuntimePageStateValue(take, 4, 17, []byte("admitted-position"), time.Now().Add(time.Hour)), owned)
	if err != nil {
		return golem.RuntimePageResult{}, err
	}
	items, consumed, position, more := golem.RuntimePageBatchParts(batch)
	caller.more, caller.position = more, position
	if caller.checkBatch != nil {
		if err := caller.checkBatch(items, consumed, position, more); err != nil {
			return golem.RuntimePageResult{}, err
		}
	}
	return golem.RuntimePageResultValue(items, func() (string, error) {
		caller.finalized++
		if caller.finalizeErr != nil {
			return "", caller.finalizeErr
		}
		if more {
			return "opaque-continuation", nil
		}
		return "", nil
	}), nil
}

func pageBindingCompilation(t *testing.T, item compilerir.GraphQLTypeIR) (compilerir.CompilationIR, golem.SchemaBundle, compilerir.CustomOperationContractIR) {
	t.Helper()
	compilation, _ := incarnationCompilation(t)
	page := compilerir.CustomPaginationIR{DefaultTake: 2, MaxTake: 2, MaxCandidates: 4, Revision: "binding-v1", Item: item}
	operation := compilerir.CustomOperationContractIR{ExtensionID: "page-binding", Operation: compilerir.CustomOperationQuery, Name: "paged", Capability: compilerir.CustomOperationCallerOnly,
		Resolver: compilerir.AttachedMethodIR{PackagePath: compilation.Model.Schema.PackagePath, Name: "Paged", Kind: "customquery"},
		Result:   compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "BindingPage"}, Pagination: &page,
		Arguments: []compilerir.GraphQLArgumentContractIR{
			{Name: "seed", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}},
			{Name: "label", PresenceAware: true, Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}},
			{Name: "take", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
			{Name: "after", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}},
		},
	}
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{operation}
	compilation.Contract.GraphQLTypes = append(compilation.Contract.GraphQLTypes, compilerir.GraphQLPageDefinition("BindingPage", page))
	return compilation, generatedTestBundle(t, compilation), operation
}

func TestGeneratedPageExecutionBoundsInputBeforeCallingDispatcher(t *testing.T) {
	type args struct {
		Seed   int32
		Label  golem.InputValue[string]
		Values []int32
	}
	integer := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}
	compilation, _, operation := pageBindingCompilation(t, integer)
	operation.Arguments = append(operation.Arguments, compilerir.GraphQLArgumentContractIR{Name: "values", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &integer}})
	compilation.Contract.CustomOperations[0] = operation
	bundle := generatedTestBundle(t, compilation)
	resolverCalls := 0
	resolver := func(context.Context, *pageBindingTypedCaller, golem.PageRequest[args]) (golem.PageBatch[int32], error) {
		resolverCalls++
		return golem.PageItems([]int32{1}, 1, nil), nil
	}
	binding, err := BindGeneratedPageQueryContract(bundle, CustomBindingSpec{ExtensionID: string(operation.ExtensionID), ResolverPackage: operation.Resolver.PackagePath, ResolverName: operation.Resolver.Name}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewCustomRegistry(bundle, binding)
	if err != nil {
		t.Fatal(err)
	}
	// This dispatcher is a boundary spy: exported native execution must reject
	// oversized page inputs before any dispatcher is allowed to see them.
	caller := &pageBindingCaller{typed: &pageBindingTypedCaller{}, registered: resolver}
	execution, err := registry.ForCaller(caller)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, graphqlcustom.MaxCanonicalArgumentNodes+1)
	for index := range values {
		values[index] = int32(index)
	}
	_, err = execution.Execute(context.Background(), CustomQuery, operation.Name, map[string]any{"seed": int32(7), "values": values})
	var invariant interface{ Cause() error }
	if !errors.As(err, &invariant) || !errors.Is(invariant.Cause(), graphqlcustom.ErrCanonicalArgumentsLimit) || caller.dispatches != 0 || resolverCalls != 0 {
		t.Fatalf("oversized input reached native page dispatch before admission: dispatches=%d resolver=%d err=%v", caller.dispatches, resolverCalls, err)
	}
}

func pageBindingDescriptor(t *testing.T, compilation compilerir.CompilationIR, model compilerir.ModelDeclIR) golem.ModelDescriptor[pageBindingModel] {
	t.Helper()
	modelID := generatedTestModelID(t, model.ID)
	var contract compilerir.ModelContractIR
	for _, candidate := range compilation.Contract.Models {
		if candidate.ModelID == model.ID {
			contract = candidate
			break
		}
	}
	readOnly := map[compilerir.FieldID]bool{}
	for _, field := range contract.Fields {
		for _, mode := range field.Modes {
			if mode == compilerir.ModeReadOnly {
				readOnly[field.FieldID] = true
			}
		}
	}
	fields := append([]compilerir.FieldIR(nil), model.Fields...)
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].DeclarationOrder != fields[j].DeclarationOrder {
			return fields[i].DeclarationOrder < fields[j].DeclarationOrder
		}
		return fields[i].ID < fields[j].ID
	})
	var scan, write []golem.FieldID
	var relations []golem.RelationMetadata
	for _, field := range fields {
		id := generatedTestFieldID(t, field.ID)
		if field.Scalar != nil {
			scan = append(scan, id)
			if field.Scalar.Generation == nil && !field.Scalar.DatabaseReadOnly && !readOnly[field.ID] {
				write = append(write, id)
			}
			continue
		}
		if field.Relation == nil {
			t.Fatal("model field has no descriptor payload")
		}
		var relation compilerir.RelationIR
		found := false
		for _, candidate := range compilation.Model.Relations {
			if candidate.ID == field.Relation.RelationID {
				relation, found = candidate, true
				break
			}
		}
		if !found {
			t.Fatal("model relation is absent")
		}
		target, role := relation.TargetModel, golem.RelationSource
		if field.Relation.Role == compilerir.RelationInverse {
			target, role = relation.SourceModel, golem.RelationInverse
		}
		cardinality := golem.RelationToOne
		if field.Relation.Kind == compilerir.RelationHasMany {
			cardinality = golem.RelationToMany
		}
		var relationID golem.RelationID
		generatedTestFixedID(t, string(relation.ID), relationID[:])
		relations = append(relations, golem.GeneratedRelationMetadata(modelID, generatedTestModelID(t, target), id, relationID, role, cardinality))
	}
	var identities []golem.IdentityMetadata
	for _, selector := range contract.Selectors {
		var key golem.KeyID
		generatedTestFixedID(t, string(selector.KeyID), key[:])
		kind := golem.PrimaryIdentity
		if selector.Kind == compilerir.KeyUnique {
			kind = golem.UniqueIdentity
		}
		fields := make([]golem.FieldID, len(selector.Fields))
		for index, field := range selector.Fields {
			fields[index] = generatedTestFieldID(t, field)
		}
		identities = append(identities, golem.GeneratedIdentityMetadata(modelID, key, kind, fields...))
	}
	return golem.GeneratedModelDescriptor[pageBindingModel](modelID, golem.GeneratedDescriptorShape(scan, write, identities, relations))
}

func pageBindingRoot(t *testing.T, compilation compilerir.CompilationIR, query string) (*graphqloperation.Compiler, graphqloperation.CustomRoot) {
	t.Helper()
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	parsed, errs := gqlparser.LoadQuery(schema, query)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(parsed, parsed.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Custom) != 1 {
		t.Fatal("one page root expected")
	}
	return compiler, compiled.Custom[0]
}

func TestGeneratedPageBindingUsesRuntimeOwnedArgumentsAndDefersContinuation(t *testing.T) {
	compilation, bundle, operation := pageBindingCompilation(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"})
	for _, presence := range []string{"omitted", "null", "value"} {
		t.Run(presence, func(t *testing.T) {
			caller := &pageBindingCaller{typed: &pageBindingTypedCaller{marker: "public-caller"}, owned: map[string]any{"seed": int32(9)}}
			supplied := map[string]any{"seed": int32(7), "take": int32(2), "after": "opaque-input"}
			if presence == "null" {
				supplied["label"] = nil
				caller.owned["label"] = nil
			}
			if presence == "value" {
				supplied["label"] = "label"
				caller.owned["label"] = "owned-label"
			}
			calls := 0
			resolver := func(_ context.Context, actual *pageBindingTypedCaller, request golem.PageRequest[pageBindingArgs]) (golem.PageBatch[int32], error) {
				calls++
				if actual != caller.typed || request.Arguments().Seed != 9 || request.Take() != 2 || request.CandidateLimit() != 4 || request.Consumed() != 17 || string(request.State()) != "admitted-position" {
					t.Fatalf("typed request/caller mismatch: %#v", request)
				}
				label := request.Arguments().Label
				if label.IsSet() != (presence != "omitted") || label.IsNull() != (presence == "null") {
					t.Fatalf("input presence lost: %#v", label)
				}
				if presence == "value" {
					if value, ok := label.Get(); !ok || value != "owned-label" {
						t.Fatal("decoded caller-owned input instead of runtime-owned input")
					}
				}
				return golem.PageItems([]int32{42}, 1, []byte("next-position")), nil
			}
			caller.registered = resolver
			binding, err := BindGeneratedPageQueryContract(bundle, customSpec(operation), resolver)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewCustomRegistry(bundle, binding)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := registry.ForCaller(caller)
			if err != nil {
				t.Fatal(err)
			}
			result, err := execution.Execute(context.Background(), CustomQuery, "paged", supplied)
			if err != nil {
				t.Fatal(err)
			}
			if caller.dispatches != 1 || calls != 1 || caller.finalized != 0 || caller.input != (golem.PageInput{Take: 2, After: "opaque-input"}) {
				t.Fatalf("dispatch=%d resolver=%d finalized=%d input=%+v", caller.dispatches, calls, caller.finalized, caller.input)
			}
			if _, ok := caller.arguments["take"]; ok {
				t.Fatal("take leaked into ordinary args")
			}
			if _, ok := caller.arguments["after"]; ok {
				t.Fatal("after leaked into ordinary args")
			}
			if _, present := caller.arguments["label"]; present != (presence != "omitted") {
				t.Fatal("ordinary omission became explicit null")
			}
			compiler, root := pageBindingRoot(t, compilation, `{ paged(seed:7) { items next } }`)
			encoded, failures, err := compiler.EncodeCustomWithComputedPartial(context.Background(), root, result.Value(), func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, selectset.Slot) ([]any, error) {
				t.Fatal("unexpected computed resolver")
				return nil, nil
			})
			if err != nil || len(failures) != 0 {
				t.Fatalf("page encode err=%v failures=%v", err, failures)
			}
			value, err := result.finalizeValue(compiler, root, encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, map[string]any{"items": []any{int32(42)}, "next": "opaque-continuation"}) || caller.finalized != 1 || result.Value().(map[string]any)["next"] != nil {
				t.Fatalf("final value=%#v finalized=%d", value, caller.finalized)
			}
		})
	}
}

func TestGeneratedPageBindingRefusesOrdinaryDispatcherAndInvalidItems(t *testing.T) {
	_, bundle, operation := pageBindingCompilation(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"})
	for _, items := range [][]any{{"wrong-scalar"}, {nil}, {int32(1), int32(2), int32(3)}} {
		caller := &pageBindingCaller{typed: &pageBindingTypedCaller{}}
		resolver := func(context.Context, *pageBindingTypedCaller, golem.PageRequest[pageBindingArgs]) (golem.PageBatch[any], error) {
			return golem.PageItems(items, uint32(len(items)), nil), nil
		}
		caller.registered = resolver
		binding, err := BindGeneratedPageQueryContract(bundle, customSpec(operation), resolver)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := NewCustomRegistry(bundle, binding)
		if err != nil {
			t.Fatal(err)
		}
		execution, _ := registry.ForCaller(caller)
		if _, err := execution.Execute(context.Background(), CustomQuery, "paged", map[string]any{"seed": int32(7)}); err == nil || caller.finalized != 0 {
			t.Fatalf("invalid page items were finalized: %#v err=%v", items, err)
		}
		wrong, _ := registry.ForCaller(&customTypedCaller{})
		if _, err := wrong.Execute(context.Background(), CustomQuery, "paged", map[string]any{"seed": int32(7)}); err == nil {
			t.Fatal("page resolver ran without registered dispatcher")
		}
	}
	ordinary, err := BindCustomQuery(customSpec(operation), func([]CustomArgument) (struct{}, error) { return struct{}{}, nil }, func(context.Context, *customTypedCaller, struct{}) (map[string]any, error) {
		return map[string]any{"items": []any{}, "next": "forged"}, nil
	}, func(value map[string]any) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCustomRegistry(bundle, ordinary); err == nil {
		t.Fatal("ordinary custom binding bypassed page runtime registration")
	}
}

func TestGeneratedPageBindingPreservesEmptyCheckpointForRuntimeRefusal(t *testing.T) {
	_, bundle, operation := pageBindingCompilation(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"})
	refused := errors.New("runtime refused non-advancing checkpoint")
	caller := &pageBindingCaller{typed: &pageBindingTypedCaller{}, checkBatch: func(_ []any, _ uint32, position []byte, more bool) error {
		if !more || position == nil || len(position) != 0 {
			t.Fatal("non-nil empty checkpoint was silently changed to terminal")
		}
		return refused
	}}
	resolver := func(context.Context, *pageBindingTypedCaller, golem.PageRequest[pageBindingArgs]) (golem.PageBatch[int32], error) {
		return golem.PageItems([]int32{}, 0, []byte{}), nil
	}
	caller.registered = resolver
	binding, err := BindGeneratedPageQueryContract(bundle, customSpec(operation), resolver)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewCustomRegistry(bundle, binding)
	if err != nil {
		t.Fatal(err)
	}
	execution, _ := registry.ForCaller(caller)
	if _, err := execution.Execute(context.Background(), CustomQuery, "paged", map[string]any{"seed": int32(7)}); !errors.Is(err, refused) || caller.finalized != 0 {
		t.Fatalf("checkpoint refusal err=%v finalized=%d", err, caller.finalized)
	}
}

func TestGeneratedPageBindingDefaultsOnlyAbsentOrNullTake(t *testing.T) {
	_, bundle, operation := pageBindingCompilation(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"})
	for _, kind := range []string{"omitted", "null", "zero", "negative"} {
		t.Run(kind, func(t *testing.T) {
			caller := &pageBindingCaller{typed: &pageBindingTypedCaller{}}
			calls := 0
			resolver := func(_ context.Context, _ *pageBindingTypedCaller, request golem.PageRequest[pageBindingArgs]) (golem.PageBatch[int32], error) {
				calls++
				if request.Take() != 2 {
					t.Fatalf("default take=%d", request.Take())
				}
				return golem.PageItems([]int32{1}, 1, nil), nil
			}
			caller.registered = resolver
			binding, err := BindGeneratedPageQueryContract(bundle, customSpec(operation), resolver)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewCustomRegistry(bundle, binding)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := registry.ForCaller(caller)
			if err != nil {
				t.Fatal(err)
			}
			arguments := map[string]any{"seed": int32(7)}
			switch kind {
			case "null":
				arguments["take"] = nil
			case "zero":
				arguments["take"] = int32(0)
			case "negative":
				arguments["take"] = int32(-1)
			}
			_, err = execution.Execute(context.Background(), CustomQuery, "paged", arguments)
			if kind == "omitted" || kind == "null" {
				if err != nil || calls != 1 || caller.dispatches != 1 || caller.input.Take != 0 {
					t.Fatalf("default input=%+v calls=%d dispatches=%d err=%v", caller.input, calls, caller.dispatches, err)
				}
				return
			}
			var invalid *golem.Error
			if !errors.As(err, &invalid) || invalid.Code != golem.CodeBadUserInput || invalid.Reason != golem.ReasonInvalidValue || calls != 0 || caller.dispatches != 0 {
				t.Fatalf("explicit invalid take reached resolver: calls=%d dispatches=%d err=%v", calls, caller.dispatches, err)
			}
		})
	}
}

type PageBoundItem struct{ Values []int32 }

func TestGeneratedPageBindingBoundsAdmittedTakeBeforeTraversingDTOItems(t *testing.T) {
	compilation, _, operation := pageBindingCompilation(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"})
	operation.Pagination.Item = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "PageBoundItem"}
	compilation.Contract.CustomOperations[0] = operation
	for index := range compilation.Contract.GraphQLTypes {
		if compilation.Contract.GraphQLTypes[index].Name == "BindingPage" {
			compilation.Contract.GraphQLTypes[index] = compilerir.GraphQLPageDefinition("BindingPage", *operation.Pagination)
		}
	}
	compilation.Contract.GraphQLTypes = append(compilation.Contract.GraphQLTypes, compilerir.GraphQLTypeDefinitionIR{
		Kind: compilerir.GraphQLTypeObject, Name: "PageBoundItem", Go: compilerir.GoNamedTypeIR{PackagePath: reflect.TypeFor[PageBoundItem]().PkgPath(), Name: "PageBoundItem"},
		Fields: []compilerir.GraphQLObjectFieldIR{{Name: "values", GoName: "Values", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}, MaxItems: 10}}},
	})
	bundle := generatedTestBundle(t, compilation)
	caller := &pageBindingCaller{typed: &pageBindingTypedCaller{}}
	calls := 0
	resolver := func(_ context.Context, _ *pageBindingTypedCaller, request golem.PageRequest[pageBindingArgs]) (golem.PageBatch[PageBoundItem], error) {
		calls++
		if request.Take() != 1 {
			t.Fatalf("admitted take=%d", request.Take())
		}
		// Traversing the second DTO would report its nested maximum10.
		// The outer admitted maximum1 must refuse before that traversal.
		return golem.PageItems([]PageBoundItem{{Values: []int32{1}}, {Values: make([]int32, 11)}}, 2, nil), nil
	}
	caller.registered = resolver
	binding, err := BindGeneratedPageQueryContract(bundle, customSpec(operation), resolver)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewCustomRegistry(bundle, binding)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.ForCaller(caller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = execution.Execute(context.Background(), CustomQuery, "paged", map[string]any{"seed": int32(7), "take": int32(1)})
	var limit *golem.Error
	if !errors.As(err, &limit) || limit.Code != golem.CodeLimitExceeded || limit.Limit != golem.LimitListItems || limit.LimitMax != 1 || calls != 1 || caller.finalized != 0 {
		t.Fatalf("admitted item bound was late/missing: calls=%d finalized=%d err=%v", calls, caller.finalized, err)
	}
}

func TestGeneratedPageExecutorHydratesAliasesBeforeFinalizing(t *testing.T) {
	compilation, bundle, operation := pageBindingCompilation(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"})
	contract := generatedTestContract(t, compilation.Contract, "Post")
	model := generatedTestModel(t, compilation.Model, contract.ModelID)
	modelID := generatedTestModelID(t, model.ID)
	idField := generatedTestFieldID(t, model.PrimaryKey.Fields[0])
	titleField := generatedTestFieldID(t, generatedTestField(t, model, "Title").ID)
	identityCell := golem.RuntimePresentReadCell(idField, golem.UUID{15: 1}, nil)
	identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{idField}, []golem.RuntimeReadCell{identityCell})
	if err != nil {
		t.Fatal(err)
	}
	row := func(title string) golem.RuntimeModelRow {
		value, err := golem.RuntimeModelReadRow(modelID, identityCell, golem.RuntimePresentReadCell(titleField, title, nil))
		if err != nil {
			t.Fatal(err)
		}
		return golem.RuntimeModelRowWithIdentity(value, identity)
	}
	typed, err := golem.RuntimeTypedReadRow(pageBindingDescriptor(t, compilation, model), row("forged-stale-cell"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "denied", "hydration-error", "finalize-error"} {
		t.Run(mode, func(t *testing.T) {
			caller := &pageBindingCaller{typed: &pageBindingTypedCaller{}, rows: []golem.RuntimeModelRow{row("fresh-authorized")}}
			if mode == "denied" {
				caller.rows = nil
			}
			if mode == "hydration-error" {
				caller.readErr = errors.New("hydration refused")
			}
			if mode == "finalize-error" {
				caller.finalizeErr = errors.New("expired before finalization")
			}
			resolver := func(context.Context, *pageBindingTypedCaller, golem.PageRequest[pageBindingArgs]) (golem.PageBatch[golem.Row[pageBindingModel]], error) {
				return golem.PageItems([]golem.Row[pageBindingModel]{typed}, 1, []byte("next-position")), nil
			}
			caller.registered = resolver
			binding, err := BindGeneratedPageQueryContract(bundle, customSpec(operation), resolver)
			if err != nil {
				t.Fatal(err)
			}
			executor := pageBindingExecutor(t, bundle, binding, caller)
			document, err := graphqlschema.Build(compilation)
			if err != nil {
				t.Fatal(err)
			}
			server, err := NewServer(document.SDL, Config[int]{Bundle: bundle, ContractFingerprint: bundle.Contract().Fingerprint(), PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(context.Context, error) {}}, executor)
			if err != nil {
				t.Fatal(err)
			}
			response := server.Execute(context.Background(), 1, Request{Query: `{ output: paged(seed:7) { a: items { title } b: items { id } token: next again: next } }`})
			if mode == "hydration-error" || mode == "finalize-error" {
				if len(response.Errors) != 1 {
					t.Fatalf("failed page response=%#v", response)
				}
				want := 1
				if mode == "hydration-error" {
					want = 0
				}
				if caller.finalized != want {
					t.Fatalf("failed page finalized=%d want%d", caller.finalized, want)
				}
				return
			}
			if len(response.Errors) != 0 || caller.dispatches != 1 || caller.finalized != 1 || caller.reads == 0 {
				t.Fatalf("response=%#v dispatch=%d finalized=%d reads=%d", response, caller.dispatches, caller.finalized, caller.reads)
			}
			page := response.Data.(map[string]any)["output"].(map[string]any)
			if mode == "denied" {
				if len(page["a"].([]any)) != 0 || len(page["b"].([]any)) != 0 || page["token"] != "opaque-continuation" {
					t.Fatalf("denied candidates not pruned: %#v", page)
				}
				return
			}
			if page["token"] != "opaque-continuation" || page["again"] != "opaque-continuation" || page["a"].([]any)[0].(map[string]any)["title"] != "fresh-authorized" {
				t.Fatalf("hydrated/aliased page=%#v", page)
			}
			if _, present := page["next"]; present {
				t.Fatal("canonical next leaked beside aliases")
			}
		})
	}
}

type PageRequiredItem struct{ Post golem.Row[pageBindingModel] }

func TestGeneratedPageExecutorDoesNotMintOnRequiredModelEncodingFailure(t *testing.T) {
	for _, gated := range []bool{false, true} {
		name := "required-model"
		if gated {
			name = "row-gated-model"
		}
		t.Run(name, func(t *testing.T) {
			compilation, _, operation := pageBindingCompilation(t, compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"})
			model := generatedTestModel(t, compilation.Model, generatedTestContract(t, compilation.Contract, "Post").ModelID)
			modelID := generatedTestModelID(t, model.ID)
			idField := generatedTestFieldID(t, model.PrimaryKey.Fields[0])
			cell := golem.RuntimePresentReadCell(idField, golem.UUID{15: 1}, nil)
			identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{idField}, []golem.RuntimeReadCell{cell})
			if err != nil {
				t.Fatal(err)
			}
			row, err := golem.RuntimeModelReadRow(modelID, cell)
			if err != nil {
				t.Fatal(err)
			}
			typed, err := golem.RuntimeTypedReadRow(pageBindingDescriptor(t, compilation, model), golem.RuntimeModelRowWithIdentity(row, identity))
			if err != nil {
				t.Fatal(err)
			}
			operation.Pagination.Item = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "PageRequiredItem"}
			compilation.Contract.CustomOperations[0] = operation
			for index := range compilation.Contract.GraphQLTypes {
				if compilation.Contract.GraphQLTypes[index].Name == "BindingPage" {
					compilation.Contract.GraphQLTypes[index] = compilerir.GraphQLPageDefinition("BindingPage", *operation.Pagination)
				}
			}
			definition := compilerir.GraphQLTypeDefinitionIR{Kind: compilerir.GraphQLTypeObject, Name: "PageRequiredItem", Go: compilerir.GoNamedTypeIR{PackagePath: reflect.TypeFor[PageRequiredItem]().PkgPath(), Name: "PageRequiredItem"}, Fields: []compilerir.GraphQLObjectFieldIR{{Name: "post", GoName: "Post", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "Post"}}}}
			if gated {
				definition.RowGate = "post"
			}
			compilation.Contract.GraphQLTypes = append(compilation.Contract.GraphQLTypes, definition)
			bundle := generatedTestBundle(t, compilation)
			caller := &pageBindingCaller{typed: &pageBindingTypedCaller{}}
			resolver := func(context.Context, *pageBindingTypedCaller, golem.PageRequest[pageBindingArgs]) (golem.PageBatch[PageRequiredItem], error) {
				return golem.PageItems([]PageRequiredItem{{Post: typed}}, 1, []byte("next-position")), nil
			}
			caller.registered = resolver
			binding, err := BindGeneratedPageQueryContract(bundle, customSpec(operation), resolver)
			if err != nil {
				t.Fatal(err)
			}
			executor := pageBindingExecutor(t, bundle, binding, caller)
			document, err := graphqlschema.Build(compilation)
			if err != nil {
				t.Fatal(err)
			}
			server, err := NewServer(document.SDL, Config[int]{Bundle: bundle, ContractFingerprint: bundle.Contract().Fingerprint(), PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(context.Context, error) {}}, executor)
			if err != nil {
				t.Fatal(err)
			}
			response := server.Execute(context.Background(), 1, Request{Query: `{ paged(seed:7) { items { post { title } } next } }`})
			if gated {
				if len(response.Errors) != 0 || caller.finalized != 1 {
					t.Fatalf("row-gated page=%#v finalized=%d", response, caller.finalized)
				}
				page := response.Data.(map[string]any)["paged"].(map[string]any)
				if len(page["items"].([]any)) != 0 || page["next"] != "opaque-continuation" {
					t.Fatalf("row gate failed to drop candidate: %#v", page)
				}
			} else if len(response.Errors) == 0 || caller.finalized != 0 {
				t.Fatalf("required model failure minted token: response=%#v finalized=%d", response, caller.finalized)
			}
		})
	}
}
