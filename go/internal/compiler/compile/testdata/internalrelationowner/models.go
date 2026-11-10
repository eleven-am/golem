package internalrelationowner

import "github.com/eleven-am/golem/go/golem"

type Actor struct{}

type Owner struct {
	_ struct{} `golem:"model;table=owners;graphql=Owner"`

	ID golem.UUID `db:"id" golem:"pk;default=uuid"`
}

type Task struct {
	_ struct{} `golem:"model;table=tasks;graphql=Task"`

	ID      golem.UUID `db:"id" golem:"pk;default=uuid"`
	OwnerID golem.UUID `db:"owner_id" golem:"internal"`
	Owner   *Owner     `db:"-" golem:"relation=belongs_to;fields=owner_id;references=id;internal"`
}

func (Owner) GolemModel() golem.ModelSpec[Owner] {
	return golem.DefineModel(golem.GraphQL[Owner](golem.GraphQLOperations(golem.GraphQLFindOne)))
}

func (Task) GolemModel() golem.ModelSpec[Task] {
	return golem.DefineModel(golem.GraphQL[Task](golem.GraphQLOperations(golem.GraphQLCreate)))
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "internalrelationowner")
	golem.Actor[Actor](schema)
	golem.Model[Owner](schema)
	golem.Model[Task](schema)
	golem.Providers(schema, golem.SQLite)
}
