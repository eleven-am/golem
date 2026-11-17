package invalidationsinvalid

import (
	"context"
	"github.com/eleven-am/golem/go/golem"
)

type Actor struct{}
type OtherActor struct{}
type Principal struct{}
type Caller[P any] struct{}
type User struct {
	ID   int64
	Name string
}
type Args struct{ Take int32 }
type OtherArgs struct{ Take int32 }
type JSONArgs struct{ Filter golem.JSON[any] }

func Resolve(context.Context, *Caller[Principal], Args) (string, error)         { return "", nil }
func ResolveJSON(context.Context, *Caller[Principal], JSONArgs) (string, error) { return "", nil }
func Bind(context.Context, Actor, Args) (golem.Predicate[User], error)          { return golem.All[User](), nil }
func WrongActor(context.Context, OtherActor, Args) (golem.Predicate[User], error) {
	return golem.All[User](), nil
}
func WrongArgs(context.Context, Actor, OtherArgs) (golem.Predicate[User], error) {
	return golem.All[User](), nil
}
func WrongResult(context.Context, Actor, Args) (bool, error) { return false, nil }
func JSONBind(context.Context, Actor, JSONArgs) (golem.Predicate[User], error) {
	return golem.All[User](), nil
}
func DefineGraphQL(g *golem.GraphQLSchema) {
	golem.Mutation(g, "mutation", Resolve, golem.InvalidateRows[User]("bad", Bind, Users.Name))
	golem.Query(g, "actor", Resolve, golem.InvalidateRows[User]("bad", WrongActor, Users.Name))
	golem.Query(g, "args", Resolve, golem.InvalidateRows[User]("bad", WrongArgs, Users.Name))
	golem.Query(g, "result", Resolve, golem.InvalidateRows[User]("bad", WrongResult, Users.Name))
	golem.Query(g, "duplicate", Resolve, golem.InvalidateRows[User]("bad", Bind, Users.Name, Users.Name))
	golem.Query(g, "JSON", ResolveJSON, golem.InvalidateRows[User]("bad", JSONBind, Users.Name))
}
