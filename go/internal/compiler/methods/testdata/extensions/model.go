package extensions

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

type GreetingArgs struct {
	Prefix string `golem:"graphql=prefix"`
}

type SearchArgs struct {
	Where g.Predicate[User] `golem:"graphql=where"`
	Take  *int32            `golem:"graphql=take"`
}

func (User) DefineGraphQL(graphql *g.GraphQLModel[User]) {
	g.ComputedField(graphql, "greeting", g.GraphQLString().NonNull(), User{}.Greeting, g.Requires(Users.Name))
}

func (User) Greeting(context.Context, g.Row[User], GreetingArgs) (string, error) {
	return "", nil
}

func DefineGraphQL(graphql *g.GraphQLSchema) {
	g.Query(graphql, "searchUsers", SearchUsers)
	g.Mutation(graphql, "renameUser", RenameUser)
}

func SearchUsers(context.Context, *Caller[Principal], SearchArgs) ([]g.Row[User], error) {
	return nil, nil
}

func RenameUser(context.Context, *Caller[Principal], GreetingArgs) (g.Row[User], error) {
	return g.Row[User]{}, nil
}
