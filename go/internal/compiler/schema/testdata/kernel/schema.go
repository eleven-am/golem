package kernel

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type User struct {
	_  struct{} `golem:"model;id=kernel.User;table=users"`
	ID int64    `db:"id" golem:"id=kernel.User.ID;pk"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "kernel")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}
