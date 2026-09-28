package handle

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteCloseRefreshesPlannerStatisticsAfterIngestion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "planner.db")
	database, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	raw := database.UnsafeSQLX()
	for _, statement := range []string{
		`CREATE TABLE messages(id INTEGER PRIMARY KEY,mailbox TEXT NOT NULL,uid INTEGER NOT NULL) STRICT`,
		`CREATE INDEX idx_messages_mailbox ON messages(mailbox)`,
		`CREATE INDEX idx_messages_uid ON messages(uid)`,
		`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<20000) INSERT INTO messages SELECT n,printf('m%d',n%10),n FROM seq`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	raw = reopened.UnsafeSQLX()
	var statistic string
	if err := raw.GetContext(ctx, &statistic, `SELECT stat FROM sqlite_stat1 WHERE tbl='messages' AND idx='idx_messages_uid'`); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statistic, "20000 ") {
		t.Fatalf("uid statistics=%q", statistic)
	}
	rows, err := raw.QueryxContext(ctx, `EXPLAIN QUERY PLAN SELECT id FROM messages WHERE mailbox='m3' AND uid<200`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(details, "\n"), "idx_messages_uid") {
		t.Fatalf("selective range did not use uid index: %v", details)
	}
}
