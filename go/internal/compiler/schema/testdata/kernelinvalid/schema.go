package kernelinvalid

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type User struct {
	_  struct{} `golem:"model;id=kernelinvalid.User;table=users"`
	ID int64    `db:"id" golem:"id=kernelinvalid.User.ID;pk"`
}

var other *golem.Schema

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "kernel_invalid")
	golem.WithoutGraphQL(schema)
	golem.WithoutGraphQL(schema)
	golem.WithoutGraphQL(other)
	golem.WithoutGraphQL(schema, schema)
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}
