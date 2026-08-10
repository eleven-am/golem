package social

import (
	"time"

	decl "github.com/eleven-am/golem/go/golem"
)

type User struct {
	_ struct{} `golem:"model;id=social.User;table=users"`

	ID    decl.UUID `db:"id" golem:"id=social.User.ID;pk;default=uuid"`
	Posts []Post    `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

type Post struct {
	_ struct{} `golem:"model;id=social.Post;table=posts;graphql=Post"`
	_ struct{} `golem:"index=idx_posts_author(author_id,created_at)"`

	ID        decl.UUID `db:"id" golem:"id=social.Post.ID;pk;default=uuid"`
	AuthorID  decl.UUID `db:"author_id"`
	Title     string    `db:"title" golem:"type=varchar(120)"`
	CreatedAt time.Time `db:"created_at" golem:"default=now;readonly"`
	Author    *User     `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}

func (Post) GolemModel() decl.ModelSpec[Post] {
	return decl.DefineModel[Post]()
}
