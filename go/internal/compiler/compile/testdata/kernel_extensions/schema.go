package kernelextensions

import (
	"context"
	"encoding/hex"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ UserID golem.UUID }
type Principal struct{ UserID golem.UUID }

type User struct {
	_ struct{} `golem:"model;id=kernelextensions.User;table=users"`

	ID   golem.UUID `db:"id" golem:"id=kernelextensions.User.ID;pk;default=uuid"`
	Name string     `db:"name" golem:"type=varchar(80)"`
}

type GreetingArgs struct {
	Prefix string `golem:"graphql=prefix"`
}

type RenameArgs struct {
	Name string `golem:"graphql=name"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "kernel_extensions")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}

func (User) DefineGraphQL(graphql *golem.GraphQLModel[User]) {
	golem.ComputedField(graphql, "greeting", golem.GraphQLString().NonNull(), User{}.Greeting, golem.Requires(Users.Name))
	golem.BatchedComputedField(graphql, "batchGreeting", golem.GraphQLString(), Users.ID, LoadGreetings, 64, golem.Requires(Users.Name))
	golem.BatchedComputedFieldWithCacheKey(graphql, "cachedGreeting", golem.GraphQLString(), Users.ID, LoadGreetings, GreetingCacheKey, 64, golem.Requires(Users.Name))
}

func (User) Greeting(context.Context, golem.Row[User], GreetingArgs) (string, error) {
	return "", nil
}

func LoadGreetings(context.Context, []golem.UUID, GreetingArgs) (map[golem.UUID]string, error) {
	return nil, nil
}

func GreetingCacheKey(value golem.UUID) (string, error) {
	return hex.EncodeToString(value[:]), nil
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "countUsers", CountUsers)
	golem.Mutation(graphql, "renameUsers", RenameUsers)
}

func CountUsers(context.Context, *Caller[Principal], GreetingArgs) (int32, error) {
	return 0, nil
}

func RenameUsers(context.Context, *Caller[Principal], RenameArgs) (int32, error) {
	return 0, nil
}
