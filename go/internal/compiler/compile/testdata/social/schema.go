package social

import golem "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "social_complete")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Post](schema)
	golem.Model[Comment](schema)
	golem.Model[Friendship](schema)
	golem.Model[Tag](schema)
	golem.Model[PostTag](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

type Actor struct{ UserID golem.UUID }
