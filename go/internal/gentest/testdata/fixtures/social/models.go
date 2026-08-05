// Package social is source testdata for the P1 compiler. Files below testdata
// are not compiled as part of gentest itself.
package social

import (
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type Actor struct {
	ID golem.UUID
}

type User struct {
	_ struct{} `golem:"model;id=social.User;table=users;graphql=User"`

	ID        golem.UUID `db:"id" golem:"id=social.User.ID;pk"`
	Handle    string     `db:"handle" golem:"id=social.User.Handle;type=varchar(40);unique"`
	CreatedAt time.Time  `db:"created_at" golem:"id=social.User.CreatedAt;default=now;readonly"`

	Posts []Post `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

type Post struct {
	_ struct{} `golem:"model;id=social.Post;table=posts;graphql=Post"`
	_ struct{} `golem:"index=idx_posts_author_created(author_id,created_at)"`

	ID        golem.UUID `db:"id" golem:"id=social.Post.ID;pk"`
	AuthorID  golem.UUID `db:"author_id" golem:"id=social.Post.AuthorID"`
	Title     string     `db:"title" golem:"id=social.Post.Title;type=varchar(200)"`
	Body      *string    `db:"body" golem:"id=social.Post.Body"`
	CreatedAt time.Time  `db:"created_at" golem:"id=social.Post.CreatedAt;default=now;readonly"`

	Author *User `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "social")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Post](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
