package schema

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/invalid/actor"
	"github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/invalid/models"
)

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "pipeline_invalid")
	golem.Actor[actor.Actor](schema)
	golem.Model[models.User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
