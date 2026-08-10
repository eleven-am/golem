package models

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/generate/pipeline/testdata/multipkg/actor"
)

type User struct {
	_ struct{} `golem:"model;id=pipeline.User;table=users"`

	ID    golem.UUID `db:"id" golem:"id=pipeline.User.ID;pk;default=uuid"`
	Posts []Post     `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

type Post struct {
	_ struct{} `golem:"model;id=pipeline.Post;table=posts"`
	_ struct{} `golem:"index=idx_posts_author(author_id)"`

	ID       golem.UUID `db:"id" golem:"id=pipeline.Post.ID;pk;default=uuid"`
	AuthorID golem.UUID `db:"author_id"`
	Title    *string    `db:"title"`
	Author   *User      `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}

func (User) DefinePolicy(rules *golem.Rules[User], value actor.Actor) {
	rules.CanRead(Users.ID.Eq(value.ID))
}

func (Post) DefinePolicy(rules *golem.Rules[Post], value actor.Actor) {
	rules.CanRead(Posts.AuthorID.Eq(value.ID))
}

func (Post) BeforeCreate(_ context.Context, _ *PostCreateRequest) error { return nil }
