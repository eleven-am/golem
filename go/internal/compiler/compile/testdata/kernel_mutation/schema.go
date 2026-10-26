package kernelmutation

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ UserID golem.UUID }
type Principal struct{ UserID golem.UUID }

type User struct {
	_ struct{} `golem:"model;id=kernelmutation.User;table=users"`

	ID   golem.UUID `db:"id" golem:"id=kernelmutation.User.ID;pk;default=uuid"`
	Name string     `db:"name" golem:"type=varchar(80)"`
}

func (User) GolemModel() golem.ModelSpec[User] {
	return golem.DefineModel(
		golem.GraphQL[User](golem.GraphQLHookOwned(Users.Name)),
		golem.Subscriptions[User](),
	)
}

type RenameArgs struct {
	Name string `golem:"graphql=name"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "kernel_mutation")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "renameUsers", RenameUsers)
}

func RenameUsers(context.Context, *Caller[Principal], RenameArgs) (int32, error) {
	return 0, nil
}
