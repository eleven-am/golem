package social

import golem "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "social")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Post](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
	golem.EmbeddingSpace(schema, "content", 384)
}

type Actor struct {
	ID string
}
