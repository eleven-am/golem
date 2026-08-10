package p5social

import golem "github.com/eleven-am/golem/go/golem"

type Principal struct {
	UserID golem.UUID
	Valid  bool
}

type Actor struct {
	UserID     golem.UUID
	AllowedTag string
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "p5_generated_social")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Post](schema)
	golem.Model[Comment](schema)
	golem.Model[Friendship](schema)
	golem.Model[Tag](schema)
	golem.Model[PostTag](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
