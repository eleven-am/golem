package capabilities

import g "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }
type Payload struct{ Label string }
type Status string

const (
	StatusDraft Status = "draft"
	StatusLive  Status = "live"
)

func (Status) GolemEnum() g.EnumSpec[Status] {
	return g.DefineEnum(g.EnumValue(StatusDraft), g.EnumValue(StatusLive))
}

type User struct {
	_     struct{} `golem:"model;id=pipeline.CapabilityUser;table=capability_users"`
	ID    int64    `db:"id" golem:"id=pipeline.CapabilityUser.ID;pk"`
	Posts []Post   `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

type Post struct {
	_ struct{} `golem:"model;id=pipeline.CapabilityPost;table=capability_posts"`

	ID                int64                   `db:"id" golem:"id=pipeline.CapabilityPost.ID;pk"`
	AuthorID          int64                   `db:"author_id"`
	Published         bool                    `db:"published"`
	Status            Status                  `db:"status"`
	Rating            int64                   `db:"rating"`
	Title             string                  `db:"title"`
	Blob              []byte                  `db:"blob"`
	Tags              g.List[string]          `db:"tags"`
	Metadata          g.JSON[Payload]         `db:"metadata" golem:"type=json"`
	OptionalPublished g.Null[bool]            `db:"optional_published"`
	OptionalRating    g.Null[int64]           `db:"optional_rating"`
	OptionalTitle     g.Null[string]          `db:"optional_title"`
	OptionalBlob      g.Null[[]byte]          `db:"optional_blob"`
	OptionalTags      g.Null[g.List[string]]  `db:"optional_tags"`
	OptionalMetadata  g.Null[g.JSON[Payload]] `db:"optional_metadata" golem:"type=json"`
	Author            *User                   `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}

func DefineSchema(schema *g.Schema) {
	g.SchemaName(schema, "pipeline_capabilities")
	g.Actor[Actor](schema)
	g.Model[User](schema)
	g.Model[Post](schema)
	g.Providers(schema, g.SQLite, g.PostgreSQL)
}

func (Post) GolemModel() g.ModelSpec[Post] {
	return g.DefineModel(
		g.Check("ck_capability_rating", Posts.Rating.Expr().GTE(int64(0))),
		g.Index[Post]("idx_capability_title").Keys(g.IndexExpr(g.Lower(Posts.Title))),
		g.Index[Post]("idx_capability_expr_matrix").Keys(
			g.IndexExpr(Posts.Published.Expr()),
			g.IndexExpr(Posts.Status.Expr()),
			g.IndexExpr(Posts.Rating.Expr()),
			g.IndexExpr(Posts.Title.Expr()),
			g.IndexExpr(Posts.Blob.Expr()),
			g.IndexExpr(Posts.Tags.Expr()),
			g.IndexExpr(Posts.Metadata.Expr()),
			g.IndexExpr(Posts.OptionalPublished.Expr()),
			g.IndexExpr(Posts.OptionalRating.Expr()),
			g.IndexExpr(Posts.OptionalTitle.Expr()),
			g.IndexExpr(Posts.OptionalBlob.Expr()),
			g.IndexExpr(Posts.OptionalTags.Expr()),
			g.IndexExpr(Posts.OptionalMetadata.Expr()),
		),
	)
}

func (User) DefinePolicy(rules *g.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.ID))
	rules.CanRead(Users.Posts.Some(Posts.Published.Eq(true)))
	rules.CanRead(Users.Posts.Every(Posts.Rating.GTE(int64(0))))
	rules.CanRead(Users.Posts.None(Posts.Status.Eq(StatusDraft)))
}

func (Post) DefinePolicy(rules *g.Rules[Post], actor Actor) {
	rules.CanRead(Posts.Published.Ne(false))
	rules.CanRead(Posts.Status.In(StatusDraft, StatusLive))
	rules.CanRead(Posts.Rating.GTE(int64(0)))
	rules.CanRead(Posts.Title.Contains("golem"))
	rules.CanRead(Posts.Blob.In([]byte("public")))
	rules.CanRead(Posts.Tags.Has("go"))
	rules.CanRead(Posts.Tags.HasEvery("go", "api"))
	rules.CanRead(Posts.Tags.HasSome("go", "typescript"))
	rules.CanRead(Posts.Tags.IsEmpty(false))
	rules.CanRead(Posts.Tags.Eq(g.List[string]{"go"}))
	rules.CanRead(Posts.OptionalPublished.IsNull())
	rules.CanRead(Posts.OptionalRating.IsNotNull())
	rules.CanRead(Posts.OptionalTitle.IsNull())
	rules.CanRead(Posts.OptionalBlob.IsNotNull())
	rules.CanRead(Posts.OptionalTags.IsNull())
	rules.CanRead(Posts.OptionalMetadata.IsNotNull())
	rules.CanRead(Posts.Author.Is(Users.ID.Eq(actor.ID)))
	rules.CanRead(Posts.Author.IsNot(Users.ID.Ne(actor.ID)))
	rules.CanRead(Posts.Author.IsNull())
	_ = Posts.Metadata.Expr()
}
