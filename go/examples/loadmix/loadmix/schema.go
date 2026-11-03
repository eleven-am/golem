package loadmix

import "github.com/eleven-am/golem/go/golem"

type Principal struct {
	UserID golem.UUID
}

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "golem_loadmix_example")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Article](schema)
	golem.Model[ArticleAudio](schema)
	golem.Model[ChatThread](schema)
	golem.Model[ChatMessage](schema)
	golem.Model[ReadingSession](schema)
	golem.Providers(schema, golem.PostgreSQL)
}
