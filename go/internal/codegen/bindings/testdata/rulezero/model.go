package rulezero

import g "github.com/eleven-am/golem/go/golem"

type Actor struct{}
type User struct{ ID int64 }

func (User) DefinePolicy(rules *g.Rules[User], _ Actor) {
	rules.CanReadFields(g.All[User]())
}
