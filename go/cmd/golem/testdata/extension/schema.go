package extension

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type User struct {
	_    struct{} `golem:"model;id=cli.ExtensionUser;table=users"`
	ID   int64    `db:"id" golem:"id=cli.ExtensionUser.ID;pk"`
	Name string   `db:"name"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "cli_extension")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (User) GolemModel() golem.ModelSpec[User] {
	return golem.DefineModel[User](
		golem.ForProvider[User](golem.PostgreSQL, golem.Index[User]("idx_users_name_pg").Keys(golem.IndexColumn(Users.Name))),
	)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.ID))
}
