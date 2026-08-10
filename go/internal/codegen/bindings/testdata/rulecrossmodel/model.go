package rulecrossmodel

import g "github.com/eleven-am/golem/go/golem"

type Actor struct{}
type User struct{ ID int64 }
type Post struct{ ID int64 }

func (User) DefinePolicy(rules *g.Rules[User], _ Actor) {
	rules.CanReadFields(g.All[User](), Posts.ID)
}

func (Post) DefinePolicy(rules *g.Rules[Post], _ Actor) {
	rules.CanRead(g.All[Post]())
}
