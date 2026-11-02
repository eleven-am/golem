package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func TestSQLiteNetworkFilesystemConvertsStrandedWriteAheadLogDatabase(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.sqlite")
	writer, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:" + source})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	pool := writer.UnsafeSQLX()
	if _, err := pool.ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO durable(value) VALUES ('checkpointed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO durable(value) VALUES ('wal-only')`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stranded.sqlite")
	for _, suffix := range []string{"", "-wal"} {
		contents, err := os.ReadFile(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(contents) == 0 {
			t.Fatalf("source file %q is empty", source+suffix)
		}
		if err := os.WriteFile(path+suffix, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path+"-shm", nil, 0o400); err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: "file:" + path})
	if err != nil {
		t.Fatalf("network-filesystem profile refused a stranded write-ahead log database: %v", err)
	}
	defer database.Close()
	assertConvertedRollbackDatabase(t, database, path, "checkpointed,wal-only")
	if info, err := os.Stat(path + "-shm"); err != nil || info.Size() != 0 {
		t.Fatalf("empty shared-memory leftover changed: info=%v err=%v", info, err)
	}
}

func TestSQLiteNetworkFilesystemReopensNetworkFilesystemWALWithoutManualConversion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "round-trip.sqlite")
	dsn := "file:" + path
	wal, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wal.UnsafeSQLX().ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := wal.UnsafeSQLX().ExecContext(ctx, `INSERT INTO durable(value) VALUES ('wal')`); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-shm", nil, 0o400); err != nil {
		t.Fatal(err)
	}
	rollback, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatalf("rollback profile after network-filesystem WAL: %v", err)
	}
	assertConvertedRollbackDatabase(t, rollback, path, "wal")
	if _, err := rollback.UnsafeSQLX().ExecContext(ctx, `INSERT INTO durable(value) VALUES ('rollback')`); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + "-shm"); err != nil {
		t.Fatal(err)
	}
	wal, err = sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatalf("network-filesystem WAL after rollback profile: %v", err)
	}
	var values []string
	if err := wal.UnsafeSQLX().SelectContext(ctx, &values, `SELECT value FROM durable ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(values, ",") != "wal,rollback" {
		t.Fatalf("values=%v, want [wal rollback]", values)
	}
}

func TestSQLiteNetworkFilesystemRefusesConversionBesideLiveSharedMemory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "live.sqlite")
	dsn := "file:" + path
	wal, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if _, err := wal.UnsafeSQLX().ExecContext(ctx, `CREATE TABLE durable (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-shm"); err != nil || info.Size() == 0 {
		t.Fatalf("live shared-memory index missing: info=%v err=%v", info, err)
	}
	database, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{DataSourceName: dsn})
	if database != nil {
		_ = database.Close()
		t.Fatal("rollback profile converted a database in live shared-memory WAL use")
	}
	if code, ok := provider.CodeOf(err); !ok || code != provider.CodeOpen {
		t.Fatalf("code=(%q,%v), want OPEN", code, ok)
	}
	var journal string
	if err := wal.UnsafeSQLX().GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil || strings.ToLower(journal) != "wal" {
		t.Fatalf("journal=%q err=%v, want wal", journal, err)
	}
}

func assertConvertedRollbackDatabase(t *testing.T, database *provider.Database, path string, want string) {
	t.Helper()
	ctx := context.Background()
	pool := database.UnsafeSQLX()
	var journal, integrity string
	if err := pool.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil || strings.ToLower(journal) != "delete" {
		t.Fatalf("journal=%q err=%v, want delete", journal, err)
	}
	if err := pool.GetContext(ctx, &integrity, "PRAGMA integrity_check"); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	var values []string
	if err := pool.SelectContext(ctx, &values, `SELECT value FROM durable ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if strings.Join(values, ",") != want {
		t.Fatalf("values=%v, want %s", values, want)
	}
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("write-ahead log remains after conversion: %v", err)
	}
}
