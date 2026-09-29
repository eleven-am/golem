package handle

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/ncruces/go-sqlite3/driver"
)

func TestSQLiteCloseLeavesNoFullTextShadowStatistics(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fulltext.db")
	database, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	raw := database.UnsafeSQLX()
	for _, statement := range []string{
		`CREATE TABLE notes(id INTEGER PRIMARY KEY,body TEXT NOT NULL) STRICT`,
		`CREATE VIRTUAL TABLE notes_fts USING fts5(body)`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	for row := range 20 {
		if _, err := raw.ExecContext(ctx, `INSERT INTO notes_fts(rowid,body) VALUES(?,?)`, row+1, fmt.Sprintf("alpha beta %d", row)); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := driver.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inspected := sqlx.NewDb(opened, "sqlite3")
	defer inspected.Close()
	var present int
	if err := inspected.GetContext(ctx, &present, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`); err != nil {
		t.Fatal(err)
	}
	if present == 0 {
		return
	}
	var shadow int
	if err := inspected.GetContext(ctx, &shadow, `SELECT count(*) FROM sqlite_stat1 WHERE tbl IN (SELECT name FROM pragma_table_list WHERE schema='main' AND type='shadow')`); err != nil {
		t.Fatal(err)
	}
	if shadow != 0 {
		t.Fatalf("closing recorded %d statistics rows for full-text storage, which the next open would plan scans from", shadow)
	}
}
