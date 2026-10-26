package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestFilteredSubscriptionReceivesDeletes(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		runFilteredSubscriptionReceivesDeletes(t, openDeleteEventsSQLite(t))
	})
	t.Run("postgresql", func(t *testing.T) {
		runFilteredSubscriptionReceivesDeletes(t, openDeleteEventsPostgreSQL(t))
	})
}

func openDeleteEventsSQLite(t *testing.T) *provider.Database {
	t.Helper()
	dsn := "file:" + t.TempDir() + "/delete-events.sqlite"
	applyReviewedSQLiteMigration(t, socialHostRoot(t), dsn)
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func openDeleteEventsPostgreSQL(t *testing.T) *provider.Database {
	t.Helper()
	dsn := testenv.DisposablePostgreSQLFrom(t, testenv.PostgreSQLDSN(t, testenv.PostgreSQLDSNVariable))
	applyReviewedPostgreSQLMigration(t, socialHostRoot(t), dsn)
	database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func startDeleteEventsApplication(t *testing.T, ctx context.Context, database *provider.Database) *social.App[social.Principal] {
	t.Helper()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
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
	t.Cleanup(func() {
		stopPublisher()
		<-publisherDone
	})
	awaitPublisher(t, application)
	return application
}

func runFilteredSubscriptionReceivesDeletes(t *testing.T, database *provider.Database) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	application := startDeleteEventsApplication(t, ctx, database)
	aliceID := mustUUID(t, "13000000-0000-0000-0000-000000000001")
	if _, err := application.System().Users.Create(ctx, social.Users.Create(social.Users.ID.Create(aliceID), social.Users.Handle.Create("alice"), social.Users.Email.Create("alice@example.test"))); err != nil {
		t.Fatal(err)
	}
	alice, err := application.ForPrincipal(ctx, social.Principal{Development: true, DevUserID: aliceID})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := alice.Posts.Events(ctx, golem.EventWhere[social.Post](social.Posts.AuthorID.Eq(aliceID)))
	if err != nil {
		t.Fatal(err)
	}
	defer mine.Close()
	postID := mustUUID(t, "23000000-0000-0000-0000-000000000001")
	if _, err := alice.Posts.Create(ctx, referencePostInput(t, postID, false)); err != nil {
		t.Fatal(err)
	}
	assertCascadeEvents(t, ctx, "filtered stream after create", mine, []string{"created " + postID.String()})
	if _, err := alice.Posts.Delete(ctx, social.Posts.ByID.Value(postID)); err != nil {
		t.Fatal(err)
	}
	assertCascadeEvents(t, ctx, "filtered stream after delete", mine, []string{"deleted " + postID.String()})
}

func TestGraphQLHiddenModelHasGoEvents(t *testing.T) {
	sdl, err := os.ReadFile(filepath.Join(socialHostRoot(t), "social", "zz_golem_graphql.schema.graphqls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sdl), "Session") {
		t.Fatal("the GraphQL schema exposes the hidden Session model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	application := startDeleteEventsApplication(t, ctx, openDeleteEventsSQLite(t))
	aliceID := mustUUID(t, "14000000-0000-0000-0000-000000000001")
	bobID := mustUUID(t, "14000000-0000-0000-0000-000000000002")
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
	sessions, err := alice.Sessions.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	bobSession := mustUUID(t, "54000000-0000-0000-0000-000000000002")
	aliceSession := mustUUID(t, "54000000-0000-0000-0000-000000000001")
	for _, session := range []struct{ id, user golem.UUID }{{bobSession, bobID}, {aliceSession, aliceID}} {
		if _, err := system.Sessions.Create(ctx, social.Sessions.Create(
			social.Sessions.ID.Create(session.id), social.Sessions.UserID.Create(session.user),
			social.Sessions.TokenHash.Create([]byte(session.id.String())),
			social.Sessions.ExpiresAt.Create(time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)),
		)); err != nil {
			t.Fatal(err)
		}
	}
	assertCascadeEvents(t, ctx, "Alice's sessions after create", sessions, []string{"created " + aliceSession.String()})
	if _, err := alice.Sessions.Delete(ctx, social.Sessions.ByID.Value(aliceSession)); err != nil {
		t.Fatal(err)
	}
	assertCascadeEvents(t, ctx, "Alice's sessions after delete", sessions, []string{"deleted " + aliceSession.String()})
}
