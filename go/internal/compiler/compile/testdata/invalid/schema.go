package invalid

import golem "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "invalid")
	golem.Actor[Actor](schema)
	golem.Model[Broken](schema)
}

type Actor struct{ ID string }

type Broken struct {
	_    struct{} `golem:"model;table=broken"`
	Name string   `db:"name"`
}
