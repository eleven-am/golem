package main

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func TestCascadeEventsSQLite(t *testing.T) {
	dsn := "file:" + t.TempDir() + "/cascade.sqlite"
	applyReviewedSQLiteMigration(t, socialHostRoot(t), dsn)
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runCascadeEvents(t, database)
}

func TestCascadeEventsPostgreSQL(t *testing.T) {
	dsn := testenv.DisposablePostgreSQLFrom(t, testenv.PostgreSQLDSN(t, testenv.PostgreSQLDSNVariable))
	applyReviewedPostgreSQLMigration(t, socialHostRoot(t), dsn)
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runCascadeEvents(t, database)
}

func runCascadeEvents(t *testing.T, database *provider.Database) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 128})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(ctx, database, transport)
	if err != nil {
		t.Fatal(err)
	}
	publisherContext, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherContext) }()
	defer func() {
		stopPublisher()
		<-publisherDone
	}()
	awaitPublisher(t, application)

	aliceID := mustUUID(t, "12000000-0000-0000-0000-000000000001")
	bobID := mustUUID(t, "12000000-0000-0000-0000-000000000002")
	system := application.System()
	for _, user := range []struct {
		id     golem.UUID
		handle string
	}{{aliceID, "alice"}, {bobID, "bob"}} {
		if _, err := system.Users.Create(ctx, social.Users.Create(social.Users.ID.Create(user.id), social.Users.Handle.Create(user.handle), social.Users.Email.Create(user.handle+"@example.test"))); err != nil {
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
	alicePosts, err := alice.Posts.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer alicePosts.Close()
	aliceComments, err := alice.Comments.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer aliceComments.Close()
	bobPosts, err := bob.Posts.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer bobPosts.Close()
	alicePost := mustUUID(t, "22000000-0000-0000-0000-000000000001")
	bobPost := mustUUID(t, "22000000-0000-0000-0000-000000000002")
	barrier := mustUUID(t, "22000000-0000-0000-0000-000000000003")
	onAlicePost := mustUUID(t, "33000000-0000-0000-0000-000000000001")
	reply := mustUUID(t, "33000000-0000-0000-0000-000000000002")
	onBobPost := mustUUID(t, "33000000-0000-0000-0000-000000000003")
	bobOnAlicePost := mustUUID(t, "33000000-0000-0000-0000-000000000004")
	if _, err := alice.Posts.Create(ctx, referencePostInput(t, alicePost, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Posts.Create(ctx, referencePostInput(t, bobPost, true)); err != nil {
		t.Fatal(err)
	}
	for _, comment := range []struct {
		id, post golem.UUID
		parent   *golem.UUID
	}{{onAlicePost, alicePost, nil}, {reply, alicePost, &onAlicePost}, {onBobPost, bobPost, nil}} {
		values := []golem.CreateValue[social.Comment]{social.Comments.ID.Create(comment.id), social.Comments.PostID.Create(comment.post), social.Comments.AuthorID.Create(aliceID), social.Comments.Body.Create("alice")}
		if comment.parent != nil {
			values = append(values, social.Comments.ParentID.Create(*comment.parent))
		}
		if _, err := alice.Comments.Create(ctx, social.Comments.Create(values...)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := system.Comments.Create(ctx, social.Comments.Create(social.Comments.ID.Create(bobOnAlicePost), social.Comments.PostID.Create(alicePost), social.Comments.AuthorID.Create(bobID), social.Comments.Body.Create("bob"))); err != nil {
		t.Fatal(err)
	}
	assertCascadeEvents(t, ctx, "Alice's posts during setup", alicePosts, []string{"created " + alicePost.String(), "created " + bobPost.String()})
	assertCascadeEvents(t, ctx, "Alice's comments during setup", aliceComments, []string{"created " + onAlicePost.String(), "created " + reply.String(), "created " + onBobPost.String(), "created " + bobOnAlicePost.String()})
	assertCascadeEvents(t, ctx, "Bob's posts during setup", bobPosts, []string{"created " + bobPost.String()})

	if _, err := bob.Posts.Delete(ctx, social.Posts.ByID.Value(bobPost)); err != nil {
		t.Fatalf("Bob deletes his post under Alice's comment: %v", err)
	}
	assertCascadeEvents(t, ctx, "Alice's comments after Bob's delete", aliceComments, []string{"deleted " + onBobPost.String()})
	assertCascadeEvents(t, ctx, "Bob's posts after his delete", bobPosts, []string{"deleted " + bobPost.String()})
	assertCascadeEvents(t, ctx, "Alice's posts after Bob's delete", alicePosts, []string{"deleted " + bobPost.String()})

	if _, err := alice.Users.Delete(ctx, social.Users.ByID.Value(aliceID)); err != nil {
		t.Fatalf("Alice deletes her account: %v", err)
	}
	assertCascadeEvents(t, ctx, "Alice's posts after her account delete", alicePosts, []string{"deleted " + alicePost.String()})
	assertCascadeEvents(t, ctx, "Alice's comments after her account delete", aliceComments, []string{"deleted " + onAlicePost.String(), "deleted " + reply.String(), "deleted " + bobOnAlicePost.String()})
	if _, err := bob.Posts.Create(ctx, referencePostInput(t, barrier, true)); err != nil {
		t.Fatal(err)
	}
	assertCascadeEvents(t, ctx, "Bob's posts after Alice's account delete", bobPosts, []string{"created " + barrier.String()})
	for _, model := range []string{"posts", "comments"} {
		var remaining int
		if err := database.UnsafeSQLX().GetContext(ctx, &remaining, `SELECT COUNT(*) FROM `+model); err != nil || remaining != 1 && model == "posts" || remaining != 0 && model == "comments" {
			t.Fatalf("%s remaining=%d err=%v", model, remaining, err)
		}
	}
}

func assertCascadeEvents[S interface {
	Recv(context.Context) (E, error)
}, E interface {
	ID() golem.UUID
	Metadata() golem.EventMetadata
}](t *testing.T, ctx context.Context, name string, stream S, want []string) {
	t.Helper()
	var got []string
	for range want {
		receive, cancel := context.WithTimeout(ctx, 5*time.Second)
		event, err := stream.Recv(receive)
		cancel()
		if err != nil {
			t.Fatalf("%s: receive after %v: %v", name, got, err)
		}
		action := "updated"
		switch event.Metadata().Action() {
		case golem.EventCreated:
			action = "created"
		case golem.EventDeleted:
			action = "deleted"
		}
		got = append(got, action+" "+event.ID().String())
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: events=%v want=%v", name, got, want)
	}
}
