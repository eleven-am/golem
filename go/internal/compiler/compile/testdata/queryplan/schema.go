package queryplanfixture

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ ID golem.UUID }

type User struct {
	_ struct{} `golem:"model;id=queryplan.User;table=users"`

	ID   golem.UUID `db:"id" golem:"id=queryplan.User.ID;pk;default=uuid"`
	Name string     `db:"name" golem:"id=queryplan.User.Name"`
}

type Post struct {
	_ struct{} `golem:"model;id=queryplan.Post;table=posts"`

	ID       golem.UUID `db:"id" golem:"id=queryplan.Post.ID;pk;default=uuid"`
	AuthorID golem.UUID `db:"author_id" golem:"id=queryplan.Post.AuthorID"`
	Views    int64      `db:"views" golem:"id=queryplan.Post.Views"`
	Author   *User      `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "queryplan")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Post](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel(
		golem.ScopedReads[Post](),
		golem.Analytics[Post](
			golem.AnalyticsDimensions(Posts.AuthorID),
			golem.AnalyticsMeasures(Posts.Views),
			golem.AnalyticsRelationDimensions(
				golem.NamedRelationDimension("authorName", golem.Via(Posts.Author, golem.DimensionField(Users.Name))),
			),
		),
	)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) { rules.CanRead(Users.ID.Eq(actor.ID)) }
func (Post) DefinePolicy(rules *golem.Rules[Post], actor Actor) { rules.CanRead(Posts.ID.Eq(actor.ID)) }

// ExplainPost must type-check first against the declaration-discovery superset
// and again against the exact post-contract prospective registry.
func ExplainPost(ctx context.Context, caller *Caller[Actor], selector golem.UniqueSelectorValue[Post], aggregate golem.AggregateRequest[Post], group golem.GroupRequest[Post], relation golem.RelationGroupRequest[Post], scoped golem.ScopedQuery[Post]) error {
	if _, err := caller.Posts.ExplainFindMany(ctx); err != nil {
		return err
	}
	if _, err := caller.Posts.ExplainFindFirst(ctx); err != nil {
		return err
	}
	if _, err := caller.Posts.ExplainFindUnique(ctx, selector); err != nil {
		return err
	}
	if _, err := caller.Posts.ExplainCount(ctx); err != nil {
		return err
	}
	if _, err := caller.Posts.ExplainAggregate(ctx, aggregate); err != nil {
		return err
	}
	if _, err := caller.Posts.ExplainGroupBy(ctx, group); err != nil {
		return err
	}
	if _, err := caller.Posts.ExplainRelationGroupBy(ctx, relation); err != nil {
		return err
	}
	_, err := caller.Posts.ExplainScoped(ctx, scoped)
	return err
}
