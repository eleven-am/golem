package internalrelation

import golem "github.com/eleven-am/golem/go/golem"

type Principal struct{}

type Actor struct{}

type Parent struct {
	_ struct{} `golem:"model;id=internalrelation.Parent;table=parents;graphql=Parent"`

	ID       golem.UUID `db:"id" golem:"id=internalrelation.Parent.ID;pk"`
	Name     string     `db:"name" golem:"type=varchar(80)"`
	Children []Child    `db:"-" golem:"relation=has_many;fields=id;references=parent_id"`
}

type Child struct {
	_ struct{} `golem:"model;id=internalrelation.Child;table=children;graphql=Child"`

	ID       golem.UUID `db:"id" golem:"id=internalrelation.Child.ID;pk"`
	ParentID golem.UUID `db:"parent_id"`
	Parent   *Parent    `db:"-" golem:"relation=belongs_to;fields=parent_id;references=id;internal"`
}

func (Parent) GolemModel() golem.ModelSpec[Parent] {
	return golem.DefineModel(
		golem.GraphQL[Parent](golem.GraphQLOperations(golem.GraphQLFindOne, golem.GraphQLFindMany, golem.GraphQLCreate)),
	)
}

func (Child) GolemModel() golem.ModelSpec[Child] {
	return golem.DefineModel(
		golem.GraphQL[Child](golem.GraphQLOperations(golem.GraphQLFindOne, golem.GraphQLFindMany, golem.GraphQLCreate, golem.GraphQLUpdate)),
		golem.Subscriptions[Child](),
	)
}

func (Parent) DefinePolicy(rules *golem.Rules[Parent], _ Actor) {
	rules.CanRead(golem.All[Parent]())
	rules.CanCreate(golem.All[Parent]())
}

func (Child) DefinePolicy(rules *golem.Rules[Child], _ Actor) {
	rules.CanRead(golem.All[Child]())
	rules.CanCreate(golem.All[Child]())
	rules.CanUpdate(golem.All[Child]())
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "internal_relation")
	golem.Actor[Actor](schema)
	golem.Model[Parent](schema)
	golem.Model[Child](schema)
	golem.Providers(schema, golem.SQLite)
}
