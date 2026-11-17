package sqlite

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/restoreprobe"
	"github.com/jmoiron/sqlx"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// ErrSnapshotPublished means the new destination exists, but a subsequent
// durability or staging-cleanup operation failed. It must not be retried as
// though publication had never happened.
var ErrSnapshotPublished = errors.New("sqlite snapshot published before finalization failed")

const snapshotPagesPerStep = 64

// Snapshot owns a new, private artifact through copy, query-only verification,
// and no-clobber publication. The caller has already admitted the verified
// source handle and the expected generated bundle. This is not a restore.
func (provider *Provider) Snapshot(ctx context.Context, source *sqlx.DB, expected physical.PhysicalSchema, manifest migration.Manifest, destination string) (resultErr error) {
	if ctx == nil || source == nil || destination == "" || strings.HasPrefix(destination, "file:") {
		return fmt.Errorf("sqlite snapshot configuration is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err := requireNewSnapshotDestination(destination); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	stageDirectory, err := os.MkdirTemp(parent, ".golem-snapshot-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if err := os.RemoveAll(stageDirectory); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("sqlite snapshot staging cleanup: %w", err)
		}
		if published && resultErr != nil {
			resultErr = fmt.Errorf("%w: %v", ErrSnapshotPublished, resultErr)
		}
	}()
	stage := filepath.Join(stageDirectory, "snapshot.db")
	// The private directory and exclusive file creation prevent the backup
	// engine from opening somebody else's existing file, including an empty one.
	file, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := copySQLiteSnapshot(ctx, source, stage); err != nil {
		return err
	}
	if err := prepareSQLiteSnapshotVerification(ctx, stage); err != nil {
		return err
	}
	if err := provider.verifySnapshot(ctx, stage, expected, manifest); err != nil {
		return err
	}
	file, err = os.OpenFile(stage, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		directory.Close()
		return err
	}
	published, err = publishSQLiteSnapshot(stage, destination, directory)
	closeErr = directory.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func publishSQLiteSnapshot(stage, destination string, directory *os.File) (bool, error) {
	if err := requireNewSnapshotDestination(destination); err != nil {
		return false, err
	}
	// A hard link is an atomic publication that fails if the destination was
	// created concurrently. Rename would overwrite it. Both paths share a
	// parent filesystem; unsupported link semantics fail without a fallback.
	if err := os.Link(stage, destination); err != nil {
		return false, err
	}
	// Publication is the point of no return. Finish synchronization even when
	// cancellation races it; never report ordinary cancellation after success.
	return true, directory.Sync()
}

func requireNewSnapshotDestination(destination string) error {
	// The caller owns this unused namespace in a trusted directory. Recheck
	// before publication, but do not imply these separate filesystem lookups
	// fence a concurrent writer creating sidecars after the final check.
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(destination + suffix); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return fmt.Errorf("sqlite snapshot destination namespace is not new")
		}
	}
	return nil
}

func copySQLiteSnapshot(ctx context.Context, source *sqlx.DB, destination string) (resultErr error) {
	return copySQLiteSnapshotURI(ctx, source, (&url.URL{Scheme: "file", Path: destination}).String())
}

// copySQLiteSnapshotURI owns the incremental backup and its connection lifetime.
func copySQLiteSnapshotURI(ctx context.Context, source *sqlx.DB, uri string) (resultErr error) {
	connection, err := source.Connx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := connection.Close(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	return connection.Raw(func(raw any) (resultErr error) {
		native := raw.(sqlitedriver.Conn).Raw()
		if native.Filename("main").String() == "" || !native.GetAutocommit() {
			return fmt.Errorf("sqlite snapshot requires an idle file-backed source connection")
		}
		previous := native.SetInterrupt(ctx)
		defer native.SetInterrupt(previous)
		backup, err := native.BackupInit("main", uri)
		if err != nil {
			return err
		}
		defer func() {
			if err := backup.Close(); err != nil && resultErr == nil {
				resultErr = err
			}
		}()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			done, err := backup.Step(snapshotPagesPerStep)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			if done {
				return nil
			}
			restoreprobe.Reach(ctx, restoreprobe.BackupStepIncomplete)
		}
	})
}

// prepareSQLiteSnapshotVerification changes only the completed, privately
// owned staging copy. SQLite closes/checkpoints its journals itself; no sidecar
// is removed directly. DELETE mode makes the published main file standalone.
func prepareSQLiteSnapshotVerification(ctx context.Context, path string) (resultErr error) {
	uri := &url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"mode": {"rw"}}.Encode()}
	standard, err := sqlitedriver.Open(uri.String())
	if err != nil {
		return err
	}
	database := sqlx.NewDb(standard, "sqlite3")
	database.SetMaxOpenConns(1)
	defer func() {
		if err := database.Close(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	var mode string
	if err := database.GetContext(ctx, &mode, "PRAGMA journal_mode=DELETE"); err != nil {
		return err
	}
	if mode != "delete" {
		return fmt.Errorf("sqlite snapshot journal preparation failed")
	}
	return nil
}

func openSQLiteSnapshotVerification(path string) (*sqlx.DB, error) {
	// The SQLite engine skips CHECK validation in integrity_check on physically
	// read-only handles. Use an RW handle with SQL writes prohibited instead.
	// The owned artifact has already been normalized to DELETE journal mode.
	uri := &url.URL{Scheme: "file", Path: path}
	uri.RawQuery = url.Values{"mode": {"rw"}, "_pragma": {"foreign_keys(1)", "query_only(1)"}}.Encode()
	standard, err := sqlitedriver.Open(uri.String())
	if err != nil {
		return nil, err
	}
	database := sqlx.NewDb(standard, "sqlite3")
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	return database, nil
}

func (provider *Provider) verifySnapshot(ctx context.Context, path string, expected physical.PhysicalSchema, manifest migration.Manifest) (resultErr error) {
	// Only completed artifacts reach this query-only verifier. Their main file
	// must not depend on a journal; never use this to inspect a live database.
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Stat(path + suffix)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Size() != 0 {
			return fmt.Errorf("sqlite snapshot is not a completed standalone artifact")
		}
	}
	database, err := openSQLiteSnapshotVerification(path)
	if err != nil {
		return err
	}
	defer func() {
		if err := database.Close(); err != nil && resultErr == nil {
			resultErr = err
		}
		if resultErr == nil {
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
					resultErr = fmt.Errorf("sqlite snapshot verification left a sidecar")
					break
				}
			}
		}
	}()
	actual, err := provider.introspectCatalog(ctx, database, expected)
	if err != nil {
		return err
	}
	if err := physical.CompareFingerprints(expected, actual); err != nil {
		return err
	}
	name, err := sqliteLedgerName(expected.System)
	if err != nil {
		return err
	}
	ledger, err := readLedgerNamed(ctx, database, name)
	if err != nil {
		return err
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		return err
	}
	if physical.HasDataIncarnation(expected.System) {
		if _, err := provider.ReadDataIncarnation(ctx, database, expected.System); err != nil {
			return fmt.Errorf("sqlite snapshot data incarnation is invalid")
		}
	}
	rows, err := database.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			rows.Close()
			return err
		}
		count++
		if outcome != "ok" || count != 1 {
			rows.Close()
			return fmt.Errorf("sqlite snapshot integrity check failed")
		}
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if count != 1 {
		return fmt.Errorf("sqlite snapshot integrity check was incomplete")
	}
	rows, err = database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	if rows.Next() {
		rows.Close()
		return fmt.Errorf("sqlite snapshot foreign key check failed")
	}
	readErr = rows.Err()
	closeErr = rows.Close()
	if readErr != nil {
		return readErr
	}
	return closeErr
}
