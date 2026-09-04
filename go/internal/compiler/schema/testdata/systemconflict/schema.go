package systemconflict

import declaration "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *declaration.Schema) {
	declaration.SchemaName(schema, "systemconflict")
	declaration.Actor[Actor](schema)
	declaration.Model[Article](schema)
}

type Actor struct {
	ID declaration.UUID
}
