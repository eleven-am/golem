package aggregates

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ UserID golem.UUID }
type Principal struct{ UserID golem.UUID }

type User struct {
	_ struct{} `golem:"model;id=aggregates.User;table=users;graphql=User"`

	ID    golem.UUID `db:"id" golem:"id=aggregates.User.ID;pk;default=uuid"`
	Name  string     `db:"name" golem:"type=varchar(80)"`
	Quota int64      `db:"quota"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "aggregates")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.UserID))
	golem.Within(rules, CountUsers).CanCount(Users.Name.StartsWith("a"))
	golem.Within(rules, CountUsers).CanAggregate(golem.All[User](), Users.Quota)
}

func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "countUsers", CountUsers)
}

func CountUsers(ctx context.Context, tx *CallerTx[Principal], _ struct{}) (int64, error) {
	return tx.Users.Count(ctx)
}
