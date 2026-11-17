package scalarsets

import "github.com/eleven-am/golem/go/golem"

type Actor struct {
	AccountID     golem.UUID
	ProfileID     golem.UUID
	GrantRevision int64
	Allowed       []golem.UUID
}

type Item struct {
	_      struct{}   `golem:"model;id=scalarsets.Item;table=items"`
	ID     golem.UUID `db:"id" golem:"pk"`
	Owner  golem.UUID `db:"owner"`
	Label  string     `db:"label"`
	Secret string     `db:"secret"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "scalar_sets")
	golem.Actor[Actor](schema)
	golem.Model[Item](schema)
	golem.Providers(schema, golem.SQLite)
	golem.WithoutGraphQL(schema)
}

func (Item) DefinePolicy(rules *golem.Rules[Item], actor Actor) {
	scope := Items.Owner.In(actor.Allowed...)
	rules.CanRead(scope)
	rules.CanUpdate(scope)
	rules.CannotReadFields(golem.All[Item](), Items.Secret)
	rules.CanReadFields(scope.And(Items.Owner.Eq(actor.AccountID)), Items.Secret)
}
