package graphql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type mutationExecutionSpy struct {
	order []golem.RuntimeMutationOperation
	row   golem.RuntimeModelRow
	fail  golem.RuntimeMutationOperation
}

func (spy *mutationExecutionSpy) ExecuteFrozenMutation(_ context.Context, request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {
	spy.order = append(spy.order, request.Operation())
	if request.Operation() == spy.fail {
		return golem.RuntimeMutationResult{}, golem.RuntimeOperationError(golem.CodeForbidden, "mutation", request.ModelID(), golem.FieldID{}, "mutation refused", nil)
	}
	if request.Operation() == golem.RuntimeMutationUpdateMany || request.Operation() == golem.RuntimeMutationDeleteMany {
		return golem.RuntimeMutationCountResult(3)
	}
	return golem.RuntimeMutationRowResult(spy.row), nil
}

func TestMutationExecutionRunsTopLevelFieldsSeriallyAndEncodesP4Results(t *testing.T) {
	compilation, _ := generatedTestCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := generatedTestContract(t, compilation.Contract, "Post")
	user := generatedTestContract(t, compilation.Contract, "User")
	postSelector := generatedTestSelectorForField(t, post, "id")
	userSelector := generatedTestSelectorForField(t, user, "id")
	model := generatedTestModel(t, compilation.Model, post.ModelID)
	id := generatedTestField(t, model, "ID")
	title := generatedTestField(t, model, "Title")
	query, queryErrors := gqlparser.LoadQuery(schema, `mutation Serial($id: UUID!, $author: UUID!) {
  first: `+post.Roots.Create+`(data: { title: "first", body: "body", author: { connect: { `+userSelector+`: $author } } }) { id title }
  second: `+post.Roots.UpdateMany+`(where: { all: true }, data: { title: { set: "second" } }) { amount: count }
  third: `+post.Roots.Delete+`(where: { `+postSelector+`: $id }) { id title }
}`)
	if len(queryErrors) != 0 {
		t.Fatalf("query errors = %v", queryErrors)
	}
	compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations.ForName("Serial"), map[string]any{
		"id": "00000000-0000-0000-0000-000000000011", "author": "00000000-0000-0000-0000-000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := golem.RuntimeModelReadRow(generatedTestModelID(t, model.ID),
		golem.RuntimePresentReadCell(generatedTestFieldID(t, id.ID), golem.UUID{15: 11}, nil),
		golem.RuntimePresentReadCell(generatedTestFieldID(t, title.ID), "visible", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	spy := &mutationExecutionSpy{row: row}
	response := executeMutationRoots(context.Background(), compiler, spy, compiled.Mutations, func(context.Context, error) {})
	want := []golem.RuntimeMutationOperation{golem.RuntimeMutationCreate, golem.RuntimeMutationUpdateMany, golem.RuntimeMutationDelete}
	if len(response.Errors) != 0 || len(spy.order) != len(want) {
		t.Fatalf("response=%#v order=%v", response, spy.order)
	}
	for index := range want {
		if spy.order[index] != want[index] {
			t.Fatalf("execution order[%d] = %d, want %d", index, spy.order[index], want[index])
		}
	}
	data := response.Data.(map[string]any)
	if data["first"].(map[string]any)["title"] != "visible" || data["second"].(map[string]any)["amount"] != int32(3) || data["third"].(map[string]any)["title"] != "visible" {
		t.Fatalf("encoded data = %#v", data)
	}

	failed := &mutationExecutionSpy{row: row, fail: golem.RuntimeMutationUpdateMany}
	refused := executeMutationRoots(context.Background(), compiler, failed, compiled.Mutations, func(context.Context, error) {})
	if refused.Data != nil || len(refused.Errors) != 1 {
		t.Fatalf("non-null mutation failure response = %#v", refused)
	}
	wantPrefix := []golem.RuntimeMutationOperation{golem.RuntimeMutationCreate, golem.RuntimeMutationUpdateMany}
	if len(failed.order) != len(wantPrefix) {
		t.Fatalf("failed mutation executed later roots: %v", failed.order)
	}
	for index := range wantPrefix {
		if failed.order[index] != wantPrefix[index] {
			t.Fatalf("failed order[%d] = %d, want %d", index, failed.order[index], wantPrefix[index])
		}
	}
}

func TestMutationRefusalDoesNotEnterExecutionSpy(t *testing.T) {
	compilation, _ := generatedTestCompilation(t)
	document, _ := graphqlschema.Build(compilation)
	schema, _ := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	post := generatedTestContract(t, compilation.Contract, "Post")
	postSelector := generatedTestSelectorForField(t, post, "id")
	query, validation := gqlparser.LoadQuery(schema, `mutation($id: UUID!) { `+post.Roots.Update+`(where: { `+postSelector+`: $id }, data: { title: { set: "x", increment: 1 } }) { id } }`)
	spy := &mutationExecutionSpy{}
	if len(validation) == 0 {
		compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.Compile(query, query.Operations[0], map[string]any{"id": "00000000-0000-0000-0000-000000000011"})
		if err == nil {
			executeMutationRoots(context.Background(), compiler, spy, compiled.Mutations, func(context.Context, error) {})
		}
	}
	if len(spy.order) != 0 {
		t.Fatalf("refused mutation executed %d roots", len(spy.order))
	}
}

func generatedTestSelectorForField(t *testing.T, contract compilerir.ModelContractIR, graphqlName string) string {
	t.Helper()
	var fieldID compilerir.FieldID
	for _, field := range contract.Fields {
		if field.GraphQLName == graphqlName {
			fieldID = field.FieldID
			break
		}
	}
	if fieldID == "" {
		t.Fatalf("missing field %s.%s", contract.GraphQLName, graphqlName)
	}
	for _, selector := range contract.Selectors {
		if len(selector.Fields) == 1 && selector.Fields[0] == fieldID {
			return selector.Name
		}
	}
	t.Fatalf("missing selector for %s.%s", contract.GraphQLName, graphqlName)
	return ""
}
