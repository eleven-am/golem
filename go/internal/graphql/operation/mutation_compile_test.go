package operation

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestGraphQLMutationRootsLowerToExactP4RequestsAndResults(t *testing.T) {
	compilation := social(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	user := contractNamed(t, compilation.Contract, "User")
	postSelector, userSelector := selectorNameForField(t, post, "id"), selectorNameForField(t, user, "id")
	query, queryErrors := gqlparser.LoadQuery(schema, `mutation Mutate($id: UUID!, $author: UUID!, $includeDelete: Boolean!) {
  made: `+post.Roots.Create+`(data: { title: "made", body: "body", author: { connect: { `+userSelector+`: $author } } }) { ...PostResult }
  changed: `+post.Roots.Update+`(where: { `+postSelector+`: $id }, data: { title: { set: "changed" } }) { ...PostResult }
  chosen: `+post.Roots.Upsert+`(where: { `+postSelector+`: $id }, create: { title: "upsert", body: "body", author: { connect: { `+userSelector+`: $author } } }, update: { title: { set: "upserted" } }) { ...PostResult }
  removed: `+post.Roots.Delete+`(where: { `+postSelector+`: $id }) @include(if: $includeDelete) { ...PostResult }
  bulk: `+post.Roots.UpdateMany+`(where: { all: true }, data: { title: { set: "bulk" } }) { total: count }
  ...DeleteTail
}
fragment DeleteTail on Mutation {
  cleared: `+post.Roots.DeleteMany+`(where: { title: { equals: "gone" } }) { count }
}
fragment PostResult on Post { key: id title }
`)
	if len(queryErrors) != 0 {
		t.Fatalf("query errors = %v", queryErrors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations.ForName("Mutate"), map[string]any{
		"id": "00000000-0000-0000-0000-000000000011", "author": "00000000-0000-0000-0000-000000000001", "includeDelete": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"made", "changed", "chosen", "removed", "bulk", "cleared"}
	wantOperation := []golem.RuntimeMutationOperation{
		golem.RuntimeMutationCreate, golem.RuntimeMutationUpdate, golem.RuntimeMutationUpsert,
		golem.RuntimeMutationDelete, golem.RuntimeMutationUpdateMany, golem.RuntimeMutationDeleteMany,
	}
	if len(compiled.Mutations) != len(want) || len(compiled.Reads) != 0 {
		t.Fatalf("compiled roots = reads:%d mutations:%d", len(compiled.Reads), len(compiled.Mutations))
	}
	for index, root := range compiled.Mutations {
		if root.ResponseName != want[index] {
			t.Fatalf("mutation order[%d] = %q, want %q", index, root.ResponseName, want[index])
		}
		if root.Frozen.ModelID() != golemModelID(post.ModelID) {
			t.Fatalf("mutation %s lost stable model identity", root.ResponseName)
		}
		if root.Frozen.Operation() != wantOperation[index] {
			t.Fatalf("mutation %s operation = %d, want %d", root.ResponseName, root.Frozen.Operation(), wantOperation[index])
		}
		if index < 4 && (len(root.Slots) != 2 || len(root.BatchSlots) != 0) {
			t.Fatalf("row mutation %s slots = %#v/%#v", root.ResponseName, root.Slots, root.BatchSlots)
		}
		if index >= 4 && len(root.BatchSlots) != 1 {
			t.Fatalf("batch mutation %s slots = %#v", root.ResponseName, root.BatchSlots)
		}
	}
	postID, err := publicModelID(post.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	idField, err := publicFieldID(contractFieldNamed(t, post, "id").FieldID)
	if err != nil {
		t.Fatal(err)
	}
	titleField, err := publicFieldID(contractFieldNamed(t, post, "title").FieldID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeModelReadRow(postID,
		golem.RuntimePresentReadCell(idField, golem.UUID{15: 0x11}, nil),
		golem.RuntimePresentReadCell(titleField, "encoded", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		encoded, encodeErr := compiler.EncodeMutation(compiled.Mutations[index], golem.RuntimeMutationRowResult(row))
		if encodeErr != nil {
			t.Fatalf("encode row root %s: %v", compiled.Mutations[index].ResponseName, encodeErr)
		}
		object, ok := encoded.(map[string]any)
		if !ok || object["key"] != "00000000-0000-0000-0000-000000000011" || object["title"] != "encoded" {
			t.Fatalf("encoded row root %s = %#v", compiled.Mutations[index].ResponseName, encoded)
		}
	}
	count, err := golem.RuntimeMutationCountResult(7)
	if err != nil {
		t.Fatal(err)
	}
	for index := 4; index < 6; index++ {
		encoded, encodeErr := compiler.EncodeMutation(compiled.Mutations[index], count)
		if encodeErr != nil {
			t.Fatalf("encode batch root %s: %v", compiled.Mutations[index].ResponseName, encodeErr)
		}
		object, ok := encoded.(map[string]any)
		if !ok {
			t.Fatalf("encoded batch root %s = %#v", compiled.Mutations[index].ResponseName, encoded)
		}
		for _, value := range object {
			if value != int32(7) {
				t.Fatalf("encoded batch root %s = %#v", compiled.Mutations[index].ResponseName, encoded)
			}
		}
	}
	if _, err := compiler.EncodeMutation(compiled.Mutations[4], golem.RuntimeMutationResult{}); err == nil {
		t.Fatal("zero-value mutation result was accepted as a successful zero-count batch")
	}
	zero, err := golem.RuntimeMutationCountResult(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compiler.EncodeMutation(compiled.Mutations[4], zero); err != nil {
		t.Fatalf("explicit zero-count batch result was refused: %v", err)
	}
}

func TestGraphQLCreateOmittedExplicitNullAndDefaultRemainDistinct(t *testing.T) {
	compilation := social(t)
	optionalID := compilerir.FieldID("f1000000000000000000000000000001")
	for modelIndex := range compilation.Model.Models {
		if compilation.Model.Models[modelIndex].LogicalName != "Comment" {
			continue
		}
		compilation.Model.Models[modelIndex].Fields = append(compilation.Model.Models[modelIndex].Fields, compilerir.FieldIR{
			ID: optionalID, GoName: "OptionalRef", LogicalName: "optionalRef", DeclarationOrder: 100,
			Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "optional_ref", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, Nullable: true},
		})
		for contractIndex := range compilation.Contract.Models {
			if compilation.Contract.Models[contractIndex].ModelID == compilation.Model.Models[modelIndex].ID {
				compilation.Contract.Models[contractIndex].Fields = append(compilation.Contract.Models[contractIndex].Fields, compilerir.FieldContractIR{FieldID: optionalID, GraphQLName: "optionalRef", Modes: []compilerir.FieldMode{compilerir.ModeVisible}})
			}
		}
	}
	document, _ := graphqlschema.Build(compilation)
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	comment := contractNamed(t, compilation.Contract, "Comment")
	post := contractNamed(t, compilation.Contract, "Post")
	user := contractNamed(t, compilation.Contract, "User")
	postSelector, userSelector := selectorNameForField(t, post, "id"), selectorNameForField(t, user, "id")
	query, queryErrors := gqlparser.LoadQuery(schema, `mutation Presence($id: UUID!, $post: UUID!, $author: UUID!, $optional: UUID = "00000000-0000-0000-0000-000000000022") {
  omitted: `+comment.Roots.Create+`(data: { id: $id, body: "omitted", post: { connect: { `+postSelector+`: $post } }, author: { connect: { `+userSelector+`: $author } } }) { id }
  explicit: `+comment.Roots.Create+`(data: { id: $id, optionalRef: null, body: "explicit", post: { connect: { `+postSelector+`: $post } }, author: { connect: { `+userSelector+`: $author } } }) { id }
  defaulted: `+comment.Roots.Create+`(data: { id: $id, optionalRef: $optional, body: "defaulted", post: { connect: { `+postSelector+`: $post } }, author: { connect: { `+userSelector+`: $author } } }) { id }
}`)
	if len(queryErrors) != 0 {
		t.Fatalf("query errors = %v", queryErrors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations.ForName("Presence"), map[string]any{
		"id": "00000000-0000-0000-0000-000000000021", "post": "00000000-0000-0000-0000-000000000011", "author": "00000000-0000-0000-0000-000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Mutations) != 3 {
		t.Fatalf("mutation count = %d", len(compiled.Mutations))
	}
	optionalFieldID, err := publicFieldID(optionalID)
	if err != nil {
		t.Fatal(err)
	}
	for index, root := range compiled.Mutations {
		input, ok := root.Frozen.Input()
		if !ok {
			t.Fatalf("root %d input is absent", index)
		}
		found := false
		var authored any
		for _, field := range input.Fields() {
			if field.FieldID() == optionalFieldID {
				found = true
				if index == 1 && field.Operation() != golem.MutationFieldNull {
					t.Fatalf("explicit parent operation = %d", field.Operation())
				}
				authored, _ = field.Value()
			}
		}
		if index == 0 && found || index > 0 && !found {
			t.Fatalf("root %s optional scalar presence = %v", root.ResponseName, found)
		}
		if index == 1 && authored != nil {
			t.Fatalf("explicit null acquired value %#v", authored)
		}
		if index == 2 {
			exact, ok := authored.(policyir.Value)
			if !ok {
				t.Fatalf("default value type = %T, want exact policy value", authored)
			}
			uuid, ok := exact.UUID()
			if !ok || uuid != (golem.UUID{15: 0x22}) {
				t.Fatalf("default UUID = %x/%v", uuid, ok)
			}
		}
	}
}

func contractFieldNamed(t *testing.T, contract compilerir.ModelContractIR, name string) compilerir.FieldContractIR {
	t.Helper()
	for _, field := range contract.Fields {
		if field.GraphQLName == name {
			return field
		}
	}
	t.Fatalf("missing field %s.%s", contract.GraphQLName, name)
	return compilerir.FieldContractIR{}
}

func selectorNameForField(t *testing.T, contract compilerir.ModelContractIR, graphqlName string) string {
	t.Helper()
	field := contractFieldNamed(t, contract, graphqlName)
	for _, selector := range contract.Selectors {
		if len(selector.Fields) == 1 && selector.Fields[0] == field.FieldID {
			return selector.Name
		}
	}
	t.Fatalf("missing selector for %s.%s", contract.GraphQLName, graphqlName)
	return ""
}
