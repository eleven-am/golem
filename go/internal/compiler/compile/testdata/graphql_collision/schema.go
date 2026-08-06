package graphqlcollision

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{}
type Principal struct{}

type User struct {
	_  struct{} `golem:"model;id=collision.User;table=users;graphql=User"`
	ID int64    `db:"id" golem:"id=collision.User.ID;pk"`
}

type Args struct {
	Value string `golem:"graphql=value"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "graphql_collision")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "users", UsersQuery)
	golem.Query(graphql, "aggregateUsers", AggregateUsersQuery)
}

func UsersQuery(context.Context, *Caller[Principal], Args) (string, error) {
	return "", nil
}

func AggregateUsersQuery(context.Context, *Caller[Principal], Args) (string, error) {
	return "", nil
}
