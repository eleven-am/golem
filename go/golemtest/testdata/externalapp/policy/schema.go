package policy

import "github.com/eleven-am/golem/go/golem"

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

type Author struct {
	_ struct{} `golem:"model;id=example.kit.Author;table=authors"`

	ID       golem.UUID `db:"id" golem:"id=example.kit.Author.ID;pk"`
	Name     string     `db:"name" golem:"type=varchar(64)"`
	Verified bool       `db:"verified" golem:"default=false"`
	Listed   bool       `db:"listed" golem:"default=false"`
	Articles []Article  `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

type Article struct {
	_ struct{} `golem:"model;id=example.kit.Article;table=articles"`

	ID       golem.UUID             `db:"id" golem:"id=example.kit.Article.ID;pk"`
	OwnerID  golem.UUID             `db:"owner_id"`
	AuthorID golem.Null[golem.UUID] `db:"author_id"`
	Public   bool                   `db:"public" golem:"default=false"`
	Title    string                 `db:"title" golem:"type=varchar(160)"`
	Draft    string                 `db:"draft"`
	Notes    string                 `db:"notes"`
	Summary  string                 `db:"summary"`
	Secret   string                 `db:"secret"`
	Author   *Author                `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	Comments []Comment              `db:"-" golem:"relation=has_many;fields=id;references=article_id"`
}

type Comment struct {
	_ struct{} `golem:"model;id=example.kit.Comment;table=comments"`

	ID        golem.UUID `db:"id" golem:"id=example.kit.Comment.ID;pk"`
	ArticleID golem.UUID `db:"article_id"`
	Body      string     `db:"body"`
	Approved  bool       `db:"approved" golem:"default=false"`
	Article   *Article   `db:"-" golem:"relation=belongs_to;fields=article_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "golem_policy_kit_gate")
	golem.Actor[Actor](schema)
	golem.Model[Author](schema)
	golem.Model[Article](schema)
	golem.Model[Comment](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
