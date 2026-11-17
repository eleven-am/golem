package graphql

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
)

type ComputedObjectPayload struct{ Values []string }

func TestComputedDTOBoundsAndNormalizationRemainPerParent(t *testing.T) {
	fixture := computedFixture(t)
	result := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "ComputedPayload", Nullable: true}
	fixture.compilation.Contract.GraphQLTypes = []compilerir.GraphQLTypeDefinitionIR{{Kind: result.Kind, Name: result.Name, Go: compilerir.GoNamedTypeIR{PackagePath: reflect.TypeFor[ComputedObjectPayload]().PkgPath(), Name: "ComputedObjectPayload"}, Fields: []compilerir.GraphQLObjectFieldIR{{Name: "values", GoName: "Values", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, MaxItems: 1, Element: &compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"}}}}}}
	for i := range fixture.compilation.Contract.Models {
		for j := range fixture.compilation.Contract.Models[i].Computed {
			field := &fixture.compilation.Contract.Models[i].Computed[j]
			if field.ExtensionID == fixture.greeting.ExtensionID {
				field.Result = result
			}
		}
	}
	invocations := 0
	binding, err := BindComputed(string(fixture.greeting.ExtensionID), func(context.Context, ComputedRequest) (any, error) {
		invocations++
		switch invocations {
		case 1:
			return ComputedObjectPayload{Values: []string{"too", "many"}}, nil
		case 2:
			return ComputedObjectPayload{Values: []string{"allowed"}}, nil
		default:
			return (*ComputedObjectPayload)(nil), nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := newComputedExecution(fixture.compilation, []ComputedBinding{binding, fixture.batchBinding(t, nil, nil)}, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.close()
	compiler, err := graphqloperation.New(fixture.compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	execution.hydrator = &resultHydrator{compiler: compiler, maxRows: defaultLimits.MaxHydratedRows}
	slot := selectset.Slot{Kind: selectset.SlotComputed, ResponseName: "greeting", Computed: &graphqlextension.ComputedSelection{ExtensionID: fixture.greeting.ExtensionID, Result: result}, Children: []selectset.Slot{{Kind: selectset.SlotObjectField, ObjectField: "values", ResponseName: "values"}}}
	parents := []golem.RuntimeModelRow{fixture.row(t, fixture.uuid(1), false), fixture.row(t, fixture.uuid(2), false), fixture.row(t, fixture.uuid(3), false)}
	values, err := execution.resolve(context.Background(), "", parents, slot)
	var failures interface{ ComputedFieldFailures() map[int]error }
	if !errors.As(err, &failures) {
		t.Fatalf("expected one parent bound failure: %v", err)
	}
	byIndex := failures.ComputedFieldFailures()
	var limit *golem.Error
	if len(byIndex) != 1 || !errors.As(byIndex[0], &limit) || limit.Code != golem.CodeLimitExceeded || limit.Limit != golem.LimitListItems || limit.LimitMax != 1 {
		t.Fatalf("per-parent bound failures=%v", byIndex)
	}
	if !reflect.DeepEqual(values[1], map[string]any{"values": []any{"allowed"}}) || values[2] != nil {
		t.Fatalf("normalized siblings=%#v", values)
	}
}

func TestComputedDTOHydratesSharedIdentitiesAcrossParents(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../internal/compiler/compile/testdata/objectrows", Pattern: "."})
	if compiled.Compilation == nil || len(compiled.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	contract := generatedTestContract(t, compilation.Contract, "Movie")
	model := generatedTestModel(t, compilation.Model, contract.ModelID)
	modelID := generatedTestModelID(t, model.ID)
	var idField, titleField golem.FieldID
	for _, field := range model.Fields {
		if field.GoName == "ID" {
			idField = generatedTestFieldID(t, field.ID)
		}
		if field.GoName == "Title" {
			titleField = generatedTestFieldID(t, field.ID)
		}
	}
	row := func(key byte, title string) golem.RuntimeModelRow {
		cell := golem.RuntimePresentReadCell(idField, golem.UUID{key}, nil)
		value, err := golem.RuntimeModelReadRow(modelID, cell, golem.RuntimePresentReadCell(titleField, title, nil))
		if err != nil {
			t.Fatal(err)
		}
		identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{idField}, []golem.RuntimeReadCell{cell})
		if err != nil {
			t.Fatal(err)
		}
		return golem.RuntimeModelRowWithIdentity(value, identity)
	}
	compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	document := computedQuery(t, `{ movies(take: 10) { watching { playhead media { title } } } }`)
	operation, err := compiler.Compile(document, document.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	slot := operation.Reads[0].Slots[0]
	caller := &storedRowsCaller{stored: []golem.RuntimeModelRow{row(1, "fresh")}}
	execution := &computedExecution{hydrator: &resultHydrator{compiler: compiler, caller: caller, maxRows: defaultLimits.MaxHydratedRows}}
	// Start at the normalized-value boundary; the generated HTTP acceptance
	// separately supplies real typed Row values through generated bindings.
	values := []any{
		[]any{map[string]any{"playhead": int64(7), "media": row(1, "supplied"), "episode": nil}},
		[]any{map[string]any{"playhead": int64(9), "media": row(1, "supplied"), "episode": nil}},
	}
	values, err = execution.hydrateValues(context.Background(), slot, values, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(caller.takes, []int{1}) {
		t.Fatalf("computed DTO hydration read separately for each parent: %v", caller.takes)
	}
	encoded, failures, err := compiler.EncodeReadWithComputedPartial(context.Background(), operation.Reads[0], []golem.RuntimeModelRow{row(1, "parent one"), row(2, "parent two")}, func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, selectset.Slot) ([]any, error) {
		return values, nil
	})
	if err != nil || len(failures) != 0 {
		t.Fatalf("encoded=%#v failures=%#v error=%v", encoded, failures, err)
	}
	for i, playhead := range []string{"7", "9"} {
		item := encoded.([]any)[i].(map[string]any)["watching"].([]any)[0].(map[string]any)
		if item["playhead"] != playhead || item["media"].(map[string]any)["title"] != "fresh" {
			t.Fatalf("computed DTO restored wrong value: %#v", item)
		}
	}
}
