package basic

import (
	"context"
	"strconv"

	g "github.com/eleven-am/golem/go/golem"
)

type User struct {
	ID    int64
	Age   int64
	Name  string
	Score int64
	Small int16
}

const adultAge int64 = 18

func (User) GolemModel() g.ModelSpec[User] {
	return g.DefineModel(
		g.GraphQL[User](
			g.GraphQLOperations(g.GraphQLFindOne, g.GraphQLFindMany, g.GraphQLCreate),
			g.GraphQLPlural("accounts"),
			g.GraphQLRoots(g.GraphQLRootNames{FindOne: "account", FindMany: "accounts"}),
			g.GraphQLPageSizes(25, 250),
		),
		g.PrimaryKey("pk_users", Users.ID),
		g.Unique("uq_users_name", Users.Name),
		g.Index[User]("idx_users_name_age").Keys(
			g.IndexColumn(Users.Name).Desc(),
			g.IndexExpr(g.Lower(Users.Name)),
			g.IndexExpr(g.Cast(Users.Small, g.Int16ToInt64)),
		).Where(Users.Age.Expr().GTE(adultAge)),
		g.Check("ck_users_age", Users.Age.Expr().GTE(0)),
		g.ForProvider(g.PostgreSQL,
			g.Generated(Users.Score, Users.Age.Expr().Add(g.SchemaValueOf[User](int64(1))), g.Stored),
		),
	)
}

type GreetingArgs struct {
	Prefix string `golem:"graphql=prefix"`
}

func (User) DefineGraphQL(graphql *g.GraphQLModel[User]) {
	g.ComputedField(graphql, "greeting", g.GraphQLString().NonNull(), User{}.Greeting, g.Requires(Users.Name))
	g.BatchedComputedField(graphql, "batchGreeting", g.GraphQLString(), Users.ID, loadGreetings, 32, g.Requires(Users.Name))
	g.BatchedComputedFieldWithCacheKey(graphql, "canonicalGreeting", g.GraphQLString(), Users.ID, loadGreetings, greetingCacheKey, 16, g.Requires(Users.Name))
}

func (User) Greeting(context.Context, g.Row[User], GreetingArgs) (string, error) {
	return "", nil
}

func loadGreetings(context.Context, []int64, GreetingArgs) (map[int64]string, error) {
	return nil, nil
}

func greetingCacheKey(value int64) (string, error) {
	return strconv.FormatInt(value, 10), nil
}

// Audit deliberately has no GolemModel. Advanced declarations are optional.
type Audit struct {
	ID int64
}
