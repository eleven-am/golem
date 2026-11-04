package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
)

type dependencyWorld struct {
	ctx         context.Context
	application *social.App[social.Principal]
	alice       golem.UUID
	bob         golem.UUID
	carol       golem.UUID
	dave        golem.UUID
	shared      golem.UUID
	draft       golem.UUID
	aliceDraft  golem.UUID
}

func (world *dependencyWorld) caller(t *testing.T, user golem.UUID) *social.Caller[social.Principal] {
	t.Helper()
	caller, err := world.application.ForPrincipal(world.ctx, social.Principal{Development: true, DevUserID: user})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func newDependencyWorld(t *testing.T, database *provider.Database) *dependencyWorld {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	world := &dependencyWorld{
		ctx: ctx, application: startDeleteEventsApplication(t, ctx, database),
		alice: mustUUID(t, "15000000-0000-0000-0000-000000000001"), bob: mustUUID(t, "15000000-0000-0000-0000-000000000002"),
		carol: mustUUID(t, "15000000-0000-0000-0000-000000000003"), dave: mustUUID(t, "15000000-0000-0000-0000-000000000004"),
		shared: mustUUID(t, "25000000-0000-0000-0000-000000000001"), draft: mustUUID(t, "25000000-0000-0000-0000-000000000002"),
		aliceDraft: mustUUID(t, "25000000-0000-0000-0000-000000000003"),
	}
	for _, user := range []struct {
		id     golem.UUID
		handle string
	}{{world.alice, "alice"}, {world.bob, "bob"}, {world.carol, "carol"}, {world.dave, "dave"}} {
		if _, err := world.application.System().Users.Create(ctx, social.Users.Create(social.Users.ID.Create(user.id), social.Users.Handle.Create(user.handle), social.Users.Email.Create(user.handle+"@example.test"))); err != nil {
			t.Fatal(err)
		}
	}
	settle, err := world.caller(t, world.dave).Posts.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settle.Close() })
	for _, post := range []struct {
		id        golem.UUID
		author    golem.UUID
		published bool
	}{{world.shared, world.carol, true}, {world.draft, world.carol, false}, {world.aliceDraft, world.alice, false}} {
		if _, err := world.caller(t, post.author).Posts.Create(ctx, referencePostInput(t, post.id, post.published)); err != nil {
			t.Fatal(err)
		}
	}
	barrier := mustUUID(t, "25000000-0000-0000-0000-0000000000ff")
	if _, err := world.caller(t, world.carol).Posts.Create(ctx, referencePostInput(t, barrier, true)); err != nil {
		t.Fatal(err)
	}
	pending := map[golem.UUID]bool{world.shared: true, barrier: true}
	for len(pending) > 0 {
		event, err := dependencyRecv(world, settle, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		delete(pending, event.ID())
	}
	return world
}

func (world *dependencyWorld) commentsOn(t *testing.T, user, post golem.UUID) golem.EventStream[social.CommentEvent] {
	t.Helper()
	stream, err := world.caller(t, user).Comments.Events(world.ctx, golem.EventWhere[social.Comment](social.Comments.Post.Is(social.Posts.ID.Eq(post))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func (world *dependencyWorld) comment(t *testing.T, id string, post golem.UUID) golem.UUID {
	t.Helper()
	comment := mustUUID(t, id)
	if _, err := world.application.System().Comments.Create(world.ctx, social.Comments.Create(social.Comments.ID.Create(comment), social.Comments.PostID.Create(post), social.Comments.AuthorID.Create(world.alice), social.Comments.Body.Create("hello"))); err != nil {
		t.Fatal(err)
	}
	return comment
}

func dependencyRecv[E any](world *dependencyWorld, stream golem.EventStream[E], wait time.Duration) (E, error) {
	receive, cancel := context.WithTimeout(world.ctx, wait)
	defer cancel()
	return stream.Recv(receive)
}

func (world *dependencyWorld) expectEnd(t *testing.T, name string, stream golem.EventStream[social.CommentEvent], code events.ErrorCode, reason events.Reason) {
	t.Helper()
	_, err := dependencyRecv(world, stream, 5*time.Second)
	got, coded := events.CodeOf(err)
	gotReason, _ := events.ReasonOf(err)
	if !coded || got != code || gotReason != reason {
		t.Fatalf("%s: stream ended with %v (code %q reason %q), want %s %q", name, err, got, gotReason, code, reason)
	}
}

func (world *dependencyWorld) expectDelivers(t *testing.T, name string, stream golem.EventStream[social.CommentEvent], comment golem.UUID) {
	t.Helper()
	event, err := dependencyRecv(world, stream, 5*time.Second)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if event.ID() != comment {
		t.Fatalf("%s: received %s, want %s", name, event.ID(), comment)
	}
}

func expectDependencySilent[E any](t *testing.T, world *dependencyWorld, name string, stream golem.EventStream[E]) {
	t.Helper()
	event, err := dependencyRecv(world, stream, 400*time.Millisecond)
	if err == nil {
		t.Fatalf("%s: unexpected frame %+v", name, event)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s: stream ended with %v, want silence", name, err)
	}
}

func eachDependencyProvider(t *testing.T, run func(*testing.T, *provider.Database)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { run(t, openDeleteEventsSQLite(t)) })
	t.Run("postgresql", func(t *testing.T) { run(t, openDeleteEventsPostgreSQL(t)) })
}

func TestDependencyRevocationEndsTheStream(t *testing.T) {
	eachDependencyProvider(t, func(t *testing.T, database *provider.Database) {
		world := newDependencyWorld(t, database)
		aliceOwned := mustUUID(t, "25000000-0000-0000-0000-000000000009")
		if _, err := world.caller(t, world.alice).Posts.Create(world.ctx, referencePostInput(t, aliceOwned, true)); err != nil {
			t.Fatal(err)
		}
		bob := world.commentsOn(t, world.bob, aliceOwned)
		if _, err := world.caller(t, world.alice).Posts.Update(world.ctx, social.Posts.ByID.Value(aliceOwned), social.Posts.Update(social.Posts.Published.Set(false))); err != nil {
			t.Fatal(err)
		}
		world.expectEnd(t, "Bob's comments after Alice unpublished", bob, events.CodeSubscriptionResync, events.ReasonDependencyChanged)
	})
}

func TestDependencyThroughOneRelationIgnoresCreatesAndDeletes(t *testing.T) {
	eachDependencyProvider(t, func(t *testing.T, database *provider.Database) {
		world := newDependencyWorld(t, database)
		bob := world.commentsOn(t, world.bob, world.shared)
		extra := mustUUID(t, "25000000-0000-0000-0000-000000000008")
		if _, err := world.caller(t, world.carol).Posts.Create(world.ctx, referencePostInput(t, extra, true)); err != nil {
			t.Fatal(err)
		}
		if _, err := world.caller(t, world.carol).Posts.Delete(world.ctx, social.Posts.ByID.Value(extra)); err != nil {
			t.Fatal(err)
		}
		expectDependencySilent(t, world, "Bob's comments after an unrelated post was created and deleted", bob)
		later := world.comment(t, "35000000-0000-0000-0000-000000000003", world.shared)
		world.expectDelivers(t, "Bob's comments after an unrelated commit", bob, later)
	})
}

func TestDependencyNarrowingKeepsUnrelatedStreamsQuiet(t *testing.T) {
	eachDependencyProvider(t, func(t *testing.T, database *provider.Database) {
		world := newDependencyWorld(t, database)
		bobComments := world.commentsOn(t, world.bob, world.shared)
		daveComments := world.commentsOn(t, world.dave, world.shared)
		bobPosts, err := world.caller(t, world.bob).Posts.Events(world.ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = bobPosts.Close() })
		if _, err := world.caller(t, world.carol).Posts.Update(world.ctx, social.Posts.ByID.Value(world.draft), social.Posts.Update(social.Posts.Title.Set("draft, edited"))); err != nil {
			t.Fatal(err)
		}
		if _, err := world.caller(t, world.alice).Posts.Update(world.ctx, social.Posts.ByID.Value(world.aliceDraft), social.Posts.Update(social.Posts.Title.Set("alice draft, edited"))); err != nil {
			t.Fatal(err)
		}
		expectDependencySilent(t, world, "Bob's posts", bobPosts)
		expectDependencySilent(t, world, "Bob's comments", bobComments)
		expectDependencySilent(t, world, "Dave's comments", daveComments)

		if _, err := world.caller(t, world.carol).Posts.Update(world.ctx, social.Posts.ByID.Value(world.shared), social.Posts.Update(social.Posts.Title.Set("shared, edited"))); err != nil {
			t.Fatal(err)
		}
		if event, err := dependencyRecv(world, bobPosts, 5*time.Second); err != nil || event.ID() != world.shared {
			t.Fatalf("Bob's posts: event %v error %v, want the edit of the post he can read", event.ID(), err)
		}
		expectDependencySilent(t, world, "Bob's comments after an edit outside the fields his policy reads", bobComments)
		expectDependencySilent(t, world, "Dave's comments after an edit outside the fields his policy reads", daveComments)

		later := world.comment(t, "35000000-0000-0000-0000-000000000001", world.shared)
		world.expectDelivers(t, "Bob's comments after an unrelated commit", bobComments, later)
		world.expectDelivers(t, "Dave's comments after an unrelated commit", daveComments, later)
	})
}

func TestUnobservableDependencyIsRefusedAtOpen(t *testing.T) {
	eachDependencyProvider(t, func(t *testing.T, database *provider.Database) {
		world := newDependencyWorld(t, database)
		_, err := world.caller(t, world.bob).Posts.Events(world.ctx, golem.EventWhere[social.Post](social.Posts.Author.Is(social.Users.Handle.Eq("carol"))))
		code, coded := events.CodeOf(err)
		reason, _ := events.ReasonOf(err)
		if !coded || code != events.CodeSubscriptionInvalid || reason != events.ReasonDependencyUnobservable {
			t.Fatalf("open error = %v, want %s %s", err, events.CodeSubscriptionInvalid, events.ReasonDependencyUnobservable)
		}
		if !strings.Contains(err.Error(), "User") {
			t.Fatalf("open error %q does not name the model", err)
		}
	})
}

func TestInvalidatePoliciesEndsEveryOpenStream(t *testing.T) {
	eachDependencyProvider(t, func(t *testing.T, database *provider.Database) {
		world := newDependencyWorld(t, database)
		dependent := world.commentsOn(t, world.bob, world.shared)
		plain, err := world.caller(t, world.carol).Comments.Events(world.ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = plain.Close() })
		posts, err := world.caller(t, world.dave).Posts.Events(world.ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = posts.Close() })
		world.application.InvalidatePolicies()
		world.expectEnd(t, "the dependent comment stream", dependent, events.CodeSubscriptionResync, "")
		world.expectEnd(t, "the plain comment stream", plain, events.CodeSubscriptionResync, "")
		if _, err := dependencyRecv(world, posts, 5*time.Second); !errors.Is(err, events.Failure(events.CodeSubscriptionResync)) {
			t.Fatalf("the post stream ended with %v, want %s", err, events.CodeSubscriptionResync)
		}
		reopened := world.commentsOn(t, world.bob, world.shared)
		later := world.comment(t, "35000000-0000-0000-0000-000000000002", world.shared)
		world.expectDelivers(t, "a stream reopened after the invalidation", reopened, later)
	})
}
