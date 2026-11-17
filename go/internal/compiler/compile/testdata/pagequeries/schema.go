package pagequeries

import (
	"context"
	"github.com/eleven-am/golem/go/golem"
)

type Actor struct{}
type Principal struct{}
type User struct {
	_    struct{}   `golem:"model;id=page.User;table=users"`
	ID   golem.UUID `db:"id" golem:"id=page.User.ID;pk;default=uuid"`
	Name string     `db:"name"`
}

func DefineSchema(schema *golem.Schema) {
	golem.WithDataIncarnation(schema)
	golem.SchemaName(schema, "page_queries")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}
func (User) DefinePolicy(rules *golem.Rules[User], _ Actor) { rules.CanRead(golem.All[User]()) }

type HomeSegment struct {
	Kind  string
	Index int32
}
type HomeArgs struct{ Seed int32 }

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Output[HomeSegment](schema, "HomeSegment")
	golem.PageQuery(schema, "homeFeed", HomeFeed, golem.PageLimits(25, 100, 200), golem.PageRevision("home-v1"))
}
func HomeFeed(context.Context, *Caller[Principal], golem.PageRequest[HomeArgs]) (golem.PageBatch[HomeSegment], error) {
	var batch golem.PageBatch[HomeSegment]
	return batch, nil
}
