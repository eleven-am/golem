package p6metrics

import golem "github.com/eleven-am/golem/go/golem"

type Principal struct{ CategoryPrefix string }
type Actor struct{ CategoryPrefix string }

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "p6_metrics")
	golem.Actor[Actor](schema)
	golem.Model[Category](schema)
	golem.Model[Metric](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
