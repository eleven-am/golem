package graphql

import (
	"reflect"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

type HomeBindingPage struct{ Results []HomeBindingSegment }
type HomeBindingSegment struct {
	Kind  string
	Index int32
}
type HomeBindingInput struct{ Range *HomeBindingRange }
type HomeBindingRange struct{ Seed int32 }

func TestGeneratedBindingsNormalizeRegisteredObjectsAndDecodeNestedInput(t *testing.T) {
	stringType := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String"}
	intType := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Int"}
	segment := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "HomeSegment"}
	page := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "HomeFeedPage"}
	pkg := reflect.TypeFor[HomeBindingPage]().PkgPath()
	compilation := compilerir.CompilationIR{Contract: compilerir.ContractIR{GraphQLTypes: []compilerir.GraphQLTypeDefinitionIR{
		{Kind: page.Kind, Name: page.Name, Go: compilerir.GoNamedTypeIR{PackagePath: pkg, Name: "HomeBindingPage"}, Fields: []compilerir.GraphQLObjectFieldIR{{Name: "results", GoName: "Results", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &segment, MaxItems: 25}}}},
		{Kind: segment.Kind, Name: segment.Name, Go: compilerir.GoNamedTypeIR{PackagePath: pkg, Name: "HomeBindingSegment"}, Fields: []compilerir.GraphQLObjectFieldIR{{Name: "kind", GoName: "Kind", Type: stringType}, {Name: "index", GoName: "Index", Type: intType}}},
	}}}
	value := HomeBindingPage{Results: []HomeBindingSegment{{Kind: "recent", Index: 3}}}
	got, err := normalizeGeneratedResult[struct{}](compilation, page, value, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"results": []any{map[string]any{"kind": "recent", "index": int32(3)}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized=%#v", got)
	}
	if got, err := normalizeGeneratedResult[struct{}](compilation, page, &value, false); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("non-null computed DTO pointer=%#v: %v", got, err)
	}
	if _, err := normalizeGeneratedResult[struct{}](compilation, page, (*HomeBindingPage)(nil), false); err == nil {
		t.Fatal("non-null DTO accepted a nil pointer")
	}
	if _, err := normalizeGeneratedResult[struct{}](compilation, page, HomeBindingSegment{}, false); err == nil {
		t.Fatal("accepted a different Go object type")
	}
	args, err := generatedArguments[struct{ Input HomeBindingInput }]([]ComputedArgument{{Name: "input", Value: map[string]any{"range": map[string]any{"seed": int32(7)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if args.Input.Range == nil || args.Input.Range.Seed != 7 {
		t.Fatalf("arguments=%#v", args)
	}
	if _, err := generatedArguments[struct{ Input HomeBindingInput }]([]ComputedArgument{{Name: "input", Value: map[string]any{"unknown": true}}}); err == nil {
		t.Fatal("accepted an unknown nested Go input field")
	}
}

func TestGeneratedArgumentsUseCompilerNamesForInitialisms(t *testing.T) {
	type Input struct {
		ID       string
		URLValue string
		CustomID string `golem:"graphql=identifier"`
	}
	got, err := generatedArguments[struct{ Input Input }]([]ComputedArgument{{Name: "input", Value: map[string]any{"id": "one", "urlValue": "two", "identifier": "three"}}})
	if err != nil || got.Input.ID != "one" || got.Input.URLValue != "two" || got.Input.CustomID != "three" {
		t.Fatalf("compiler/runtime field names disagree: %#v: %v", got, err)
	}
}
