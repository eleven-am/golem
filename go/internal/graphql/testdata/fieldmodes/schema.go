package fieldmodes

import golem "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "field_modes")
	golem.Actor[Actor](schema)
	golem.Model[Account](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

type Actor struct{ AccountID golem.UUID }
