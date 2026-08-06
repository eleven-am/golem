package graphqlextensions

import (
	"context"
	"encoding/hex"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ UserID golem.UUID }
type Principal struct{ UserID golem.UUID }

type UserStatus string

const (
	UserStatusActive   UserStatus = "ACTIVE"
	UserStatusDisabled UserStatus = "DISABLED"
)

func (UserStatus) GolemEnum() golem.EnumSpec[UserStatus] {
	return golem.DefineEnum(
		golem.EnumValue(UserStatusActive),
		golem.EnumValue(UserStatusDisabled),
	)
}

type User struct {
	_ struct{} `golem:"model;id=graphql.User;table=users;graphql=User"`

	ID     golem.UUID `db:"id" golem:"id=graphql.User.ID;pk;default=uuid"`
	Name   string     `db:"name" golem:"type=varchar(80)"`
	Status UserStatus `db:"status" golem:"default=ACTIVE"`
}

type GreetingArgs struct {
	Prefix string `golem:"graphql=prefix"`
}

type SearchArgs struct {
	Where golem.Predicate[User] `golem:"graphql=where"`
}

type ImportArgs struct {
	Create golem.JSON[any]     `golem:"graphql=metadata"`
	Data   UserCreateInput     `golem:"graphql=data"`
	Patch  UserUpdateManyInput `golem:"graphql=patch"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "graphql_extensions")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (User) DefineGraphQL(graphql *golem.GraphQLModel[User]) {
	golem.ComputedField(graphql, "greeting", golem.GraphQLString().NonNull(), User{}.Greeting, golem.Requires(Users.Name))
	golem.BatchedComputedFieldWithCacheKey(graphql, "batchGreeting", golem.GraphQLString(), Users.ID, LoadGreetings, GreetingCacheKey, 64, golem.Requires(Users.Name))
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
	golem.Query(graphql, "searchUsers", SearchUsers)
	golem.Mutation(graphql, "importUsers", ImportUsers)
}

func SearchUsers(context.Context, *Caller[Principal], SearchArgs) ([]golem.Row[User], error) {
	return nil, nil
}

func ImportUsers(context.Context, *Caller[Principal], ImportArgs) (golem.Row[User], error) {
	return golem.Row[User]{}, nil
}
