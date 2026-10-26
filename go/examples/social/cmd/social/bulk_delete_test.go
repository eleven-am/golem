package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func TestBulkDeleteOverflowsOutbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dsn := "file:" + t.TempDir() + "/bulk-delete.sqlite"
	applyReviewedSQLiteMigration(t, socialHostRoot(t), dsn)
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 128})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(ctx, database, transport)
	if err != nil {
		t.Fatal(err)
	}
	system := application.System()
	authorID := mustUUID(t, "13000000-0000-0000-0000-000000000001")
	if _, err := system.Users.Create(ctx, social.Users.Create(social.Users.ID.Create(authorID), social.Users.Handle.Create("bulk"), social.Users.Email.Create("bulk@example.test"))); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("b", 40*1024)
	const rows = 30
	for index := 1; index <= rows; index++ {
		id := mustUUID(t, fmt.Sprintf("23000000-0000-0000-0000-%012d", index))
		input := social.Posts.Create(
			social.Posts.ID.Create(id), social.Posts.AuthorID.Create(authorID),
			social.Posts.Title.Create("bulk"), social.Posts.Body.Create(body),
			social.Posts.LiveDate.Create(mustDate(t, "2026-08-09")), social.Posts.LiveTime.Create(mustTime(t, "13:14:15")),
			social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)),
			social.Posts.Topics.Create(golem.List[string]{"bulk"}),
		)
		if _, err := system.Posts.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	count, err := system.Posts.DeleteMany(ctx, social.Posts.AuthorID.Eq(authorID))
	if err != nil {
		t.Fatalf("bulk delete of %d posts with %d byte bodies failed: %v", rows, len(body), err)
	}
	if count != rows {
		t.Fatalf("bulk delete count=%d want %d", count, rows)
	}
}
