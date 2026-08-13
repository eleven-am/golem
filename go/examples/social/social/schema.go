package social

import "github.com/eleven-am/golem/go/golem"

// Principal is the authentication result passed to the generated application.
// Actor is the immutable authorization view resolved for one caller execution.
type Principal struct {
	TokenHash   [32]byte
	Development bool
	DevUserID   golem.UUID
}

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "golem_social_example")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Session](schema)
	golem.Model[Post](schema)
	golem.Model[Comment](schema)
	golem.Model[Tag](schema)
	golem.Model[PostTag](schema)
	golem.Model[VersionedNote](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
