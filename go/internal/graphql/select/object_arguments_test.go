package selectset

import (
	"reflect"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestRegisteredNestedInputUsesExactScalarsAndRejectsInvalidFields(t *testing.T) {
	intType := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}
	rangeType := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "PageRange", Nullable: true}
	inputType := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "HomeInput"}
	compilation := compilerir.CompilationIR{Contract: compilerir.ContractIR{GraphQLTypes: []compilerir.GraphQLTypeDefinitionIR{
		{Kind: compilerir.GraphQLTypeInputObject, Name: "HomeInput", Fields: []compilerir.GraphQLObjectFieldIR{{Name: "range", GoName: "Range", Type: rangeType}}},
		{Kind: compilerir.GraphQLTypeInputObject, Name: "PageRange", Fields: []compilerir.GraphQLObjectFieldIR{{Name: "seed", GoName: "Seed", Type: intType}}},
	}}}
	for _, raw := range []map[string]any{{}, {"range": nil}, {"range": map[string]any{"seed": 7}}} {
		got, err := CoerceExtensionValue(compilation, inputType, raw, 4)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{}
		if value, present := raw["range"]; present {
			want["range"] = nil
			if value != nil {
				want["range"] = map[string]any{"seed": int32(7)}
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("coerced=%#v want=%#v", got, want)
		}
	}
	for _, raw := range []any{
		"wrong shape", map[string]any{"unknown": 1}, map[string]any{"range": map[string]any{}},
		map[string]any{"range": map[string]any{"seed": nil}}, map[string]any{"range": map[string]any{"seed": int64(1) << 40}},
	} {
		if _, err := CoerceExtensionValue(compilation, inputType, raw, 4); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
	list := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &inputType}
	if _, err := CoerceExtensionValue(compilation, list, []any{map[string]any{}, map[string]any{}}, 1); err == nil {
		t.Fatal("input list exceeded its runtime bound")
	}
}

func TestRegisteredInputListsPromoteSingletons(t *testing.T) {
	text := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"}
	inner := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &text, Nullable: true}
	outer := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &inner, Nullable: true}
	for _, test := range []struct{ raw, want any }{
		{"x", []any{[]any{"x"}}},
		{[]string{"x"}, []any{[]any{"x"}}},
		{[]any{"x", nil}, []any{[]any{"x"}, nil}},
		{nil, nil},
	} {
		got, wire, err := coerceComputedValue(outer, test.raw, nil, nil, 2)
		if err != nil || !reflect.DeepEqual(got, test.want) || !reflect.DeepEqual(wire, test.want) {
			t.Fatalf("input=%#v value=%#v wire=%#v want=%#v err=%v", test.raw, got, wire, test.want, err)
		}
	}
	if _, _, err := coerceComputedValue(outer, []any{"a", "b", "c"}, nil, nil, 2); err == nil {
		t.Fatal("singleton promotion bypassed the list bound")
	}
}
