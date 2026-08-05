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
}

func (User) DefinePolicy(rules *g.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.ID))
}

func (Post) DefinePolicy(rules *g.Rules[Post], actor Actor) {
	rules.CanCreate(Posts.AuthorID.Eq(actor.ID))
}

func (Post) BeforeCreate(ctx context.Context, request *PostCreateRequest) error {
	actor := g.ActorFrom[Actor](ctx)
	return g.SetCreate(request, Posts.AuthorID, actor.ID)
}

func (Post) AfterCreate(_ context.Context, _ PostCreateResult) error       { return nil }
func (Post) AfterCommitCreate(_ context.Context, _ PostCreateResult) error { return nil }
