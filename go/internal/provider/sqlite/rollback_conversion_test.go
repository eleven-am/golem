package sqlite

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRollbackConversionDataSourceFileLocation(t *testing.T) {
	for dataSourceName, want := range map[string]string{
		"plain.db":                          "plain.db",
		"file:relative.db?mode=rwc":         "relative.db",
		"file:/absolute/space%20name.db":    "/absolute/space name.db",
		"file:///absolute/triple.db":        "/absolute/triple.db",
		"file://localhost/absolute/host.db": "/absolute/host.db",
	} {
		location, fileBacked, err := dataSourceFileLocation(dataSourceName)
		if err != nil || !fileBacked || location != want {
			t.Fatalf("%q location=%q fileBacked=%v err=%v, want %q", dataSourceName, location, fileBacked, err, want)
		}
	}
	for _, dataSourceName := range []string{":memory:", "file::memory:", "file:shared?mode=memory&cache=shared", "file:shared?MODE=Memory"} {
		if _, fileBacked, err := dataSourceFileLocation(dataSourceName); err != nil || fileBacked {
			t.Fatalf("%q fileBacked=%v err=%v, want an in-memory database", dataSourceName, fileBacked, err)
		}
	}
	if _, _, err := dataSourceFileLocation("file://remote/absolute.db"); err == nil {
		t.Fatal("remote authority accepted")
	}
}

func TestRollbackConversionHeaderDetection(t *testing.T) {
	directory := t.TempDir()
	if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(filepath.Join(directory, "missing.db")); err != nil || writeAheadLog {
		t.Fatalf("missing file writeAheadLog=%v err=%v", writeAheadLog, err)
	}
	short := filepath.Join(directory, "short.db")
	if err := os.WriteFile(short, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(short); err != nil || writeAheadLog {
		t.Fatalf("short file writeAheadLog=%v err=%v", writeAheadLog, err)
	}
	foreign := filepath.Join(directory, "foreign.db")
	header := make([]byte, databaseHeaderLength)
	header[databaseHeaderWriteVersion], header[databaseHeaderReadVersion] = 2, 2
	if err := os.WriteFile(foreign, header, 0o600); err != nil {
		t.Fatal(err)
	}
	if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(foreign); err != nil || writeAheadLog {
		t.Fatalf("non-SQLite file writeAheadLog=%v err=%v", writeAheadLog, err)
	}
	ctx := context.Background()
	walPath := filepath.Join(directory, "wal.db")
	database, _, err := New().Open(ctx, "file:"+walPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "CREATE TABLE durable (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(walPath); err != nil || !writeAheadLog {
		t.Fatalf("WAL database writeAheadLog=%v err=%v", writeAheadLog, err)
	}
	rollbackPath := filepath.Join(directory, "rollback.db")
	database, _, err = New().OpenWithDurability(ctx, "file:"+rollbackPath, DurabilityNetworkFilesystem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "CREATE TABLE durable (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(rollbackPath); err != nil || writeAheadLog {
		t.Fatalf("rollback database writeAheadLog=%v err=%v", writeAheadLog, err)
	}
}

func TestRollbackConversionHealsWriteAheadLogLeftWithUncheckpointedFrames(t *testing.T) {
	ctx := context.Background()
	path := strandedWriteAheadLogFixture(t)
	if err := os.WriteFile(path+"-shm", nil, 0o400); err != nil {
		t.Fatal(err)
	}
	database, _, err := New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystem)
	if err != nil {
		t.Fatalf("rollback profile refused a stranded write-ahead log database: %v", err)
	}
	defer database.Close()
	assertRollbackConversionResult(t, database.GetContext, database.SelectContext, path)
	if info, err := os.Stat(path + "-shm"); err != nil || info.Size() != 0 {
		t.Fatalf("empty shared-memory leftover changed: info=%v err=%v", info, err)
	}
}

func TestRollbackConversionRefusesNonEmptySharedMemoryIndexWithoutChangingFiles(t *testing.T) {
	ctx := context.Background()
	path := strandedWriteAheadLogFixture(t)
	if err := os.WriteFile(path+"-shm", []byte("live shared memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotDatabaseFiles(t, path)
	database, _, err := New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystem)
	if database != nil {
		_ = database.Close()
		t.Fatal("non-empty shared-memory index was accepted")
	}
	if err == nil || !strings.Contains(err.Error(), "shared-memory index") || !strings.Contains(err.Error(), "non-empty") || !strings.Contains(err.Error(), "rollback-journal") {
		t.Fatalf("error=%v, want a non-empty shared-memory refusal from the rollback conversion", err)
	}
	assertDatabaseFilesUnchanged(t, path, before)
}

func TestRollbackConversionFailsClosedWhileAnotherHandleHoldsTheDatabase(t *testing.T) {
	ctx := context.Background()
	path := strandedWriteAheadLogFixture(t)
	holder, _, err := New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystemWAL)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := holder.GetContext(ctx, &count, "SELECT count(*) FROM durable"); err != nil || count != 6 {
		t.Fatalf("holder count=%d err=%v", count, err)
	}
	before := snapshotDatabaseFiles(t, path)
	attemptContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	database, _, err := New().OpenWithDurability(attemptContext, "file:"+path, DurabilityNetworkFilesystem)
	cancel()
	if database != nil {
		_ = database.Close()
		t.Fatal("rollback profile converted a database held exclusively by another handle")
	}
	if err == nil || !strings.Contains(err.Error(), "convert write-ahead logging database") {
		t.Fatalf("error=%v, want a failed rollback-journal conversion", err)
	}
	assertDatabaseFilesUnchanged(t, path, before)
	if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(path); err != nil || !writeAheadLog {
		t.Fatalf("header writeAheadLog=%v err=%v after failed conversion", writeAheadLog, err)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	database, _, err = New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystem)
	if err != nil {
		t.Fatalf("conversion after the holder closed: %v", err)
	}
	defer database.Close()
	assertRollbackConversionResult(t, database.GetContext, database.SelectContext, path)
}

func TestRollbackConversionLeavesWriteAheadLogProfilesUnchanged(t *testing.T) {
	ctx := context.Background()
	for _, durability := range []DurabilityProfile{DurabilityWAL, DurabilityNetworkFilesystemWAL} {
		path := strandedWriteAheadLogFixture(t)
		database, _, err := New().OpenWithDurability(ctx, "file:"+path, durability)
		if err != nil {
			t.Fatal(err)
		}
		var journal string
		if err := database.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil || normalizeJournalMode(journal) != journalModeWAL {
			t.Fatalf("profile %d journal=%q err=%v, want wal", durability, journal, err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(path); err != nil || !writeAheadLog {
			t.Fatalf("profile %d header writeAheadLog=%v err=%v", durability, writeAheadLog, err)
		}
	}
}

func strandedWriteAheadLogFixture(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.db")
	database, _, err := New().Open(ctx, "file:"+source)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, "CREATE TABLE durable (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"checkpointed-1", "checkpointed-2", "checkpointed-3"} {
		if _, err := database.ExecContext(ctx, "INSERT INTO durable(value) VALUES (?)", value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"wal-only-1", "wal-only-2", "wal-only-3"} {
		if _, err := database.ExecContext(ctx, "INSERT INTO durable(value) VALUES (?)", value); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(source + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("write-ahead log holds no frames: info=%v err=%v", info, err)
	}
	path := filepath.Join(t.TempDir(), "stranded.db")
	for _, suffix := range []string{"", "-wal"} {
		contents, err := os.ReadFile(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func assertRollbackConversionResult(t *testing.T, get func(context.Context, any, string, ...any) error, selectRows func(context.Context, any, string, ...any) error, path string) {
	t.Helper()
	ctx := context.Background()
	var journal, integrity string
	if err := get(ctx, &journal, "PRAGMA journal_mode"); err != nil || normalizeJournalMode(journal) != journalModeDelete {
		t.Fatalf("journal=%q err=%v, want delete", journal, err)
	}
	if err := get(ctx, &integrity, "PRAGMA integrity_check"); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	var values []string
	if err := selectRows(ctx, &values, "SELECT value FROM durable ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(values, ",") != "checkpointed-1,checkpointed-2,checkpointed-3,wal-only-1,wal-only-2,wal-only-3" {
		t.Fatalf("values=%v, want checkpointed and write-ahead-only rows", values)
	}
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("write-ahead log remains after conversion: %v", err)
	}
	if writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(path); err != nil || writeAheadLog {
		t.Fatalf("header writeAheadLog=%v err=%v after conversion", writeAheadLog, err)
	}
}

func snapshotDatabaseFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	snapshot := map[string][]byte{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		contents, err := os.ReadFile(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		snapshot[suffix] = contents
	}
	return snapshot
}

func assertDatabaseFilesUnchanged(t *testing.T, path string, before map[string][]byte) {
	t.Helper()
	after := snapshotDatabaseFiles(t, path)
	if len(after) != len(before) {
		t.Fatalf("database files changed: before=%d files after=%d files", len(before), len(after))
	}
	for suffix, contents := range before {
		if !bytes.Equal(after[suffix], contents) {
			t.Fatalf("database file %q changed", path+suffix)
		}
	}
}

func TestRollbackConversionSkipsDatabasesAlreadyInRollbackJournalMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rollback.db")
	database, _, err := New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "CREATE TABLE durable (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO durable(value) VALUES ('rollback')"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-shm", []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotDatabaseFiles(t, path)
	database, _, err = New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystem)
	if err != nil {
		t.Fatalf("rollback-journal database took the conversion path: %v", err)
	}
	var value string
	if err := database.GetContext(ctx, &value, "SELECT value FROM durable"); err != nil || value != "rollback" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertDatabaseFilesUnchanged(t, path, before)
}
