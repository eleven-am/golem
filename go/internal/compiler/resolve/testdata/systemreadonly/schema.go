package systemreadonly

import declaration "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *declaration.Schema) {
	declaration.SchemaName(schema, "systemreadonly")
	declaration.Actor[Actor](schema)
	declaration.Model[Article](schema)
}

type Actor struct {
	ID declaration.UUID
}
