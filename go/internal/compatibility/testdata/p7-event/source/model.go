package p7event

import golem "github.com/eleven-am/golem/go/golem"

type Actor struct {
	UserID golem.UUID
}

type Post struct {
	_       struct{}   `golem:"model;id=compat.p7event.Post;table=p7_event_posts"`
	ID      golem.UUID `db:"id" golem:"id=compat.p7event.Post.ID;pk"`
	OwnerID golem.UUID `db:"owner_id" golem:"id=compat.p7event.Post.OwnerID"`
	Title   string     `db:"title" golem:"id=compat.p7event.Post.Title;type=varchar(80)"`
}

func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel[Post](golem.Subscriptions[Post]())
}

func (Post) DefinePolicy(rules *golem.Rules[Post], actor Actor) {
	rules.CanRead(Posts.OwnerID.Eq(actor.UserID))
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "p7_event_compatibility")
	golem.Actor[Actor](schema)
	golem.Model[Post](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
