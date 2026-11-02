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

func TestSQLiteNetworkFilesystemWALProfileUsesExclusiveHeapIndexedWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "network-wal.sqlite")
	database, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{DataSourceName: "file:" + path})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
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
	if _, err := pool.ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 50; index++ {
		if _, err := pool.ExecContext(ctx, `INSERT INTO durable(value) VALUES (?)`, strings.Repeat("v", 512)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.GetContext(ctx, &count, `SELECT count(*) FROM durable`); err != nil || count != 50 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var journal, locking string
	var synchronous, foreignKeys, busyTimeout int
	for query, target := range map[string]any{"PRAGMA journal_mode": &journal, "PRAGMA locking_mode": &locking, "PRAGMA synchronous": &synchronous, "PRAGMA foreign_keys": &foreignKeys, "PRAGMA busy_timeout": &busyTimeout} {
		if err := pool.GetContext(ctx, target, query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if strings.ToLower(journal) != "wal" || strings.ToLower(locking) != "exclusive" || synchronous != 2 || foreignKeys != 1 || busyTimeout != 5000 {
		t.Fatalf("journal=%q locking=%q synchronous=%d foreign_keys=%d busy_timeout=%d", journal, locking, synchronous, foreignKeys, busyTimeout)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("write-ahead log was not used: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(path + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("shared-memory index exists after writes and reads: %v", err)
	}
}

func TestSQLiteNetworkFilesystemWALHoldsDatabaseExclusivelyUntilClose(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "exclusive.sqlite")
	dsn := "file:" + path
	database, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UnsafeSQLX().ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for name, open := range map[string]func(context.Context, sqlite.Config) (*provider.Database, error){
		"wal":                    sqlite.Open,
		"network-filesystem":     sqlite.OpenNetworkFilesystem,
		"network-filesystem-wal": sqlite.OpenNetworkFilesystemWAL,
	} {
		attemptContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		second, err := open(attemptContext, sqlite.Config{DataSourceName: dsn})
		cancel()
		if err == nil {
			_ = second.Close()
			t.Fatalf("%s profile opened a database held by network-filesystem WAL", name)
		}
	}
	raw, err := ncrucesdriver.Open(dsn + "?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	var rawCount int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM durable`).Scan(&rawCount); err == nil {
		t.Fatal("raw reader observed a database held by network-filesystem WAL")
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = ncrucesdriver.Open(dsn + "?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM durable`).Scan(&rawCount); err != nil {
		t.Fatalf("raw reader after close: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteNetworkFilesystemWALRefusesLockingOverrideAndLiveSharedMemory(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	stale := filepath.Join(directory, "stale.sqlite")
	if err := os.WriteFile(stale+"-shm", []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, config := range []sqlite.Config{
		{DataSourceName: "file:" + filepath.Join(directory, "override.sqlite") + "?_pragma=locking_mode(normal)"},
		{DataSourceName: "file:network_wal_memory?mode=memory&cache=shared"},
		{DataSourceName: "file:" + stale},
	} {
		database, err := sqlite.OpenNetworkFilesystemWAL(ctx, config)
		if database != nil {
			_ = database.Close()
			t.Fatalf("invalid configuration %q opened", config.DataSourceName)
		}
		if code, ok := provider.CodeOf(err); !ok || code != provider.CodeOpen {
			t.Fatalf("%q code=(%q,%v), want OPEN", config.DataSourceName, code, ok)
		}
	}
	if err := os.Truncate(stale+"-shm", 0); err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{DataSourceName: "file:" + stale})
	if err != nil {
		t.Fatalf("empty shared-memory leftover refused: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteNetworkFilesystemWALTransitionsBetweenRollbackAndBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "transition.sqlite")
	dsn := "file:" + path
	rollback, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rollback.UnsafeSQLX().ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := rollback.UnsafeSQLX().ExecContext(ctx, `INSERT INTO durable(value) VALUES ('rollback')`); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Close(); err != nil {
		t.Fatal(err)
	}

	wal, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	var journal string
	if err := wal.UnsafeSQLX().GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil || strings.ToLower(journal) != "wal" {
		t.Fatalf("journal=%q err=%v, want wal", journal, err)
	}
	if _, err := wal.UnsafeSQLX().ExecContext(ctx, `INSERT INTO durable(value) VALUES ('wal')`); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.CheckpointForBackup(ctx, wal); err != nil {
		t.Fatalf("network-filesystem WAL backup preparation: %v", err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("write-ahead log retained %d bytes after backup preparation", info.Size())
	}
	if _, err := os.Stat(path + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("shared-memory index exists after backup preparation: %v", err)
	}

	restored, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatalf("rollback profile after network-filesystem WAL: %v", err)
	}
	defer restored.Close()
	if err := restored.UnsafeSQLX().GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil || strings.ToLower(journal) != "delete" {
		t.Fatalf("journal=%q err=%v, want delete", journal, err)
	}
	var integrity string
	if err := restored.UnsafeSQLX().GetContext(ctx, &integrity, "PRAGMA integrity_check"); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	var values []string
	if err := restored.UnsafeSQLX().SelectContext(ctx, &values, `SELECT value FROM durable ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if strings.Join(values, ",") != "rollback,wal" {
		t.Fatalf("values=%v, want [rollback wal]", values)
	}
}
