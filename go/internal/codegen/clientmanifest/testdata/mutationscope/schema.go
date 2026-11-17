package mutationscope

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

type Actor struct{}

type Team struct {
	_  struct{} `golem:"model;id=mutationscope.Team;table=teams"`
	ID int64    `db:"id" golem:"pk"`
}

type Invite struct {
	_      struct{}          `golem:"model;id=mutationscope.Invite;table=invites"`
	ID     int64             `db:"id" golem:"pk"`
	TeamID golem.Null[int64] `db:"team_id"`
	Team   *Team             `db:"-" golem:"relation=belongs_to;fields=team_id;references=id"`
}

func (Invite) GolemModel() golem.ModelSpec[Invite] {
	return golem.DefineModel(golem.RelationOptions(Invites.Team).OnDelete(golem.SetNull))
}

type Solo struct {
	_  struct{} `golem:"model;id=mutationscope.Solo;table=solos"`
	ID int64    `db:"id" golem:"pk"`
}

type Hooked struct {
	_  struct{} `golem:"model;id=mutationscope.Hooked;table=hooked"`
	ID int64    `db:"id" golem:"pk"`
}

func (Hooked) AfterCreate(context.Context, HookedCreateResult) error { return nil }

type Public struct {
	_  struct{} `golem:"model;id=mutationscope.Public;table=publics"`
	ID int64    `db:"id" golem:"pk"`
}

type PrivateTrail struct {
	_        struct{} `golem:"model;id=mutationscope.PrivateTrail;table=private_trails"`
	ID       int64    `db:"id" golem:"pk"`
	PublicID int64    `db:"public_id"`
	Public   *Public  `db:"-" golem:"relation=belongs_to;fields=public_id;references=id;internal"`
}

func (PrivateTrail) GolemModel() golem.ModelSpec[PrivateTrail] {
	return golem.DefineModel(
		golem.GraphQL[PrivateTrail](golem.GraphQLHidden()),
		golem.RelationOptions(PrivateTrails.Public).OnDelete(golem.Cascade),
	)
}

func (Team) DefinePolicy(rules *golem.Rules[Team], _ Actor)     { rules.CanRead(golem.All[Team]()) }
func (Invite) DefinePolicy(rules *golem.Rules[Invite], _ Actor) { rules.CanRead(golem.All[Invite]()) }
func (Solo) DefinePolicy(rules *golem.Rules[Solo], _ Actor)     { rules.CanRead(golem.All[Solo]()) }
func (Hooked) DefinePolicy(rules *golem.Rules[Hooked], _ Actor) { rules.CanRead(golem.All[Hooked]()) }
func (Public) DefinePolicy(rules *golem.Rules[Public], _ Actor) { rules.CanRead(golem.All[Public]()) }

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "mutation_scope")
	golem.Actor[Actor](schema)
	golem.Model[Team](schema)
	golem.Model[Invite](schema)
	golem.Model[Solo](schema)
	golem.Model[Hooked](schema)
	golem.Model[Public](schema)
	golem.Model[PrivateTrail](schema)
	golem.Providers(schema, golem.SQLite)
}
