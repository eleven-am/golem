package social

import declaration "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *declaration.Schema) {
	declaration.SchemaName(schema, "social")
	declaration.Actor[Actor](schema)
	declaration.Model[User](schema)
	declaration.Model[Audit](schema)
}

type Actor struct {
	ID declaration.UUID
}
