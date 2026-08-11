package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
)

func TestSQLiteBackupCheckpointIncludesCommittedWALStateAndRefusesBusyReaders(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "backup_source.sqlite")
	dataSourceName := "file:" + path

	database, _, err := New().Open(ctx, dataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `CREATE TABLE ledger (id INTEGER PRIMARY KEY, note TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 64; id++ {
		if _, err := database.ExecContext(ctx, `INSERT INTO ledger (id, note) VALUES (?, ?)`, id, "committed"); err != nil {
			t.Fatal(err)
		}
	}

	holder, err := ncrucesdriver.Open(dataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	held, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := held.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	sidecar, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("write-ahead log sidecar is missing: %v", err)
	}
	if sidecar.Size() == 0 {
		t.Fatal("write-ahead log sidecar is empty while committed rows exist")
	}
	if rows := countCopiedLedgerRows(t, copyDatabaseFile(t, path, filepath.Join(directory, "main_only.sqlite"))); rows == 64 {
		t.Fatal("a main-file-only copy already contained every committed row; the sidecar evidence is not being exercised")
	}

	reader, err := database.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var observed int
	if err := reader.GetContext(ctx, &observed, `SELECT count(*) FROM ledger`); err != nil {
		t.Fatal(err)
	}
	if err := New().CheckpointForBackup(ctx, dataSourceName); err == nil {
		_ = reader.Rollback()
		t.Fatal("the truncating checkpoint accepted a busy reader")
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if sidecar, err := os.Stat(path + "-wal"); err != nil || sidecar.Size() == 0 {
		t.Fatalf("the refused checkpoint changed sidecar state: size=%v err=%v", sidecar, err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := New().CheckpointForBackup(ctx, dataSourceName); err != nil {
		t.Fatalf("checkpoint after the provider handle closed: %v", err)
	}
	switch sidecar, err := os.Stat(path + "-wal"); {
	case err == nil && sidecar.Size() != 0:
		t.Fatalf("the truncating checkpoint left %d bytes of write-ahead state", sidecar.Size())
	case err != nil && !os.IsNotExist(err):
		t.Fatalf("write-ahead sidecar state after checkpoint: %v", err)
	}
	if rows := countCopiedLedgerRows(t, copyDatabaseFile(t, path, filepath.Join(directory, "checkpointed.sqlite"))); rows != 64 {
		t.Fatalf("the checkpointed copy contains %d rows, want 64", rows)
	}
}

func copyDatabaseFile(t *testing.T, source, destination string) string {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return destination
}

func countCopiedLedgerRows(t *testing.T, path string) int {
	t.Helper()
	standard, err := ncrucesdriver.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	copied := sqlx.NewDb(standard, "sqlite3")
	defer copied.Close()
	var rows int
	if err := copied.GetContext(context.Background(), &rows, `SELECT count(*) FROM ledger`); err != nil {
		return 0
	}
	return rows
}
