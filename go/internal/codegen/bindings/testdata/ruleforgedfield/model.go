package ruleforgedfield

import g "github.com/eleven-am/golem/go/golem"

type Actor struct{}
type User struct{ ID int64 }

type forgedField struct{}

func (forgedField) fieldModel(User)          {}
func (forgedField) fieldIdentity() g.FieldID { return g.FieldID{} }

func (User) DefinePolicy(rules *g.Rules[User], _ Actor) {
	rules.CanReadFields(g.All[User](), forgedField{})
}
