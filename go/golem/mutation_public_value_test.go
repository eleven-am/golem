package golem

import (
	"reflect"
	"strings"
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type publicValueState string

func TestRuntimeMutationInputsRefuseEnginePolicyValues(t *testing.T) {
	model := ModelID{15: 1}
	field := FieldID{15: 2}
	engine := policyir.BoolValue(true)
	_, err := RuntimeMutationInputFromFields(model, []RuntimeMutationFieldValue{{Field: field, Operation: MutationFieldSet, Value: engine, HasValue: true}})
	if err == nil || !strings.Contains(err.Error(), "engine policy value") {
		t.Fatalf("FromFields accepted a policy value: %v", err)
	}
	_, err = RuntimeMutationInputFromValues(RuntimeMutationCreateInput, model, []RuntimeMutationFieldValue{{Field: field, Operation: MutationFieldCreate, Value: engine, HasValue: true}}, nil)
	if err == nil || !strings.Contains(err.Error(), "engine policy value") {
		t.Fatalf("FromValues accepted a policy value: %v", err)
	}
}

func TestFrozenMutationValuesAreWireStringsAndPlainSlices(t *testing.T) {
	model := ModelID{15: 1}
	enum, list, empty, nilList, nested := FieldID{15: 2}, FieldID{15: 3}, FieldID{15: 4}, FieldID{15: 5}, FieldID{15: 6}
	input, err := RuntimeMutationInputFromValues(RuntimeMutationCreateInput, model, []RuntimeMutationFieldValue{
		{Field: enum, Operation: MutationFieldCreate, Value: publicValueState("alpha"), HasValue: true},
		{Field: list, Operation: MutationFieldCreate, Value: List[publicValueState]{"alpha", "omega"}, HasValue: true},
		{Field: empty, Operation: MutationFieldCreate, Value: List[int64]{}, HasValue: true},
		{Field: nilList, Operation: MutationFieldCreate, Value: List[string](nil), HasValue: true},
		{Field: nested, Operation: MutationFieldCreate, Value: List[UUID]{{15: 1}}, HasValue: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[FieldID]any{
		enum: "alpha", list: []string{"alpha", "omega"}, empty: []int64{}, nilList: []string(nil), nested: []UUID{{15: 1}},
	}
	for _, field := range input.Fields() {
		got, _ := field.Value()
		expected := want[field.FieldID()]
		if reflect.TypeOf(got) != reflect.TypeOf(expected) || !reflect.DeepEqual(got, expected) {
			t.Fatalf("field %x = %T(%#v), want %T(%#v)", field.FieldID(), got, got, expected, expected)
		}
	}
}

func TestFrozenMutationValueIsIndependentOfTheCallersList(t *testing.T) {
	model := ModelID{15: 1}
	field := FieldID{15: 2}
	source := List[publicValueState]{"alpha"}
	input, err := RuntimeMutationInputFromFields(model, []RuntimeMutationFieldValue{{Field: field, Operation: MutationFieldSet, Value: source, HasValue: true}})
	if err != nil {
		t.Fatal(err)
	}
	source[0] = "changed"
	got, _ := input.Fields()[0].Value()
	if !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("frozen list followed the caller: %#v", got)
	}
}
