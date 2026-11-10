package indexsearchoptout

import "github.com/eleven-am/golem/go/golem"

type Actor struct{}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "indexsearchoptout")
	golem.Actor[Actor](schema)
	golem.EmbeddingSpace(schema, "content", 3)
	golem.Model[Document](schema)
	golem.Providers(schema, golem.SQLite)
}
