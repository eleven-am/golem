package handle

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func withSQLiteStatisticsSchedule(t *testing.T, interval time.Duration, refresh func(context.Context, *sqlx.DB) error) {
	t.Helper()
	previousInterval, previousRefresh := sqliteStatisticsInterval, sqliteStatisticsRefresh
	sqliteStatisticsInterval = interval
	if refresh != nil {
		sqliteStatisticsRefresh = refresh
	}
	t.Cleanup(func() {
		sqliteStatisticsInterval, sqliteStatisticsRefresh = previousInterval, previousRefresh
	})
}

func TestSQLiteStatisticsAreRefreshedWhileTheHandleIsOpen(t *testing.T) {
	withSQLiteStatisticsSchedule(t, 20*time.Millisecond, nil)
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "statistics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	raw := database.UnsafeSQLX()
	for _, statement := range []string{
		`CREATE TABLE posts(id INTEGER PRIMARY KEY, author_id INTEGER NOT NULL, published INTEGER NOT NULL) STRICT`,
		`CREATE INDEX posts_author ON posts(author_id)`,
		`INSERT INTO posts(author_id, published) SELECT value % 2, value % 2 FROM generate_series(1, 10)`,
		`ANALYZE`,
		`INSERT INTO posts(author_id, published) SELECT value % 500, value % 2 FROM generate_series(1, 20000)`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var stat string
		if err := raw.GetContext(ctx, &stat, `SELECT stat FROM sqlite_stat1 WHERE tbl='posts' AND idx='posts_author'`); err != nil {
			t.Fatal(err)
		}
		var rows int
		if _, scanErr := fmt.Sscan(stat, &rows); scanErr == nil && rows >= 20000 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("statistics still describe the table as it was at startup: %q", stat)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSQLiteStatisticsRefreshNeverOverlapsAndStopsAtClose(t *testing.T) {
	var active, overlaps, calls atomic.Int64
	entered := make(chan struct{}, 1)
	withSQLiteStatisticsSchedule(t, time.Millisecond, func(ctx context.Context, _ *sqlx.DB) error {
		calls.Add(1)
		if active.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer active.Add(-1)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		time.Sleep(50 * time.Millisecond)
		return nil
	})
	database, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "overlap.db"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		_ = database.Close()
		t.Fatal("the periodic refresh never ran")
	}
	closed := make(chan error, 1)
	go func() { closed <- database.Close() }()
	select {
	case <-closed:
	case <-time.After(sqliteStatisticsTimeout + 10*time.Second):
		t.Fatal("close did not stop the periodic refresh")
	}
	after := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != after {
		t.Fatalf("the periodic refresh ran after close returned: %d then %d", after, calls.Load())
	}
	if overlaps.Load() != 0 {
		t.Fatalf("statistics refresh ran concurrently with itself %d times", overlaps.Load())
	}
	if after < 2 {
		t.Fatalf("close did not refresh statistics after stopping the schedule: %d calls", after)
	}
}

func BenchmarkSQLiteRelationPolicyAfterGrowth(b *testing.B) {
	for _, users := range []int{100, 800} {
		b.Run(fmt.Sprintf("users=%d", users), func(b *testing.B) {
			previous := sqliteStatisticsInterval
			sqliteStatisticsInterval = 20 * time.Millisecond
			b.Cleanup(func() { sqliteStatisticsInterval = previous })
			ctx := context.Background()
			database, err := OpenSQLite(ctx, filepath.Join(b.TempDir(), "growth.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { database.Close() })
			raw := database.UnsafeSQLX()
			for _, statement := range []string{
				`CREATE TABLE users(id TEXT NOT NULL PRIMARY KEY, email TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL) STRICT`,
				`CREATE TABLE posts(id TEXT NOT NULL PRIMARY KEY, author_id TEXT NOT NULL REFERENCES users(id), published INTEGER NOT NULL, created_at INTEGER NOT NULL) STRICT`,
				`CREATE INDEX posts_published_created ON posts(published, created_at, id)`,
				`CREATE INDEX posts_author_created ON posts(author_id, created_at, id)`,
				`CREATE INDEX users_created ON users(created_at, id)`,
				`INSERT INTO users SELECT printf('user-%06d', value), printf('user-%06d@x', value), value FROM generate_series(0, 1)`,
				`INSERT INTO posts SELECT printf('post-%06d', value), printf('user-%06d', value % 2), value % 2, value FROM generate_series(0, 9)`,
				`ANALYZE`,
				`INSERT INTO users SELECT printf('user-%06d', value), printf('user-%06d@x', value), value FROM generate_series(2, ?1 - 1)`,
				`INSERT INTO posts SELECT printf('post-%06d', value), printf('user-%06d', 2 + value % (?1 - 2)), value % 2, value FROM generate_series(10, 10 + (?1 - 2) * 250 - 1)`,
			} {
				var arguments []any
				if strings.Contains(statement, "?1") {
					arguments = []any{users}
				}
				if _, err := raw.ExecContext(ctx, statement, arguments...); err != nil {
					b.Fatalf("%s: %v", statement, err)
				}
			}
			time.Sleep(500 * time.Millisecond)
			query := `SELECT "golem_r0"."id" FROM "users" AS "golem_r0" WHERE EXISTS (SELECT 1 FROM "posts" AS "golem_p1" WHERE "golem_p1"."author_id" = "golem_r0"."id" AND "golem_p1"."published" = ?) ORDER BY "golem_r0"."created_at" DESC, "golem_r0"."id" DESC LIMIT 10`
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				var ids []string
				if err := raw.SelectContext(ctx, &ids, query, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
