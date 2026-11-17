package operation

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestCustomDTOSelectionsPreserveAliasesFragmentsDirectivesAndExactInputs(t *testing.T) {
	compilation := objectTypesCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "objects", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, failures := gqlparser.LoadQuery(schema, `query Home($show: Boolean!) {
	  feed: homeFeed(input: {range: {seed: 7}}) {
	    __typename
	    results { ...Segment label: kind index @include(if: $show) }
	    repeated: results { index }
	  }
	}
	fragment Segment on HomeSegment { kind }`)
	if len(failures) != 0 {
		t.Fatal(failures)
	}
	for _, show := range []bool{false, true} {
		compiler, err := New(compilation, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.Compile(query, query.Operations.ForName("Home"), map[string]any{"show": show})
		if err != nil {
			t.Fatal(err)
		}
		root := compiled.Custom[0]
		if got := root.Arguments["input"].(map[string]any)["range"].(map[string]any)["seed"]; got != int32(7) {
			t.Fatalf("input=%#v", root.Arguments)
		}
		value := map[string]any{"results": []any{map[string]any{"kind": "BASIC_PICK", "index": int32(3)}}}
		encoded, failures, err := compiler.EncodeCustomWithComputedPartial(context.Background(), root, value,
			func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, selectset.Slot) ([]any, error) {
				t.Fatal("DTO invoked model resolver")
				return nil, nil
			})
		if err != nil || len(failures) != 0 {
			t.Fatalf("encoded=%#v failures=%#v error=%v", encoded, failures, err)
		}
		item := map[string]any{"kind": "BASIC_PICK", "label": "BASIC_PICK"}
		if show {
			item["index"] = int32(3)
		}
		want := map[string]any{"__typename": "HomeFeedPage", "results": []any{item}, "repeated": []any{map[string]any{"index": int32(3)}}}
		if !reflect.DeepEqual(encoded, want) {
			t.Fatalf("encoded=%#v want=%#v", encoded, want)
		}
	}
	for _, limits := range []Limits{{Depth: 1}, {Fields: 1}, {Aliases: 1}} {
		compiler, err := New(compilation, limits)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := compiler.Compile(query, query.Operations.ForName("Home"), map[string]any{"show": true}); err == nil {
			t.Fatalf("DTO selection ignored limits %#v", limits)
		}
	}
}

func TestDTOListBoundsReachTheServerComplexityBudget(t *testing.T) {
	compiler, err := New(objectTypesCompilation(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := compiler.ListBounds()
	if err != nil {
		t.Fatal(err)
	}
	if bound := bounds["HomeFeedPage.results"]; bound.Maximum != 25 || bound.TakeBounded {
		t.Fatalf("DTO list bounds=%#v", bounds)
	}
	if bound := bounds["Query.homeFeedList"]; bound.Maximum != 2 || bound.TakeBounded {
		t.Fatalf("custom take narrowed the declared result bound: %#v", bound)
	}
}

func objectTypesCompilation(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	result := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/objecttypes", Pattern: "."})
	if result.Compilation == nil || len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	return *result.Compilation
}
