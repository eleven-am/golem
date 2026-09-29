package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDriverStillMisreadsTheColumnAfterAnEmptyBlob(t *testing.T) {
	database, _, err := New().Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "empty-blob.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var empty []byte
	var after string
	if err := database.QueryRowContext(context.Background(), `SELECT x'', 'after'`).Scan(&empty, &after); err != nil {
		t.Fatal(err)
	}
	if after == "after" {
		t.Fatal("the SQLite driver now reads the column after an empty BLOB correctly: delete this test and the hex projection in policysql.ProjectColumn")
	}
	var hexed, following string
	if err := database.QueryRowContext(context.Background(), `SELECT CASE WHEN x'' IS NULL THEN NULL ELSE hex(x'') END, 'after'`).Scan(&hexed, &following); err != nil {
		t.Fatal(err)
	}
	if hexed != "" || following != "after" {
		t.Fatalf("hex projection read %q then %q; want the empty text and the following column intact", hexed, following)
	}
}
