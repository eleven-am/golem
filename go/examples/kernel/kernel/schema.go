package kernel

import "github.com/eleven-am/golem/go/golem"

type Principal struct {
	AccountID golem.UUID
}

type Actor struct {
	AccountID golem.UUID
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "golem_kernel_example")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[Account](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
