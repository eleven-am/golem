package dataincarnation

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile/testdata/social"
)

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "data_incarnation_transport")
	golem.Actor[social.Actor](schema)
	golem.Model[social.User](schema)
	golem.Model[social.Post](schema)
	golem.Model[social.Comment](schema)
	golem.Model[social.Friendship](schema)
	golem.Model[social.Tag](schema)
	golem.Model[social.PostTag](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
	golem.WithDataIncarnation(schema)
}
