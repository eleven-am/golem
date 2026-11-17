package invalidations

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

type Actor struct{ Name string }
type Principal struct{}
type User struct {
	_    struct{}   `golem:"model;table=users"`
	ID   golem.UUID `db:"id" golem:"pk;default=uuid"`
	Name string     `db:"name"`
}

func DefineSchema(schema *golem.Schema) {
	golem.WithDataIncarnation(schema)
	golem.SchemaName(schema, "invalidation_contract")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}

type ScopeArgs struct{ Name string }

func (User) GolemModel() golem.ModelSpec[User] {
	return golem.DefineModel(golem.Subscriptions(
		golem.ScopeTemplate("owner", func(args ScopeArgs) golem.Predicate[User] { return Users.Name.Eq(args.Name) }),
		golem.FactTemplate("named", "owner", func(args ScopeArgs) golem.Predicate[User] { return Users.Name.Eq(args.Name) }),
	))
}
func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	rules.BindFactScope(UserScopes.Owner(ScopeArgs{Name: actor.Name}))
	named := rules.BindFact(UserFacts.Named(ScopeArgs{Name: actor.Name}))
	rules.CanRead(named)
}

type Args struct{ Take *int32 }
type PageArgs struct{ Seed int32 }

func BindRows(context.Context, Actor, Args) (golem.Predicate[User], error) {
	return golem.All[User](), nil
}
func BindPage(context.Context, Actor, PageArgs) (golem.Predicate[User], error) {
	return golem.All[User](), nil
}
func BindFacts(_ context.Context, actor Actor, _ Args) (golem.BoundFactScope[User], error) {
	return UserScopes.Owner(ScopeArgs{Name: actor.Name}), nil
}

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Query(schema, "scalarFeed", ScalarFeed, golem.InvalidateRows[User]("progress", BindRows, Users.Name))
	golem.Query(schema, "factFeed", ScalarFeed, golem.InvalidateFacts[User]("availability", "owner", BindFacts, "named"))
	golem.PageQuery(schema, "pageFeed", PageFeed, golem.PageLimits(25, 100, 200), golem.PageRevision("v1"), golem.InvalidateRows[User]("progress", BindPage, Users.Name))
}
func ScalarFeed(context.Context, *Caller[Principal], Args) (string, error) { return "", nil }
func PageFeed(context.Context, *Caller[Principal], golem.PageRequest[PageArgs]) (golem.PageBatch[string], error) {
	return golem.PageItems([]string{}, 0, nil), nil
}
