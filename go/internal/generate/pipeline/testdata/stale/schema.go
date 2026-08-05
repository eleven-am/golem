package stale

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type User struct {
	_  struct{} `golem:"model;id=pipeline.StaleUser;table=users"`
	ID int64    `db:"id" golem:"id=pipeline.StaleUser.ID;pk"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "pipeline_stale")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.ID))
}
