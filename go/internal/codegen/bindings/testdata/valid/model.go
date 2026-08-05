package valid

import (
	"context"

	g "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ ID int64 }
type User struct{ ID int64 }
type Post struct {
	ID       int64
	AuthorID int64
	Author   *User
}

func (User) DefinePolicy(rules *g.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.ID))
	rules.CannotRead(g.None[User]())
	rules.CanCreate(g.All[User]())
	rules.CannotCreate(g.None[User]())
	rules.CanUpdate(Users.ID.Eq(actor.ID))
	rules.CannotUpdate(g.None[User]())
	rules.CanDelete(Users.ID.Eq(actor.ID))
	rules.CannotDelete(g.None[User]())
}

func (Post) DefinePolicy(rules *g.Rules[Post], actor Actor) {
	owned := Posts.AuthorID.Eq(actor.ID)
	rules.CanCreate(owned)
	rules.CanReadFields(g.All[Post](), Posts.ID)
	rules.CannotReadFields(g.All[Post](), Posts.AuthorID, Posts.Author)
	rules.CanCreateFields(owned, Posts.AuthorID, Posts.Author)
	rules.CannotCreateFields(g.None[Post](), Posts.ID)
	rules.CanUpdateFields(owned, Posts.AuthorID)
	rules.CannotUpdateFields(g.None[Post](), Posts.ID, Posts.Author)
	allowPostFields(rules, owned, Posts.ID, Posts.Author)
}

func allowPostFields(rules *g.Rules[Post], predicate g.Predicate[Post], first g.Field[Post], rest ...g.Field[Post]) {
	rules.CanReadFields(predicate, first, rest...)
}

func (Post) BeforeCreate(ctx context.Context, request *PostCreateRequest) error {
	actor := g.ActorFrom[Actor](ctx)
	return g.SetCreate(request, Posts.AuthorID, actor.ID)
}

func (Post) AfterCreate(_ context.Context, _ PostCreateResult) error       { return nil }
func (Post) AfterCommitCreate(_ context.Context, _ PostCreateResult) error { return nil }
