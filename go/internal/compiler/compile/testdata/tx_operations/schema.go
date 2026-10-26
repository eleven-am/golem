package txoperations

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ UserID golem.UUID }
type Principal struct{ UserID golem.UUID }

type User struct {
	_ struct{} `golem:"model;id=txoperations.User;table=users;graphql=User"`

	ID   golem.UUID `db:"id" golem:"id=txoperations.User.ID;pk;default=uuid"`
	Name string     `db:"name" golem:"type=varchar(80)"`
}

type RenameArgs struct {
	Name string `golem:"graphql=name"`
}

type RecordArgs struct {
	Names map[string][]byte
	Ready chan struct{}
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "tx_operations")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.UserID))
	golem.Within(rules, RecordUsers).CanCreate(Users.ID.Eq(actor.UserID))
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "renameUsers", RenameUsers)
}

func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "recordUsers", RecordUsers)
	golem.TxOperation(operations, "countUsers", CountUsers)
}

func RenameUsers(context.Context, *Caller[Principal], RenameArgs) (int32, error) {
	return 0, nil
}

func RecordUsers(context.Context, *CallerTx[Principal], RecordArgs) (struct{}, error) {
	return struct{}{}, nil
}

func CountUsers(context.Context, *CallerTx[Principal], struct{}) (map[string]int64, error) {
	return nil, nil
}
