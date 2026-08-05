package schema

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/multipkg/actor"
	"github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/multipkg/models"
)

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "pipeline_social")
	golem.Actor[actor.Actor](schema)
	golem.Model[models.User](schema)
	golem.Model[models.Post](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
