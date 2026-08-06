package graphqlcapabilities

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
	"github.com/jmoiron/sqlx"
)

type Actor struct{}
type Principal struct{}
type System[P any] struct{}
type RawSQL struct{ Text string }

type User struct {
	_  struct{} `golem:"model;id=capabilities.User;table=users;graphql=User"`
	ID int64    `db:"id" golem:"id=capabilities.User.ID;pk"`
}

type Args struct {
	Value string `golem:"graphql=value"`
}

type DBArgs struct {
	DB  *sqlx.DB `golem:"graphql=db"`
	Tx  *sqlx.Tx `golem:"graphql=tx"`
	SQL RawSQL   `golem:"graphql=sql"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "graphql_capabilities")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "unsafeSystem", UnsafeSystem)
	golem.Query(graphql, "unsafeTx", UnsafeTx)
	golem.Query(graphql, "unsafeDatabase", UnsafeDatabase)
}

func UnsafeSystem(context.Context, *System[Principal], Args) (string, error) {
	return "", nil
}

func UnsafeTx(context.Context, *CallerTx[Principal], Args) (string, error) {
	return "", nil
}

func UnsafeDatabase(context.Context, *Caller[Principal], DBArgs) (string, error) {
	return "", nil
}
