package operation

import (
	"context"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestComputedDTOUsesObjectSelectionAndEncoding(t *testing.T) {
	compilation := objectTypesCompilation(t)
	contract := &compilation.Contract.Models[0]
	contract.Computed = []compilerir.ComputedFieldContractIR{{ExtensionID: "00000000000000000000000000000001", Name: "feed", Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "HomeFeedPage"}}}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, failures := gqlparser.LoadQuery(schema, fmt.Sprintf(`{ %s { value: feed { results { label: kind index } } } }`, contract.Roots.FindMany))
	if len(failures) != 0 {
		t.Fatal(failures)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	model, err := publicModelID(contract.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeModelReadRow(model)
	if err != nil {
		t.Fatal(err)
	}
	encoded, fieldFailures, err := compiler.EncodeReadWithComputedPartial(context.Background(), compiled.Reads[0], []golem.RuntimeModelRow{row}, func(_ context.Context, _ compilerir.ModelID, _ []golem.RuntimeModelRow, slot selectset.Slot) ([]any, error) {
		if len(slot.Children) != 1 || slot.Children[0].Kind != selectset.SlotObjectField {
			t.Fatal("computed DTO did not retain object slots")
		}
		return []any{map[string]any{"results": []any{map[string]any{"kind": "BASIC_PICK", "index": int32(1)}}}}, nil
	})
	if err != nil || len(fieldFailures) != 0 {
		t.Fatalf("encoding=%#v failures=%#v error=%v", encoded, fieldFailures, err)
	}
	item := encoded.([]any)[0].(map[string]any)["value"].(map[string]any)["results"].([]any)[0].(map[string]any)
	if item["label"] != "BASIC_PICK" || item["index"] != int32(1) {
		t.Fatalf("computed DTO=%#v", item)
	}
}
