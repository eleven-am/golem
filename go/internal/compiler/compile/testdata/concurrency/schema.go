package concurrencyfixture

import (
	"context"

	golem "github.com/eleven-am/golem/go/golem"
)

type Actor struct {
	ID golem.UUID
}

type Post struct {
	_ struct{} `golem:"model;id=concurrency.Post;table=posts"`

	ID    golem.UUID `db:"id" golem:"id=concurrency.Post.ID;pk;default=uuid"`
	Token int64      `db:"token" golem:"id=concurrency.Post.Token"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "concurrency")
	golem.Actor[Actor](schema)
	golem.Model[Post](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel(
		golem.OptimisticConcurrency(Posts.Token),
	)
}

func (Post) DefinePolicy(rules *golem.Rules[Post], actor Actor) {
	rules.CanRead(Posts.ID.Eq(actor.ID))
}

// UpdatePost proves that the declaration-discovery overlay accepts the final
// versioned ABI before OptimisticConcurrency has been installed in ModelIR.
// The generation pipeline type-checks this helper again against final exact
// artifacts, so the permissive bootstrap never becomes published authority.
func UpdatePost(ctx context.Context, caller *Caller[Actor], target golem.MutationTarget[Post], expected golem.ExistingVersion, input PostUpdateInput) (golem.Row[Post], error) {
	return caller.Posts.Update(ctx, target, expected, input)
}
