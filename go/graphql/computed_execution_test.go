package graphql

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

func TestGraphQLComputedDependenciesAreSelectedMaskedAndWithheld(t *testing.T) {
	fixture := computedFixture(t)
	document := computedQuery(t, `{ `+fixture.contract.Roots.FindMany+` { id greeting(prefix: "hello") } }`)
	compiler, err := graphqloperation.New(fixture.compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(document, document.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Reads[0].Request.Selection()) != 2 || len(compiled.Reads[0].Slots) != 2 {
		t.Fatalf("dependency was not selected and withheld: %#v", compiled.Reads[0])
	}
	observedMasked, observedWithheld := false, false
	greeting, err := BindComputed(string(fixture.greeting.ExtensionID), func(_ context.Context, request ComputedRequest) (any, error) {
		name := golem.RuntimeTransportField(request.Parent, fixture.name)
		id := golem.RuntimeTransportField(request.Parent, fixture.id)
		observedMasked = name.State() == golem.ReadNull
		observedWithheld = id.State() == golem.ReadUnselected
		if len(request.Arguments) != 1 || request.Arguments[0].Name != "prefix" || request.Arguments[0].Value != "hello" {
			return nil, fmt.Errorf("typed arguments drifted: %#v", request.Arguments)
		}
		return "masked", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := fixture.batchBinding(t, nil, nil)
	execution, err := newComputedExecution(fixture.compilation, []ComputedBinding{greeting, batch}, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.close()
	row := fixture.row(t, fixture.uuid(1), true)
	encoded, err := compiler.EncodeReadWithComputed(context.Background(), compiled.Reads[0], []golem.RuntimeModelRow{row}, execution.resolve)
	if err != nil {
		t.Fatal(err)
	}
	item := encoded.([]any)[0].(map[string]any)
	if !observedMasked || !observedWithheld || item["greeting"] != "masked" {
		t.Fatalf("masked=%v withheld=%v encoded=%#v", observedMasked, observedWithheld, item)
	}
	if _, leaked := item["name"]; leaked {
		t.Fatalf("dependency leaked into response: %#v", item)
	}
	// Relation occurrences are remapped into the ordinary typed-row field cell,
	// while unrelated public fields remain physically absent.
	relationField := golem.FieldID{9}
	source, err := golem.RuntimeModelReadRowWithOccurrences(fixture.model,
		[]golem.RuntimeReadCell{golem.RuntimePresentReadCell(fixture.id, fixture.uuid(4), nil)}, nil,
		[]golem.RuntimeOccurrenceCell{golem.RuntimeNullOccurrenceCell(relationField, 7)},
	)
	if err != nil {
		t.Fatal(err)
	}
	dependencyRow, err := golem.RuntimeComputedDependencyRow(source, nil, []golem.RuntimeComputedRelationDependency{{Field: relationField, Occurrence: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if golem.RuntimeTransportField(dependencyRow, relationField).State() != golem.ReadNull || golem.RuntimeTransportField(dependencyRow, fixture.id).State() != golem.ReadUnselected {
		t.Fatal("masked relation dependency was not isolated from unrelated fields")
	}
}

func TestGraphQLBatchedComputedFieldKeysArgumentsLimitsFailuresAndWriteInvalidation(t *testing.T) {
	fixture := computedFixture(t)
	document := computedQuery(t, `{ `+fixture.contract.Roots.FindMany+` { first: batchGreeting(prefix: "hi") second: batchGreeting(prefix: "bye") } }`)
	compiler, err := graphqloperation.New(fixture.compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(document, document.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := []golem.RuntimeModelRow{
		fixture.row(t, fixture.uuid(1), false), fixture.row(t, fixture.uuid(2), false), fixture.row(t, fixture.uuid(3), false),
	}
	var batchSizes []int
	var argumentKeys []string
	batch := fixture.batchBinding(t, func(parents []ComputedBatchParent, arguments []ComputedArgument) map[string]ComputedBatchResult {
		batchSizes = append(batchSizes, len(parents))
		argumentKeys = append(argumentKeys, fmt.Sprintf("%s=%v", arguments[0].Name, arguments[0].Value))
		result := make(map[string]ComputedBatchResult, len(parents))
		for _, parent := range parents {
			result[parent.CacheKey()] = ComputedBatchResult{Value: "hello-" + parent.CacheKey()}
		}
		return result
	}, nil)
	greeting := fixture.greetingBinding(t)
	execution, err := newComputedExecution(fixture.compilation, []ComputedBinding{greeting, batch}, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.close()
	for pass := 0; pass < 2; pass++ {
		if _, err := compiler.EncodeReadWithComputed(context.Background(), compiled.Reads[0], rows, execution.resolve); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(batchSizes) != "[2 1 2 1]" || fmt.Sprint(argumentKeys) != "[prefix=hi prefix=hi prefix=bye prefix=bye]" {
		t.Fatalf("batch sizes=%v arguments=%v", batchSizes, argumentKeys)
	}
	execution.invalidateAfterWrite()
	if _, err := compiler.EncodeReadWithComputed(context.Background(), compiled.Reads[0], rows, execution.resolve); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(batchSizes) != "[2 1 2 1 2 1 2 1]" {
		t.Fatalf("write did not invalidate cache: %v", batchSizes)
	}

	failing := fixture.batchBinding(t, nil, func(context.Context, []ComputedBatchParent, []ComputedArgument) (map[string]ComputedBatchResult, error) {
		return map[string]ComputedBatchResult{}, nil
	})
	failureExecution, err := newComputedExecution(fixture.compilation, []ComputedBinding{greeting, failing}, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer failureExecution.close()
	if _, err := compiler.EncodeReadWithComputed(context.Background(), compiled.Reads[0], rows[:1], failureExecution.resolve); err == nil || !strings.Contains(err.Error(), "returned 0 keys for 1") {
		t.Fatalf("incomplete batch error=%v", err)
	}
}

func TestGraphQLComputedBatchesNeverCrossPrincipalsOperationsOrCancellation(t *testing.T) {
	fixture := computedFixture(t)
	document := computedQuery(t, `{ `+fixture.contract.Roots.FindMany+` { batchGreeting(prefix: "isolated") } }`)
	batchCalls := 0
	batch := fixture.batchBinding(t, nil, func(_ context.Context, ctxParents []ComputedBatchParent, arguments []ComputedArgument) (map[string]ComputedBatchResult, error) {
		batchCalls++
		result := make(map[string]ComputedBatchResult, len(ctxParents))
		for _, parent := range ctxParents {
			result[parent.CacheKey()] = ComputedBatchResult{Value: arguments[0].Value.(string)}
		}
		return result, nil
	})
	caller := &generatedTestCaller{rows: []golem.RuntimeModelRow{fixture.row(t, fixture.uuid(9), false)}}
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: fixture.bundle, ComputedBindings: []ComputedBinding{fixture.greetingBinding(t), batch},
		CustomBindings:      fixture.stubCustomBindings(t),
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return caller, nil },
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []int{1, 2} {
		response := executor.Execute(context.Background(), principal, Operation{Document: document, Definition: document.Operations[0]})
		if len(response.Errors) != 0 {
			t.Fatalf("principal %d response=%#v", principal, response)
		}
	}
	if batchCalls != 2 {
		t.Fatalf("batch crossed principals/operations: calls=%d", batchCalls)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	response := executor.Execute(cancelled, 3, Operation{Document: document, Definition: document.Operations[0]})
	if len(response.Errors) == 0 || batchCalls != 2 {
		t.Fatalf("cancelled response=%#v batchCalls=%d", response, batchCalls)
	}
}

func TestGeneratedExecutorRequiresExactComputedBindings(t *testing.T) {
	fixture := computedFixture(t)
	base := GeneratedExecutorConfig[int]{
		Bundle:              fixture.bundle,
		CustomBindings:      fixture.stubCustomBindings(t),
		BeginCaller:         func(context.Context, int) (CallerExecution, error) { return &generatedTestCaller{}, nil },
		ReportInternalError: func(context.Context, error) {},
	}
	if _, err := NewGeneratedExecutor(base); err == nil || !strings.Contains(err.Error(), "has no generated binding") {
		t.Fatalf("missing binding error=%v", err)
	}
	wrongBatch, err := BindComputed(string(fixture.batch.ExtensionID), func(context.Context, ComputedRequest) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	base.ComputedBindings = []ComputedBinding{fixture.greetingBinding(t), wrongBatch}
	if _, err := NewGeneratedExecutor(base); err == nil || !strings.Contains(err.Error(), "mode does not match") {
		t.Fatalf("mode mismatch error=%v", err)
	}
	extra, err := BindComputed("ffffffffffffffffffffffffffffffff", func(context.Context, ComputedRequest) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	base.ComputedBindings = []ComputedBinding{fixture.greetingBinding(t), fixture.batchBinding(t, nil, nil), extra}
	if _, err := NewGeneratedExecutor(base); err == nil || !strings.Contains(err.Error(), "absent from the contract") {
		t.Fatalf("extra binding error=%v", err)
	}
}

func (fixture computedTestFixture) stubCustomBindings(t *testing.T) []CustomBinding {
	t.Helper()
	search := customContract(t, fixture.compilation, compilerir.CustomOperationQuery, "searchUsers")
	importUsers := customContract(t, fixture.compilation, compilerir.CustomOperationMutation, "importUsers")
	query, err := BindCustomQuery[CustomCallerCapability, struct{}, []golem.RuntimeModelRow](customSpec(search), func([]CustomArgument) (struct{}, error) { return struct{}{}, nil }, func(context.Context, CustomCallerCapability, struct{}) ([]golem.RuntimeModelRow, error) {
		return nil, nil
	}, func([]golem.RuntimeModelRow) (any, error) { return []any{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := BindCustomMutation[CustomCallerCapability, struct{}, golem.RuntimeModelRow](customSpec(importUsers), func([]CustomArgument) (struct{}, error) { return struct{}{}, nil }, func(context.Context, CustomCallerCapability, struct{}) (golem.RuntimeModelRow, error) {
		return golem.RuntimeModelRow{}, nil
	}, func(value golem.RuntimeModelRow) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	return []CustomBinding{query, mutation}
}

type computedTestFixture struct {
	compilation compilerir.CompilationIR
	bundle      golem.SchemaBundle
	contract    compilerir.ModelContractIR
	greeting    compilerir.ComputedFieldContractIR
	batch       compilerir.ComputedFieldContractIR
	model       golem.ModelID
	id, name    golem.FieldID
}

func computedFixture(t *testing.T) computedTestFixture {
	t.Helper()
	result := compile.Compile(context.Background(), compile.Config{Dir: "../internal/compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	compilation := *result.Compilation
	contract := generatedTestContract(t, compilation.Contract, "User")
	model := generatedTestModel(t, compilation.Model, contract.ModelID)
	fixture := computedTestFixture{compilation: compilation, bundle: bundleForCompilation(t, compilation), contract: contract, model: generatedTestModelID(t, model.ID)}
	for _, field := range contract.Computed {
		switch field.Name {
		case "greeting":
			fixture.greeting = field
		case "batchGreeting":
			fixture.batch = field
		}
	}
	for _, field := range model.Fields {
		switch field.GoName {
		case "ID":
			fixture.id = generatedTestFieldID(t, field.ID)
		case "Name":
			fixture.name = generatedTestFieldID(t, field.ID)
		}
	}
	if fixture.greeting.ExtensionID == "" || fixture.batch.ExtensionID == "" || fixture.id == (golem.FieldID{}) || fixture.name == (golem.FieldID{}) {
		t.Fatalf("incomplete fixture=%#v", fixture)
	}
	return fixture
}

func (fixture computedTestFixture) greetingBinding(t *testing.T) ComputedBinding {
	t.Helper()
	binding, err := BindComputed(string(fixture.greeting.ExtensionID), func(context.Context, ComputedRequest) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func (fixture computedTestFixture) batchBinding(t *testing.T, load func([]ComputedBatchParent, []ComputedArgument) map[string]ComputedBatchResult, custom ComputedBatchFunc) ComputedBinding {
	t.Helper()
	if custom == nil {
		custom = func(_ context.Context, parents []ComputedBatchParent, arguments []ComputedArgument) (map[string]ComputedBatchResult, error) {
			if load != nil {
				return load(parents, arguments), nil
			}
			result := make(map[string]ComputedBatchResult, len(parents))
			for _, parent := range parents {
				result[parent.CacheKey()] = ComputedBatchResult{Value: parent.CacheKey()}
			}
			return result, nil
		}
	}
	binding, err := BindBatchedComputed(string(fixture.batch.ExtensionID), func(_ context.Context, request ComputedRequest) (string, bool, error) {
		cell := golem.RuntimeTransportField(request.Parent, fixture.id)
		if cell.State() != golem.ReadPresent {
			return "", false, nil
		}
		value, _ := cell.Get()
		return value.(golem.UUID).String(), true, nil
	}, custom)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func (fixture computedTestFixture) row(t *testing.T, id golem.UUID, maskedName bool) golem.RuntimeModelRow {
	t.Helper()
	cells := []golem.RuntimeReadCell{golem.RuntimePresentReadCell(fixture.id, id, nil)}
	if maskedName {
		cells = append(cells, golem.RuntimeNullReadCell(fixture.name))
	} else {
		cells = append(cells, golem.RuntimePresentReadCell(fixture.name, "visible", nil))
	}
	row, err := golem.RuntimeModelReadRow(fixture.model, cells...)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (computedTestFixture) uuid(last byte) golem.UUID {
	var value golem.UUID
	value[len(value)-1] = last
	return value
}

func computedQuery(t *testing.T, source string) *ast.QueryDocument {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "computed.graphql", Input: source})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func bundleForCompilation(t *testing.T, compilation compilerir.CompilationIR) golem.SchemaBundle {
	t.Helper()
	modelBytes, _ := compilerir.CanonicalModel(compilation.Model)
	contractBytes, _ := compilerir.CanonicalContract(compilation.Contract)
	modelFingerprint, _ := compilerir.ModelFingerprint(compilation.Model)
	contractFingerprint, _ := compilerir.ContractFingerprint(compilation.Contract)
	modelDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), generatedTestDigest(t, modelFingerprint), modelBytes)
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), generatedTestDigest(t, contractFingerprint), contractBytes)
	return golem.GeneratedSchemaBundle(golem.SchemaDigest{2}, "computed-test", "computed-test", modelDocument, contractDocument)
}
