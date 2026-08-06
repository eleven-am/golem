package graphql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

type queryPositionExecutionSpy struct {
	calls   int
	request golem.FrozenReadRequest
}

func (spy *queryPositionExecutionSpy) ExecuteFrozenRead(_ context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	spy.calls++
	spy.request = request
	return nil, golem.RuntimeReadError(golem.CodeForbidden, "position-spy", request.ModelID(), golem.FieldID{}, "position spy stopped before SQL", nil)
}

func TestGraphQLQueryPositionSpyVisitsWhereOrderCursorDistinctRelationAndCount(t *testing.T) {
	compilation, bundle := generatedTestCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	post := generatedTestContract(t, compilation.Contract, "Post")
	postSelector := generatedTestSelectorForField(t, post, "id")
	spy := &queryPositionExecutionSpy{}
	begins := 0
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: bundle,
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			begins++
			return spy, nil
		},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 7, true },
		ReportInternalError:  func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 7, Request{Query: `query PositionSpy {
  ` + post.Roots.FindMany + `(
    where: { title: { contains: "go" } }
    orderBy: [{ createdAt: desc }]
		cursor: { ` + postSelector + `: "00000000-0000-0000-0000-000000000011" }
    distinct: [title]
    take: 2
  ) {
    id
    comments(where: { body: { contains: "visible" } }, take: 1) { id }
    _count { comments(where: { body: { not: "spam" } }) }
  }
}`})
	if begins != 1 || spy.calls != 1 || len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("response=%#v begins=%d calls=%d", response, begins, spy.calls)
	}
	request := spy.request
	where, hasWhere := request.Where()
	if !hasWhere || where.View().RootModelID() != request.ModelID() {
		t.Fatalf("root where position = %#v/%v", where, hasWhere)
	}
	if len(request.OrderBy()) != 1 || request.OrderBy()[0].FieldID() == (golem.FieldID{}) {
		t.Fatalf("order position = %#v", request.OrderBy())
	}
	cursor, hasCursor := request.Cursor()
	if !hasCursor || len(cursor.Selector().Fields()) != 1 || cursor.Predicate().View().RootModelID() != request.ModelID() {
		t.Fatalf("cursor position = %#v/%v", cursor, hasCursor)
	}
	if distinct := request.Distinct(); len(distinct) != 1 || distinct[0] == (golem.FieldID{}) {
		t.Fatalf("distinct position = %#v", distinct)
	}
	relation, count := false, false
	for _, selection := range request.Selection() {
		child, ok := selection.Request()
		if !ok {
			continue
		}
		childWhere, hasChildWhere := child.Where()
		if !hasChildWhere || childWhere.View().RootModelID() != child.ModelID() || selection.FieldID() == (golem.FieldID{}) || selection.RelationID() == (golem.RelationID{}) || selection.TargetModelID() != child.ModelID() {
			t.Fatalf("nested position = selection:%#v child:%#v", selection, child)
		}
		if selection.IsRelation() {
			relation = true
		}
		if selection.IsRelationCount() {
			count = true
		}
	}
	if !relation || !count {
		t.Fatalf("relation/count positions were not both frozen: %#v", request.Selection())
	}
}

func TestGraphQLRefusedQueryInputDoesNotOpenExecutionOrIssueSQL(t *testing.T) {
	compilation, bundle := generatedTestCompilation(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	post := generatedTestContract(t, compilation.Contract, "Post")
	spy := &queryPositionExecutionSpy{}
	begins := 0
	executor, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: bundle,
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			begins++
			return spy, nil
		},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 1, Request{Query: `{ ` + post.Roots.FindMany + `(where: { all: true, title: { equals: "ambiguous" } }) { id } }`})
	if begins != 0 || spy.calls != 0 || len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
		t.Fatalf("refused input crossed execution boundary: response=%#v begins=%d SQL-spy=%d", response, begins, spy.calls)
	}
}
