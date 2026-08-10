package social

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

type PostHookPhase string

const (
	PostHookBeforeCreate      PostHookPhase = "before_create"
	PostHookAfterCreate       PostHookPhase = "after_create"
	PostHookAfterCommitCreate PostHookPhase = "after_commit_create"
)

type PostReadHookPhase string

const (
	PostHookBeforeFindMany PostReadHookPhase = "before_find_many"
	PostHookAfterFindMany  PostReadHookPhase = "after_find_many"
)

type PostHookObserver interface {
	ObservePostHook(context.Context, PostHookPhase, golem.Row[Post]) error
}

type postHookObserverKey struct{}

type PostReadHookObserver interface {
	ObservePostReadHook(context.Context, PostReadHookPhase, []golem.Row[Post]) error
}

type postReadHookObserverKey struct{}

func WithPostHookObserver(ctx context.Context, observer PostHookObserver) context.Context {
	return context.WithValue(ctx, postHookObserverKey{}, observer)
}

func WithPostReadHookObserver(ctx context.Context, observer PostReadHookObserver) context.Context {
	return context.WithValue(ctx, postReadHookObserverKey{}, observer)
}

func observePostHook(ctx context.Context, phase PostHookPhase, row golem.Row[Post]) error {
	observer, _ := ctx.Value(postHookObserverKey{}).(PostHookObserver)
	if observer == nil {
		return nil
	}
	return observer.ObservePostHook(ctx, phase, row)
}

func observePostReadHook(ctx context.Context, phase PostReadHookPhase, rows []golem.Row[Post]) error {
	observer, _ := ctx.Value(postReadHookObserverKey{}).(PostReadHookObserver)
	if observer == nil {
		return nil
	}
	return observer.ObservePostReadHook(ctx, phase, rows)
}

// BeforeFindMany demonstrates a retry-free request hook without weakening the
// generated policy plan or replacing caller-provided options.
func (Post) BeforeFindMany(ctx context.Context, _ *PostFindManyRequest) error {
	return observePostReadHook(ctx, PostHookBeforeFindMany, nil)
}

// AfterFindMany observes only the already-authorized, already-masked result.
func (Post) AfterFindMany(ctx context.Context, result PostFindManyResult) error {
	return observePostReadHook(ctx, PostHookAfterFindMany, result.Rows())
}

// BeforeCreate is a retry-safe hook. It owns author attribution so neither
// GraphQL nor programmatic callers can choose a different author.
func (Post) BeforeCreate(ctx context.Context, request *PostCreateRequest) error {
	actor := golem.ActorFrom[Actor](ctx)
	if err := golem.SetCreate(request, Posts.AuthorID, actor.UserID); err != nil {
		return err
	}
	return observePostHook(ctx, PostHookBeforeCreate, golem.Row[Post]{})
}

// AfterCreate runs in the transaction and must remain rollback-safe.
func (Post) AfterCreate(ctx context.Context, result PostCreateResult) error {
	return observePostHook(ctx, PostHookAfterCreate, result.Row())
}

// AfterCommitCreate is the place for irreversible notification scheduling.
// Durable domain delivery itself uses Golem's generated outbox/event surface.
func (Post) AfterCommitCreate(ctx context.Context, result PostCreateResult) error {
	return observePostHook(ctx, PostHookAfterCommitCreate, result.Row())
}
