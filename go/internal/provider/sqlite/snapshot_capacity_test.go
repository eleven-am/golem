package sqlite

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

// This is SQLite page-allocator exhaustion, not physical filesystem ENOSPC.
// It exercises the shared backup component used by snapshot and restore.
func TestSQLiteSnapshotCopyPageCapacityFailureAndRetry(t *testing.T) {
	source, schema, manifest := sqliteSnapshotFixture(t)
	source.SetMaxOpenConns(1)
	ctx := context.Background()
	var pages int
	if err := source.Get(&pages, "PRAGMA page_count"); err != nil || pages <= 2 {
		t.Fatalf("source must exceed limit: %d %v", pages, err)
	}
	destination := filepath.Join(t.TempDir(), "limited.db")
	uri := (&url.URL{Scheme: "file", Path: destination, RawQuery: url.Values{"_pragma": {"max_page_count(2)"}}.Encode()}).String()
	if err := copySQLiteSnapshotURI(ctx, source, uri); !errors.Is(err, sqlite3.FULL) {
		t.Fatalf("copy error=%v, want actual SQLITE_FULL", err)
	}
	var name, integrity string
	if err := source.Get(&name, "SELECT name FROM items WHERE id=1"); err != nil || name != "snapshot alpha" {
		t.Fatalf("source changed: %q %v", name, err)
	}
	if err := source.Get(&integrity, "PRAGMA integrity_check"); err != nil || integrity != "ok" {
		t.Fatalf("source integrity: %q %v", integrity, err)
	}
	if err := New().VerifyRestoreStage(ctx, destination, schema, manifest); err == nil {
		t.Fatal("failed copy admitted as a complete reviewed image")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := copySQLiteSnapshot(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	if err := New().VerifyRestoreStage(ctx, destination, schema, manifest); err != nil {
		t.Fatal(err)
	}
}
