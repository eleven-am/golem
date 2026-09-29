package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

type referenceWorld struct {
	ctx           context.Context
	application   *social.App[social.Principal]
	alice         *social.Caller[social.Principal]
	aliceID       golem.UUID
	alicePost     golem.UUID
	hiddenPost    golem.UUID
	publishedPost golem.UUID
	comment       golem.UUID
	missing       golem.UUID
	next          int
}

func TestReferenceReachSQLite(t *testing.T) {
	dsn := "file:" + t.TempDir() + "/reference.sqlite"
	applyReviewedSQLiteMigration(t, socialHostRoot(t), dsn)
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runReferenceReach(t, database)
}

func TestReferenceReachPostgreSQL(t *testing.T) {
	dsn := testenv.DisposablePostgreSQLFrom(t, testenv.PostgreSQLDSN(t, testenv.PostgreSQLDSNVariable))
	applyReviewedPostgreSQLMigration(t, socialHostRoot(t), dsn)
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runReferenceReach(t, database)
}

func runReferenceReach(t *testing.T, database *provider.Database) {
	world := newReferenceWorld(t, database)
	t.Run("unreadable and missing foreign keys fail identically", world.assertIdenticalOutcomes)
	t.Run("connect needs read not update", world.assertConnectUsesReadReach)
	t.Run("connect or create reports an unreadable existing target as missing", world.assertConnectOrCreate)
	t.Run("system clients are unaffected", world.assertSystem)
	t.Run("graphql", world.assertGraphQL)
}

func newReferenceWorld(t *testing.T, database *provider.Database) *referenceWorld {
	t.Helper()
	ctx := context.Background()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(ctx, database, transport)
	if err != nil {
		t.Fatal(err)
	}
	aliceID := mustUUID(t, "11000000-0000-0000-0000-000000000001")
	bobID := mustUUID(t, "11000000-0000-0000-0000-000000000002")
	system := application.System()
	for _, user := range []struct {
		id     golem.UUID
		handle string
	}{{aliceID, "alice"}, {bobID, "bob"}} {
		if _, err := system.Users.Create(ctx, social.Users.Create(
			social.Users.ID.Create(user.id), social.Users.Handle.Create(user.handle), social.Users.Email.Create(user.handle+"@example.test"),
		)); err != nil {
			t.Fatal(err)
		}
	}
	alice, err := application.ForPrincipal(ctx, social.Principal{Development: true, DevUserID: aliceID})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := application.ForPrincipal(ctx, social.Principal{Development: true, DevUserID: bobID})
	if err != nil {
		t.Fatal(err)
	}
	world := &referenceWorld{
		ctx: ctx, application: application, alice: alice, aliceID: aliceID,
		alicePost:     mustUUID(t, "21000000-0000-0000-0000-000000000001"),
		hiddenPost:    mustUUID(t, "21000000-0000-0000-0000-000000000002"),
		publishedPost: mustUUID(t, "21000000-0000-0000-0000-000000000003"),
		comment:       mustUUID(t, "31000000-0000-0000-0000-000000000001"),
		missing:       mustUUID(t, "99000000-0000-0000-0000-000000000001"),
	}
	if _, err := alice.Posts.Create(ctx, referencePostInput(t, world.alicePost, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Posts.Create(ctx, referencePostInput(t, world.hiddenPost, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Posts.Create(ctx, referencePostInput(t, world.publishedPost, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Posts.FindUnique(ctx, social.Posts.ByID.Value(world.hiddenPost)); err == nil {
		t.Fatal("the unreadable post is readable; the scenario is void")
	}
	if _, err := alice.Comments.Create(ctx, social.Comments.Create(
		social.Comments.ID.Create(world.comment), social.Comments.PostID.Create(world.alicePost),
		social.Comments.AuthorID.Create(aliceID), social.Comments.Body.Create("anchor"),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Tags.Create(ctx, social.Tags.Create(social.Tags.Name.Create("shared"))); err != nil {
		t.Fatal(err)
	}
	return world
}

func referencePostInput(t *testing.T, id golem.UUID, published bool) social.PostCreateInput {
	t.Helper()
	return social.Posts.Create(
		social.Posts.ID.Create(id), social.Posts.Title.Create("reference"), social.Posts.Body.Create("reference body"),
		social.Posts.Published.Create(published),
		social.Posts.LiveDate.Create(mustDate(t, "2026-08-09")), social.Posts.LiveTime.Create(mustTime(t, "13:14:15")),
		social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)),
		social.Posts.Topics.Create(golem.List[string]{"reference"}),
	)
}

func (world *referenceWorld) fresh(t *testing.T) golem.UUID {
	t.Helper()
	world.next++
	return mustUUID(t, fmt.Sprintf("32000000-0000-0000-0000-%012d", world.next))
}

func referenceOutcome(count int64, err error) string {
	var failure *golem.Error
	if errors.As(err, &failure) {
		return fmt.Sprintf("%s: %s", failure.Code, failure.Message)
	}
	if err != nil {
		return "untyped: " + err.Error()
	}
	return fmt.Sprintf("ok count=%d", count)
}

func (world *referenceWorld) assertIdenticalOutcomes(t *testing.T) {
	ctx, alice := world.ctx, world.alice
	paths := []struct {
		name string
		run  func(golem.UUID) (int64, error)
	}{
		{"root create", func(post golem.UUID) (int64, error) {
			_, err := alice.Comments.Create(ctx, social.Comments.Create(social.Comments.ID.Create(world.fresh(t)), social.Comments.PostID.Create(post), social.Comments.AuthorID.Create(world.aliceID), social.Comments.Body.Create("c")))
			return 0, err
		}},
		{"root update", func(post golem.UUID) (int64, error) {
			_, err := alice.Comments.Update(ctx, social.Comments.ByID.Value(world.comment), social.Comments.Update(social.Comments.PostID.Set(post)))
			return 0, err
		}},
		{"root update many", func(post golem.UUID) (int64, error) {
			return alice.Comments.UpdateMany(ctx, social.Comments.ID.Eq(world.comment), social.Comments.UpdateMany(social.Comments.PostID.Set(post)))
		}},
		{"root upsert create", func(post golem.UUID) (int64, error) {
			id := world.fresh(t)
			_, err := alice.Comments.Upsert(ctx, social.Comments.ByID.Value(id),
				social.Comments.Create(social.Comments.ID.Create(id), social.Comments.PostID.Create(post), social.Comments.AuthorID.Create(world.aliceID), social.Comments.Body.Create("c")),
				social.Comments.Update(social.Comments.Body.Set("unused")))
			return 0, err
		}},
		{"root upsert update", func(post golem.UUID) (int64, error) {
			_, err := alice.Comments.Upsert(ctx, social.Comments.ByID.Value(world.comment),
				social.Comments.Create(social.Comments.ID.Create(world.comment), social.Comments.PostID.Create(post), social.Comments.AuthorID.Create(world.aliceID), social.Comments.Body.Create("c")),
				social.Comments.Update(social.Comments.PostID.Set(post)))
			return 0, err
		}},
		{"nested create", func(post golem.UUID) (int64, error) {
			_, err := alice.Users.Update(ctx, social.Users.ByID.Value(world.aliceID), social.Users.Update(social.Users.Comments.Create(
				social.Comments.Create(social.Comments.ID.Create(world.fresh(t)), social.Comments.PostID.Create(post), social.Comments.Body.Create("c")),
			)))
			return 0, err
		}},
		{"nested update", func(post golem.UUID) (int64, error) {
			_, err := alice.Users.Update(ctx, social.Users.ByID.Value(world.aliceID), social.Users.Update(social.Users.Comments.Update(
				social.Comments.ByID.Value(world.comment), social.Comments.Update(social.Comments.PostID.Set(post)),
			)))
			return 0, err
		}},
		{"nested update many", func(post golem.UUID) (int64, error) {
			_, err := alice.Users.Update(ctx, social.Users.ByID.Value(world.aliceID), social.Users.Update(social.Users.Comments.UpdateMany(
				social.Comments.ID.Eq(world.comment), social.Comments.UpdateMany(social.Comments.PostID.Set(post)),
			)))
			return 0, err
		}},
		{"connect on create", func(post golem.UUID) (int64, error) {
			_, err := alice.Comments.Create(ctx, social.Comments.Create(social.Comments.ID.Create(world.fresh(t)), social.Comments.Post.Connect(social.Posts.ByID.Value(post)), social.Comments.AuthorID.Create(world.aliceID), social.Comments.Body.Create("c")))
			return 0, err
		}},
		{"connect on update", func(post golem.UUID) (int64, error) {
			_, err := alice.Comments.Update(ctx, social.Comments.ByID.Value(world.comment), social.Comments.Update(social.Comments.Post.Connect(social.Posts.ByID.Value(post))))
			return 0, err
		}},
		{"connect on nested create", func(post golem.UUID) (int64, error) {
			_, err := alice.Users.Update(ctx, social.Users.ByID.Value(world.aliceID), social.Users.Update(social.Users.Comments.Create(
				social.Comments.Create(social.Comments.ID.Create(world.fresh(t)), social.Comments.Post.Connect(social.Posts.ByID.Value(post)), social.Comments.Body.Create("c")),
			)))
			return 0, err
		}},
	}
	for _, path := range paths {
		unreadable := referenceOutcome(path.run(world.hiddenPost))
		missing := referenceOutcome(path.run(world.missing))
		if unreadable != missing || unreadable == "ok count=0" || unreadable == "ok count=1" {
			t.Errorf("%s: unreadable=%q missing=%q", path.name, unreadable, missing)
		}
	}
	world.assertAnchorUnchanged(t)
}

func (world *referenceWorld) assertAnchorUnchanged(t *testing.T) {
	t.Helper()
	count, err := world.application.System().Comments.Count(world.ctx, social.Comments.Where(social.Comments.PostID.Eq(world.hiddenPost)))
	if err != nil || count != 0 {
		t.Fatalf("a caller linked a comment to an unreadable post: count=%d error=%v", count, err)
	}
	anchored, err := world.application.System().Comments.Count(world.ctx, social.Comments.Where(social.Comments.ID.Eq(world.comment).And(social.Comments.PostID.Eq(world.alicePost))))
	if err != nil || anchored != 1 {
		t.Fatalf("anchor comment moved: count=%d error=%v", anchored, err)
	}
}

func (world *referenceWorld) assertConnectUsesReadReach(t *testing.T) {
	ctx, alice := world.ctx, world.alice
	if _, err := alice.PostTags.Create(ctx, social.PostTags.Create(
		social.PostTags.Post.Connect(social.Posts.ByID.Value(world.alicePost)), social.PostTags.Tag.Connect(social.Tags.ByName.Value("shared")),
	)); err != nil {
		t.Fatalf("connect to a readable tag the caller cannot update: %v", err)
	}
	id := world.fresh(t)
	if _, err := alice.Comments.Create(ctx, social.Comments.Create(
		social.Comments.ID.Create(id), social.Comments.Post.Connect(social.Posts.ByID.Value(world.publishedPost)),
		social.Comments.AuthorID.Create(world.aliceID), social.Comments.Body.Create("c"),
	)); err != nil {
		t.Fatalf("connect to a readable post the caller cannot update: %v", err)
	}
	if _, err := alice.Comments.Update(ctx, social.Comments.ByID.Value(id), social.Comments.Update(social.Comments.PostID.Set(world.alicePost))); err != nil {
		t.Fatalf("direct write to a readable post: %v", err)
	}
}

func (world *referenceWorld) assertConnectOrCreate(t *testing.T) {
	ctx, alice := world.ctx, world.alice
	if _, err := alice.PostTags.Create(ctx, social.PostTags.Create(
		social.PostTags.PostID.Create(world.publishedPost),
		social.PostTags.Tag.ConnectOrCreate(social.Tags.ByName.Value("shared"), social.Tags.Create(social.Tags.Name.Create("shared"))),
	)); err != nil {
		t.Fatalf("connect-or-create against a readable tag the caller cannot update: %v", err)
	}
	if count, err := world.application.System().Tags.Count(ctx, social.Tags.Where(social.Tags.Name.Eq("shared"))); err != nil || count != 1 {
		t.Fatalf("shared tag count=%d error=%v", count, err)
	}
	_, err := alice.PostTags.Create(ctx, social.PostTags.Create(
		social.PostTags.TagName.Create("shared"),
		social.PostTags.Post.ConnectOrCreate(social.Posts.ByID.Value(world.hiddenPost), minimalPostInput(t, world.hiddenPost, "duplicate")),
	))
	if outcome := referenceOutcome(0, err); outcome != "NOT_FOUND: record not found" {
		t.Fatalf("connect-or-create against an unreadable existing post=%q", outcome)
	}
}

func (world *referenceWorld) assertSystem(t *testing.T) {
	ctx := world.ctx
	system := world.application.System()
	id := world.fresh(t)
	if _, err := system.Comments.Create(ctx, social.Comments.Create(
		social.Comments.ID.Create(id), social.Comments.PostID.Create(world.hiddenPost),
		social.Comments.AuthorID.Create(world.aliceID), social.Comments.Body.Create("system"),
	)); err != nil {
		t.Fatalf("system client linking to any post: %v", err)
	}
	if _, err := system.Comments.Delete(ctx, social.Comments.ByID.Value(id)); err != nil {
		t.Fatal(err)
	}
}

func (world *referenceWorld) assertGraphQL(t *testing.T) {
	var mu sync.Mutex
	var reported []string
	graph, err := world.application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		ReportInternalError: func(_ context.Context, err error) {
			mu.Lock()
			reported = append(reported, err.Error())
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Shutdown(context.Background())
	principal := social.Principal{Development: true, DevUserID: world.aliceID}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		graph.Handler().ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
	})
	outcome := func(response graphResponse) string {
		if len(response.Errors) == 0 {
			return "ok " + fmt.Sprint(response.Data)
		}
		extensions, _ := response.Errors[0]["extensions"].(map[string]any)
		return fmt.Sprintf("%v: %v", extensions["code"], response.Errors[0]["message"])
	}
	updateMany := func(post golem.UUID) string {
		return outcome(graphqlHTTP(t, handler, "unused", `mutation Move($comment: UUID!, $post: UUID!) {
  updateManyComments(where: {id: {equals: $comment}}, data: {postID: {set: $post}}) { count }
}`, map[string]any{"comment": world.comment.String(), "post": post.String()}))
	}
	if unreadable, missing := updateMany(world.hiddenPost), updateMany(world.missing); unreadable != missing || unreadable[:2] == "ok" {
		t.Errorf("graphql updateManyComments unreadable=%q missing=%q", unreadable, missing)
	}
	create := func(post golem.UUID) string {
		return outcome(graphqlHTTP(t, handler, "unused", `mutation Attach($id: UUID!, $post: UUID!, $author: UUID!) {
  createComment(data: {id: $id, body: "c", post: {connect: {ID: $post}}, author: {connect: {ID: $author}}}) { id }
}`, map[string]any{"id": world.fresh(t).String(), "post": post.String(), "author": world.aliceID.String()}))
	}
	if unreadable, missing := create(world.hiddenPost), create(world.missing); unreadable != missing || unreadable[:2] == "ok" {
		t.Errorf("graphql createComment connect unreadable=%q missing=%q", unreadable, missing)
	}
	taggedPost := world.fresh(t)
	if _, err := world.alice.Posts.Create(world.ctx, referencePostInput(t, taggedPost, false)); err != nil {
		t.Fatal(err)
	}
	tagged := outcome(graphqlHTTP(t, handler, "unused", `mutation Tag($post: UUID!) {
  createPostTag(data: {post: {connect: {ID: $post}}, tag: {connect: {Name: "shared"}}}) { tagName }
}`, map[string]any{"post": taggedPost.String()}))
	if tagged[:2] != "ok" {
		t.Errorf("graphql connect to a readable tag the caller cannot update=%q", tagged)
	}
	masked := graphqlHTTP(t, handler, "unused", `query Masked($id: UUID!) { post(where: {ID: $id}) { id excerpt(maximum: 8) } }`, map[string]any{"id": world.publishedPost.String()})
	if len(masked.Errors) != 0 {
		t.Errorf("computed field over a masked dependency errors=%v body=%s", masked.Errors, masked.Raw)
	} else if post := graphMap(t, graphMap(t, masked.Data)["post"]); post["excerpt"] != nil || post["id"] != world.publishedPost.String() {
		t.Errorf("computed field over a masked dependency=%v", post)
	}
	world.assertAnchorUnchanged(t)
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 0 {
		t.Errorf("internal errors reported=%v", reported)
	}
}
