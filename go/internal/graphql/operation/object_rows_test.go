package operation

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestDTOEmbeddedRowsCompileWithIsolatedModelProjections(t *testing.T) {
	compilation := objectTypesCompilation(t)
	for i := range compilation.Contract.GraphQLTypes {
		definition := &compilation.Contract.GraphQLTypes[i]
		if definition.Name == "TransportMetadata" {
			definition.Fields = []compilerir.GraphQLObjectFieldIR{{Name: "user", GoName: "User", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeModel, Name: "User", Nullable: true}}}
		}
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, failures := gqlparser.LoadQuery(schema, `{ transportMetadata { a: user { name } b: user { id } } }`)
	if len(failures) != 0 {
		t.Fatal(failures)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Custom[0].Slots) != 2 {
		t.Fatal("embedded aliases were merged")
	}
	// Normalized model values must reach only their selected model encoder.
	hydration, needed := compiler.CustomObjectHydration(result.Custom[0])
	if !needed {
		t.Fatal("model member was not scheduled for hydration")
	}
	prepared, err := hydration.Collect(map[string]any{"user": nil})
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err := hydration.Restore(prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, failures2, err := compiler.EncodeCustomWithComputedPartial(context.Background(), result.Custom[0], restored, func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, selectset.Slot) ([]any, error) {
		t.Fatal("unexpected computed call")
		return nil, nil
	})
	if err != nil || len(failures2) != 0 {
		t.Fatalf("encoded=%#v failures=%#v error=%v", encoded, failures2, err)
	}
	if values := encoded.(map[string]any); values["a"] != nil || values["b"] != nil {
		t.Fatalf("nullable members=%#v", values)
	}
	user := contractNamed(t, compilation.Contract, "User")
	model, err := publicModelID(user.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	var idField, nameField golem.FieldID
	for _, field := range user.Fields {
		id, err := publicFieldID(field.FieldID)
		if err != nil {
			t.Fatal(err)
		}
		if field.GraphQLName == "id" {
			idField = id
		}
		if field.GraphQLName == "name" {
			nameField = id
		}
	}
	id := golem.UUID{1}
	row := func(cells ...golem.RuntimeReadCell) golem.RuntimeModelRow {
		value, err := golem.RuntimeModelReadRow(model, cells...)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{idField}, []golem.RuntimeReadCell{golem.RuntimePresentReadCell(idField, id, nil)})
		if err != nil {
			t.Fatal(err)
		}
		return golem.RuntimeModelRowWithIdentity(value, identity)
	}
	hydration, _ = compiler.CustomObjectHydration(result.Custom[0])
	prepared, err = hydration.Collect(map[string]any{"user": row(golem.RuntimePresentReadCell(nameField, "supplied", nil))})
	if err != nil {
		t.Fatal(err)
	}
	groups := hydration.Groups()
	if len(groups) != 2 || groups[0].Len() != 1 || groups[1].Len() != 1 {
		t.Fatalf("alias projection groups=%#v", groups)
	}
	data := make([]HydratedRows, len(groups))
	for i, group := range groups {
		// Each projection receives only what that caller read selected.
		stored := row(golem.RuntimePresentReadCell(nameField, "reloaded", nil))
		if i == 1 {
			stored = row(golem.RuntimePresentReadCell(idField, id, nil))
		}
		data[i], err = group.Index([]golem.RuntimeModelRow{stored})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := compiler.HydrationRequest(group, 0, group.Len()); err != nil {
			t.Fatal(err)
		}
	}
	restored, _, err = hydration.Restore(prepared, data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err = compiler.EncodeCustomWithComputedPartial(context.Background(), result.Custom[0], restored, func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, selectset.Slot) ([]any, error) {
		t.Fatal("unexpected computed call")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	values := encoded.(map[string]any)
	if values["a"].(map[string]any)["name"] != "reloaded" || len(values["b"].(map[string]any)) != 1 {
		t.Fatalf("projection isolation=%#v", values)
	}
	same := result.Custom[0]
	same.Slots = selectset.StableSlots(same.Slots)
	same.Slots[1].Model = same.Slots[0].Model
	same.Slots[1].Children = same.Slots[0].Children
	deduplicated, _ := compiler.CustomObjectHydration(same)
	for i := 0; i < 2; i++ {
		if _, err := deduplicated.Collect(map[string]any{"user": row()}); err != nil {
			t.Fatal(err)
		}
	}
	if groups := deduplicated.Groups(); len(groups) != 1 || groups[0].Len() != 1 {
		t.Fatalf("equivalent occurrences did not share one identity read: %#v", groups)
	}
	limited, err := New(compilation, Limits{ListItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := limited.CustomObjectHydration(result.Custom[0])
	if _, err := budget.Collect(map[string]any{"user": row()}); err == nil {
		t.Fatal("aliases bypassed total hydration occurrence budget")
	}
}
