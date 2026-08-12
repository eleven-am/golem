package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
)

func TestSQLiteBackupCheckpointIncludesCommittedWALStateAndRefusesBusyReaders(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "backup_source.sqlite")
	dataSourceName := "file:" + path

	database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	pool := database.UnsafeSQLX()
	if _, err := pool.ExecContext(ctx, `CREATE TABLE ledger (id INTEGER PRIMARY KEY, note TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 64; id++ {
		if _, err := pool.ExecContext(ctx, `INSERT INTO ledger (id, note) VALUES (?, ?)`, id, "committed"); err != nil {
			t.Fatal(err)
		}
	}

	holder, err := ncrucesdriver.Open(dataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	reader, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		t.Fatal(err)
	}
	var observed int
	if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM ledger`).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if observed != 64 {
		t.Fatalf("busy reader observed %d rows, want 64", observed)
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

	assertMaintenanceRefusal(t, providersqlite.CheckpointForBackup(ctx, database), "open provider handle")
	if sidecar, err := os.Stat(path + "-wal"); err != nil || sidecar.Size() == 0 {
		t.Fatalf("the open-handle refusal changed sidecar state: size=%v err=%v", sidecar, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	busyContext, cancelBusy := context.WithTimeout(ctx, 750*time.Millisecond)
	started := time.Now()
	assertMaintenanceRefusal(t, providersqlite.CheckpointForBackup(busyContext, database), "busy reader")
	cancelBusy()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("busy checkpoint exceeded its context bound: %s", elapsed)
	}
	if sidecar, err := os.Stat(path + "-wal"); err != nil || sidecar.Size() == 0 {
		t.Fatalf("the busy-reader refusal changed sidecar state: size=%v err=%v", sidecar, err)
	}
	if _, err := reader.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}

	if err := providersqlite.CheckpointForBackup(ctx, database); err != nil {
		t.Fatalf("checkpoint after all owners released the database: %v", err)
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

func assertMaintenanceRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("checkpoint accepted %s", reason)
	}
	code, ok := provider.CodeOf(err)
	if !ok || code != provider.CodeMaintenance {
		t.Fatalf("checkpoint refusal code=(%q,%v), want %q", code, ok, provider.CodeMaintenance)
	}
	for _, forbidden := range []string{"file:", "backup_source.sqlite"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("checkpoint refusal disclosed storage identity %q", forbidden)
		}
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
