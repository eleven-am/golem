package facts

import g "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }
type User struct{ ID int64 }
type Post struct {
	ID       int64
	AuthorID int64
	Author   *User
}

func (User) DefinePolicy(rules *g.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.ID))
}

func (Post) DefinePolicy(rules *g.Rules[Post], actor Actor) {
	rules.FactScope(Posts.AuthorID.Eq(actor.ID))
	ready := rules.Fact("ready", Posts.Author.Is(Users.ID.Eq(actor.ID)))
	visible := rules.Fact("visible", ready.And(Posts.ID.Eq(actor.ID)))
	rules.CanRead(visible)
	rules.CanUpdate(ready.Not())
}
