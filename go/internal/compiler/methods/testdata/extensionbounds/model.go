package extensionbounds

import (
	"context"

	g "github.com/eleven-am/golem/go/golem"
)

type Principal struct{ ID string }
type Caller[P any] struct{}

type User struct {
	ID   int64
	Name string
}

type NoArgs struct{}

var runtimeLimit uint32 = 4

var indirectOption g.CustomOption = g.MaxItems(3)

func (User) DefineGraphQL(graphql *g.GraphQLModel[User]) {
	g.ComputedField(graphql, "okList", g.GraphQLList(g.GraphQLString().NonNull(), 10).NonNull(), User{}.Words, g.Requires(Users.Name))
	g.ComputedField(graphql, "okNestedList", g.GraphQLList(g.GraphQLList(g.GraphQLString(), 3), 7), User{}.Grid, g.Requires(Users.Name))
	g.ComputedField(graphql, "zeroList", g.GraphQLList(g.GraphQLString(), 0), User{}.Words, g.Requires(Users.Name))
	g.ComputedField(graphql, "variableList", g.GraphQLList(g.GraphQLString(), runtimeLimit), User{}.Words, g.Requires(Users.Name))
}

func (User) Words(context.Context, g.Row[User], NoArgs) ([]string, error) { return nil, nil }

func (User) Grid(context.Context, g.Row[User], NoArgs) ([][]string, error) { return nil, nil }

func DefineGraphQL(graphql *g.GraphQLSchema) {
	g.Query(graphql, "okSingle", Rows, g.MaxItems(1))
	g.Query(graphql, "okNested", Grid, g.MaxItems(10, 5))
	g.Query(graphql, "missing", Rows)
	g.Query(graphql, "wrongArity", Grid, g.MaxItems(5))
	g.Query(graphql, "zeroMax", Rows, g.MaxItems(0))
	g.Query(graphql, "zeroInner", Grid, g.MaxItems(4, 0))
	g.Query(graphql, "scalarMax", Word, g.MaxItems(3))
	g.Query(graphql, "bytesMax", Blob, g.MaxItems(3))
	g.Query(graphql, "twice", Rows, g.MaxItems(1), g.MaxItems(2))
	g.Query(graphql, "notAnOption", Rows, indirectOption)
	g.Query(graphql, "variableMax", Rows, g.MaxItems(runtimeLimit))
	g.Mutation(graphql, "mutatingMissing", Rows)
	g.Mutation(graphql, "mutatingOk", Rows, g.MaxItems(2))
}

func Rows(context.Context, *Caller[Principal], NoArgs) ([]g.Row[User], error) { return nil, nil }

func Grid(context.Context, *Caller[Principal], NoArgs) ([][]g.Row[User], error) { return nil, nil }

func Word(context.Context, *Caller[Principal], NoArgs) (string, error) { return "", nil }

func Blob(context.Context, *Caller[Principal], NoArgs) ([]byte, error) { return nil, nil }
