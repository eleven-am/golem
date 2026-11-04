package loadmix

import "github.com/eleven-am/golem/go/golem"

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	if !actor.Authenticated {
		return
	}
	rules.CanRead(Users.ID.Eq(actor.UserID))
}

func (Article) DefinePolicy(rules *golem.Rules[Article], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := Articles.UserID.Eq(actor.UserID)
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
}

func (ArticleAudio) DefinePolicy(rules *golem.Rules[ArticleAudio], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := ArticleAudios.UserID.Eq(actor.UserID).
		And(ArticleAudios.Article.Is(Articles.UserID.Eq(actor.UserID)))
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
}

func (ChatThread) DefinePolicy(rules *golem.Rules[ChatThread], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := ChatThreads.UserID.Eq(actor.UserID).
		And(ChatThreads.Article.Is(Articles.UserID.Eq(actor.UserID)))
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
}

func (ChatMessage) DefinePolicy(rules *golem.Rules[ChatMessage], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := ChatMessages.Thread.Is(ChatThreads.UserID.Eq(actor.UserID))
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
}

func (ReadingSession) DefinePolicy(rules *golem.Rules[ReadingSession], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := ReadingSessions.Article.Is(Articles.UserID.Eq(actor.UserID))
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
}
