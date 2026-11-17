package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/provider/restoreprobe"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

// Normal bounded public mutations supply both real backup pages and retained
// facts/versions. This fixture adds no unmanaged objects or raw model writes.
func sqliteBackupProgressFixture(t *testing.T) (policyFactRuntimeFixture, string) {
	t.Helper()
	fixture := sqliteRestoreFixture(t)
	for id := byte(1); id <= 96; id++ {
		fixture.mutate(t, golem.EventCreated, id, 1, strings.Repeat("x", 4<<10))
	}
	database := fixture.applicationConfig.Database
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publicsqlite.CheckpointForBackup(fixture.ctx, database); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(fixture.databaseConfig.DataSourceName, "file:")
	database, err := publicsqlite.Open(fixture.ctx, fixture.databaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.UnsafeSQLX().SetMaxOpenConns(1)
	database.UnsafeSQLX().SetMaxIdleConns(1)
	fixture.applicationConfig.Database = database
	var pages, pageSize int
	if err := database.UnsafeSQLX().Get(&pages, "PRAGMA page_count"); err != nil {
		t.Fatal(err)
	}
	if err := database.UnsafeSQLX().Get(&pageSize, "PRAGMA page_size"); err != nil {
		t.Fatal(err)
	}
	if pages <= 64 || pages*pageSize > 2<<20 {
		t.Fatalf("backup fixture pages=%d size=%d must exceed one64-page step and remain <=2MiB", pages, pageSize)
	}
	return fixture, path
}

func sqliteCancelAfterBackupProgress(t *testing.T, operation func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reached := 0
	ctx = restoreprobe.WithHook(ctx, func(boundary string) {
		if boundary == restoreprobe.BackupStepIncomplete {
			reached++
			cancel()
		}
	})
	err := operation(ctx)
	if reached != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("after-progress cancellation: steps=%d error=%v", reached, err)
	}
}

func sqliteRequireEmptyBackupDestination(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled backup left published file or private staging: entries=%v error=%v", entries, err)
	}
}

func sqliteBackupProgressBytes(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Size() > 2<<20 {
		t.Fatal("missing or oversized progress fixture", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSQLitePublicSnapshotCancellationAfterBackupProgress(t *testing.T) {
	fixture, sourcePath := sqliteBackupProgressFixture(t)
	database := fixture.applicationConfig.Database
	beforeBytes := sqliteBackupProgressBytes(t, sourcePath)
	before := sqliteSnapshotRuntimeState(t, database.UnsafeSQLX())
	destinationDirectory := t.TempDir()
	destination := filepath.Join(destinationDirectory, "snapshot.db")
	sqliteCancelAfterBackupProgress(t, func(ctx context.Context) error {
		return publicsqlite.Snapshot(ctx, database, fixture.schema.Bundle, destination)
	})
	sqliteRequireEmptyBackupDestination(t, destinationDirectory)
	if !bytes.Equal(beforeBytes, sqliteBackupProgressBytes(t, sourcePath)) {
		t.Fatal("canceled snapshot changed source main-file bytes")
	}
	// MaxOpenConns(1) makes a leaked borrowed connection fail this bounded probe.
	probe, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var integrity string
	if err := database.UnsafeSQLX().GetContext(probe, &integrity, "PRAGMA integrity_check"); err != nil || integrity != "ok" {
		t.Fatalf("source sole connection/integrity: %q %v", integrity, err)
	}
	if after := sqliteSnapshotRuntimeState(t, database.UnsafeSQLX()); !reflect.DeepEqual(before, after) {
		t.Fatal("canceled snapshot changed source versions/counter/facts")
	}
	cancel()
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelRetry()
	if err := publicsqlite.Snapshot(retryCtx, database, fixture.schema.Bundle, destination); err != nil {
		t.Fatal("same source handle could not retry", err)
	}
	if !bytes.Equal(beforeBytes, sqliteBackupProgressBytes(t, sourcePath)) {
		t.Fatal("snapshot retry changed source main-file bytes")
	}
	snapshot, err := publicsqlite.Open(retryCtx, publicsqlite.Config{DataSourceName: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	config := fixture.applicationConfig
	config.Database = snapshot
	if _, err := Open(retryCtx, config); err != nil {
		t.Fatal("retry snapshot failed actual runtime admission", err)
	}
	if after := sqliteSnapshotRuntimeState(t, snapshot.UnsafeSQLX()); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot retry lost retained evidence")
	}
}

func TestSQLitePublicPrepareRestoreCancellationAfterBackupProgress(t *testing.T) {
	fixture, _ := sqliteBackupProgressFixture(t)
	source := fixture.applicationConfig.Database
	before := sqliteSnapshotRuntimeState(t, source.UnsafeSQLX())
	archive := filepath.Join(t.TempDir(), "archive.db")
	if err := publicsqlite.Snapshot(fixture.ctx, source, fixture.schema.Bundle, archive); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	immutable := sqliteBackupProgressBytes(t, archive)
	destinationDirectory := t.TempDir()
	destination := filepath.Join(destinationDirectory, "restored.db")
	config := publicsqlite.RestoreConfig{Archive: archive, Destination: destination, ArchiveBundle: fixture.schema.Bundle, TargetBundle: fixture.schema.Bundle, Mode: publicsqlite.RestoreResumeAtLeastOnce}
	sqliteCancelAfterBackupProgress(t, func(ctx context.Context) error {
		session, err := publicsqlite.PrepareRestore(ctx, config)
		if session != nil {
			_ = session.Close()
			t.Fatal("canceled restore returned a session")
		}
		return err
	})
	sqliteRequireEmptyBackupDestination(t, destinationDirectory)
	if !bytes.Equal(immutable, sqliteBackupProgressBytes(t, archive)) {
		t.Fatal("canceled restore changed immutable archive")
	}
	// PrepareRestore owns and closes its own one-connection archive pool. Retry
	// exercises a fresh pool; only the Snapshot test asserts reuse of one pool.
	retryCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	retry, err := publicsqlite.PrepareRestore(retryCtx, config)
	if err != nil {
		t.Fatal("restore retry failed", err)
	}
	defer retry.Close()
	var integrity string
	if err := retry.Database().UnsafeSQLX().GetContext(retryCtx, &integrity, "PRAGMA integrity_check"); err != nil || integrity != "ok" {
		t.Fatalf("retry image integrity=%q error=%v", integrity, err)
	}
	if after := sqliteSnapshotRuntimeState(t, retry.Database().UnsafeSQLX()); !reflect.DeepEqual(before, after) {
		t.Fatal("restore retry changed retained evidence")
	}
	if err := retry.Database().Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := retry.Publish(retryCtx)
	if err != nil || !identity.Valid() {
		t.Fatal("restore retry did not publish", err)
	}
	if !bytes.Equal(immutable, sqliteBackupProgressBytes(t, archive)) {
		t.Fatal("restore retry changed immutable archive")
	}
	restored, err := publicsqlite.Open(retryCtx, publicsqlite.Config{DataSourceName: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	applicationConfig := fixture.applicationConfig
	applicationConfig.Database = restored
	app, err := Open(retryCtx, applicationConfig)
	if err != nil || app.DataIncarnation() != identity {
		t.Fatal("restored image failed runtime/identity admission", err)
	}
}
