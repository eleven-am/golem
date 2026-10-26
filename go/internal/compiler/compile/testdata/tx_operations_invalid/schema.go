package txoperationsinvalid

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ UserID golem.UUID }
type Principal struct{ UserID golem.UUID }

type User struct {
	_ struct{} `golem:"model;id=txoperationsinvalid.User;table=users"`

	ID   golem.UUID `db:"id" golem:"id=txoperationsinvalid.User.ID;pk;default=uuid"`
	Name string     `db:"name" golem:"type=varchar(80)"`
}

type Args struct{}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "tx_operations_invalid")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}

var dynamicName = "dynamic"

func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "callerBound", CallerBound)
	golem.TxOperation(operations, dynamicName, Valid)
	golem.TxOperation(operations, "valid", Valid)
	golem.TxOperation(operations, "valid", Other)
	golem.TxOperation(operations, "missingError", MissingError)
	golem.TxOperation(operations, "literal", func(context.Context, *CallerTx[Principal], Args) (bool, error) { return false, nil })
	golem.TxOperation(operations, "again", Valid)
}

func CallerBound(context.Context, *Caller[Principal], Args) (bool, error) {
	return false, nil
}

func Valid(context.Context, *CallerTx[Principal], Args) (bool, error) {
	return false, nil
}

func Other(context.Context, *CallerTx[Principal], Args) (bool, error) {
	return false, nil
}

func MissingError(context.Context, *CallerTx[Principal], Args) bool {
	return false
}
