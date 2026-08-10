package social

import "github.com/eleven-am/golem/go/golem"

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	if !actor.Authenticated {
		return
	}
	self := Users.ID.Eq(actor.UserID)
	rules.CanRead(golem.All[User]())
	rules.CannotReadFields(golem.All[User](), Users.Email)
	rules.CanReadFields(self, Users.Email)
	rules.CanCreate(self)
	rules.CanUpdate(self)
	rules.CanDelete(self)
}

func (Session) DefinePolicy(rules *golem.Rules[Session], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := Sessions.UserID.Eq(actor.UserID)
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanDelete(owned)
}

func (Post) DefinePolicy(rules *golem.Rules[Post], actor Actor) {
	visible := Posts.Published.Eq(true)
	if !actor.Authenticated {
		rules.CanRead(visible)
		rules.CannotReadFields(golem.All[Post](), Posts.Body)
		return
	}
	owned := Posts.AuthorID.Eq(actor.UserID)
	rules.CanRead(visible.Or(owned))
	rules.CannotReadFields(golem.All[Post](), Posts.Body)
	rules.CanReadFields(owned, Posts.Body)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
}

func (Comment) DefinePolicy(rules *golem.Rules[Comment], actor Actor) {
	rules.CanRead(golem.All[Comment]())
	if !actor.Authenticated {
		return
	}
	owned := Comments.AuthorID.Eq(actor.UserID)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
}

func (Tag) DefinePolicy(rules *golem.Rules[Tag], actor Actor) {
	rules.CanRead(golem.All[Tag]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Tag]())
	}
}

func (PostTag) DefinePolicy(rules *golem.Rules[PostTag], actor Actor) {
	rules.CanRead(golem.All[PostTag]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[PostTag]())
		rules.CanDelete(golem.All[PostTag]())
	}
}
