package social

import (
	"context"
	"errors"
	"strings"

	"github.com/eleven-am/golem/go/golem"
)

type ExcerptArgs struct {
	Maximum int32 `golem:"graphql=maximum"`
}

type DisplayCodeArgs struct {
	Prefix string `golem:"graphql=prefix"`
}

func (Post) DefineGraphQL(graphql *golem.GraphQLModel[Post]) {
	golem.ComputedField(graphql, "excerpt", golem.GraphQLString(), Post{}.Excerpt, golem.Requires(Posts.Body))
	golem.BatchedComputedFieldWithCacheKey(graphql, "displayCode", golem.GraphQLString().NonNull(), Posts.ID, LoadPostDisplayCodes, PostDisplayCodeCacheKey, 64)
}

func (Post) Excerpt(_ context.Context, row golem.Row[Post], arguments ExcerptArgs) (string, error) {
	body, present := golem.Value(row, Posts.Body).Get()
	if !present {
		return "", golem.MaskedDependency("excerpt", "body")
	}
	maximum := int(arguments.Maximum)
	if maximum <= 0 || maximum > 280 {
		maximum = 120
	}
	runes := []rune(strings.TrimSpace(body))
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes), nil
}

type PostDisplayCodeLoaderObserver interface {
	ObservePostDisplayCodeLoad(context.Context, []golem.UUID, DisplayCodeArgs) error
}

type postDisplayCodeLoaderObserverKey struct{}

// WithPostDisplayCodeLoaderObserver installs an operation-local probe used to
// verify batching and cache isolation without publishing a process-global test
// hook or granting a resolver any additional capability.
func WithPostDisplayCodeLoaderObserver(ctx context.Context, observer PostDisplayCodeLoaderObserver) context.Context {
	return context.WithValue(ctx, postDisplayCodeLoaderObserverKey{}, observer)
}

func LoadPostDisplayCodes(ctx context.Context, keys []golem.UUID, arguments DisplayCodeArgs) (map[golem.UUID]string, error) {
	if observer, _ := ctx.Value(postDisplayCodeLoaderObserverKey{}).(PostDisplayCodeLoaderObserver); observer != nil {
		copied := append([]golem.UUID(nil), keys...)
		if err := observer.ObservePostDisplayCodeLoad(ctx, copied, arguments); err != nil {
			return nil, err
		}
	}
	result := make(map[golem.UUID]string, len(keys))
	for _, key := range keys {
		result[key] = arguments.Prefix + key.String()
	}
	return result, nil
}

func PostDisplayCodeCacheKey(value golem.UUID) (string, error) { return value.String(), nil }

type SearchPostsArgs struct {
	Where golem.Predicate[Post] `golem:"graphql=where"`
	Take  int32                 `golem:"graphql=take"`
}

type PublishPostArgs struct {
	PostID golem.UUID `golem:"graphql=postID"`
	Fail   bool       `golem:"graphql=fail"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "searchPosts", SearchPosts)
	golem.Mutation(graphql, "publishPost", PublishPost)
}

func SearchPosts(ctx context.Context, caller *Caller[Principal], arguments SearchPostsArgs) ([]golem.Row[Post], error) {
	take := int(arguments.Take)
	if take <= 0 || take > 50 {
		take = 20
	}
	return caller.Posts.FindMany(ctx,
		Posts.Where(arguments.Where),
		Posts.OrderBy(Posts.CreatedAt.Desc(), Posts.ID.Asc()),
		Posts.Take(take),
		Posts.Select(Posts.ID, Posts.AuthorID, Posts.Title, Posts.Body, Posts.Published, Posts.CreatedAt),
	)
}

func PublishPost(ctx context.Context, caller *Caller[Principal], arguments PublishPostArgs) (int64, error) {
	var result int64
	err := caller.Transaction(ctx, func(transaction *CallerTx[Principal]) error {
		updated, err := transaction.Posts.UpdateMany(ctx,
			Posts.ID.Eq(arguments.PostID),
			Posts.UpdateMany(Posts.Published.Set(true)),
		)
		if err != nil {
			return err
		}
		result = updated
		if arguments.Fail {
			return errors.New("requested publish rollback")
		}
		return nil
	})
	return result, err
}
