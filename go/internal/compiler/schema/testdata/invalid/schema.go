package invalid

import "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "invalid")
	golem.Actor[Actor](schema)
	register(schema)
	golem.Model[Bad](schema)
}

func register(*golem.Schema) {}

type Actor struct{}

type Bad struct {
	_  struct{} `golem:"model;table=bads"`
	ID string   `db:"id" golem:"pk;surprise=yes"`
}
