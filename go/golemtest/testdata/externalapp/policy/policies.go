package policy

import "github.com/eleven-am/golem/go/golem"

func (Author) DefinePolicy(rules *golem.Rules[Author], actor Actor) {
	if !actor.Authenticated {
		return
	}
	rules.CanRead(Authors.Listed.Eq(true))
}

func (Comment) DefinePolicy(rules *golem.Rules[Comment], actor Actor) {
	if !actor.Authenticated {
		return
	}
	rules.CanRead(golem.All[Comment]())
}

func (Article) DefinePolicy(rules *golem.Rules[Article], actor Actor) {
	if !actor.Authenticated {
		return
	}
	everything := golem.All[Article]()
	owned := Articles.OwnerID.Eq(actor.UserID)
	visible := Articles.Public.Eq(true).Or(owned)
	rules.CanRead(visible)
	rules.CannotReadFields(everything, Articles.Draft, Articles.Notes, Articles.Summary, Articles.Secret)
	rules.CanReadFields(owned, Articles.Draft)
	rules.CanReadFields(visible.And(Articles.Author.Is(Authors.Verified.Eq(true))), Articles.Notes)
	rules.CanReadFields(visible.And(Articles.Comments.Some(Comments.Approved.Eq(true))), Articles.Summary)
}
