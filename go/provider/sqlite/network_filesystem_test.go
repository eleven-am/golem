package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/sqlite"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
)

func TestSQLiteNetworkFilesystemProfileUsesVerifiedRollbackJournalAndSingleConnection(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "network.sqlite")
	database, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	pool := database.UnsafeSQLX()
	if got := database.Pool().MaximumOpen(); got != 1 {
		t.Fatalf("maximum open=%d, want 1", got)
	}
	if got := database.Pool().MaximumIdle(); got != 1 {
		t.Fatalf("maximum idle=%d, want 1", got)
	}
	if got := pool.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("database/sql maximum open=%d, want 1", got)
	}
	var journal string
	var synchronous, foreignKeys, busyTimeout int
	for query, target := range map[string]any{"PRAGMA journal_mode": &journal, "PRAGMA synchronous": &synchronous, "PRAGMA foreign_keys": &foreignKeys, "PRAGMA busy_timeout": &busyTimeout} {
		if err := pool.GetContext(ctx, target, query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if strings.ToLower(journal) != "delete" || synchronous != 2 || foreignKeys != 1 || busyTimeout != 5000 {
		t.Fatalf("journal=%q synchronous=%d foreign_keys=%d busy_timeout=%d", journal, synchronous, foreignKeys, busyTimeout)
	}
	if _, err := pool.ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO durable(value) VALUES ('kept')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.CheckpointForBackup(ctx, database); err != nil {
		t.Fatalf("rollback-journal backup preparation: %v", err)
	}
	copyPath := filepath.Join(t.TempDir(), "network-copy.sqlite")
	contents, err := os.ReadFile(strings.TrimPrefix(dsn, "file:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	copyDatabase, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: "file:" + copyPath})
	if err != nil {
		t.Fatal(err)
	}
	var copied int
	if err := copyDatabase.UnsafeSQLX().GetContext(ctx, &copied, `SELECT count(*) FROM durable WHERE value='kept'`); err != nil || copied != 1 {
		t.Fatalf("copied count=%d err=%v", copied, err)
	}
	if err := copyDatabase.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.UnsafeSQLX().GetContext(ctx, &count, `SELECT count(*) FROM durable WHERE value='kept'`); err != nil || count != 1 {
		t.Fatalf("reopened count=%d err=%v", count, err)
	}
}

func TestSQLiteNetworkFilesystemProfileRejectsMemoryAndOverrides(t *testing.T) {
	for _, config := range []sqlite.Config{
		{DataSourceName: "file:network_memory?mode=memory&cache=shared"},
		{DataSourceName: "file:" + filepath.Join(t.TempDir(), "override.sqlite") + "?_pragma=journal_mode(wal)"},
	} {
		database, err := sqlite.OpenNetworkFilesystem(context.Background(), config)
		if database != nil {
			_ = database.Close()
			t.Fatal("invalid network profile configuration opened")
		}
		if err == nil {
			t.Fatal("invalid network profile configuration returned no error")
		}
		if code, ok := provider.CodeOf(err); !ok || code != provider.CodeOpen {
			t.Fatalf("code=(%q,%v), want OPEN", code, ok)
		}
	}
}

func TestSQLiteDefaultStillTransitionsNetworkProfileDatabaseBackToWAL(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "transition.sqlite")
	initial, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.CheckpointForBackup(ctx, initial); err != nil {
		t.Fatal(err)
	}
	network, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	var networkJournal string
	if err := network.UnsafeSQLX().GetContext(ctx, &networkJournal, "PRAGMA journal_mode"); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(networkJournal) != "delete" {
		t.Fatalf("network journal=%q, want delete", networkJournal)
	}
	if err := network.Close(); err != nil {
		t.Fatal(err)
	}
	local, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	var journal string
	if err := local.UnsafeSQLX().GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journal) != "wal" {
		t.Fatalf("default journal=%q, want wal", journal)
	}
}

func TestSQLiteNetworkFilesystemBackupRefusesExternalWriter(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "network-busy.sqlite")
	database, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UnsafeSQLX().ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	external, err := ncrucesdriver.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	writer, err := external.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	busyContext, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	started := time.Now()
	err = sqlite.CheckpointForBackup(busyContext, database)
	cancel()
	if err == nil {
		t.Fatal("backup preparation accepted an active external writer")
	}
	if code, ok := provider.CodeOf(err); !ok || code != provider.CodeMaintenance {
		t.Fatalf("code=(%q,%v), want MAINTENANCE", code, ok)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("busy ownership check exceeded bound: %s", elapsed)
	}
	if _, err := writer.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.CheckpointForBackup(ctx, database); err != nil {
		t.Fatalf("backup preparation after writer release: %v", err)
	}
}
