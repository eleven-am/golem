package handle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/eleven-am/golem/go/golem"
	eventoutbox "github.com/eleven-am/golem/go/internal/event/outbox"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/event/schemahistory"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/provider/restoreprobe"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type SQLiteRestoreConfig struct {
	Archive, Destination   string
	ArchiveBundle          golem.SchemaBundle
	TargetBundle           golem.SchemaBundle
	HistoricalEventBundles []golem.SchemaBundle
	Mode                   internalsqlite.RestoreWorkMode
}

// SQLiteRestore owns one private staging namespace. Process shutdown and
// prevention of restart are caller preconditions, just as for offline backup.
type SQLiteRestore struct {
	mu           sync.Mutex
	stage        string
	directory    string
	destination  string
	previous     os.FileInfo
	database     *Database
	target       physical.PhysicalSchema
	manifest     migration.Manifest
	history      *schemahistory.History
	mode         internalsqlite.RestoreWorkMode
	queuePresent bool
	identity     golem.DataIncarnation
	published    bool
	closed       bool
	closeErr     error
	finalErr     error
}

func PrepareSQLiteRestore(ctx context.Context, config SQLiteRestoreConfig) (_ *SQLiteRestore, resultErr error) {
	if ctx == nil || config.Archive == "" || config.Destination == "" || strings.HasPrefix(config.Archive, "file:") || strings.HasPrefix(config.Destination, "file:") || config.Mode != internalsqlite.RestoreResumeAtLeastOnce && config.Mode != internalsqlite.RestoreAbandonAndResync {
		return nil, failure(CodeConfig, "sqlite restore requires file paths, explicit schemas and an explicit work mode")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	archive, err := filepath.Abs(config.Archive)
	if err != nil {
		return nil, failure(CodeConfig, "sqlite restore archive path is invalid")
	}
	destination, err := filepath.Abs(config.Destination)
	if err != nil {
		return nil, failure(CodeConfig, "sqlite restore destination path is invalid")
	}
	sourceInfo, err := restoreFileState(archive, true)
	if err != nil {
		return nil, restoreFailure(ctx, "sqlite restore archive must be a standalone regular file")
	}
	previous, err := restoreFileState(destination, false)
	if err != nil || previous != nil && os.SameFile(sourceInfo, previous) || archive == destination {
		return nil, failure(CodeMaintenance, "sqlite restore destination is not an independently owned offline file namespace")
	}
	if _, err := policyschema.NewHistorical(config.ArchiveBundle); err != nil {
		return nil, failure(CodeMaintenance, "sqlite restore archive schema is invalid or unsupported")
	}
	source, err := migration.BundlePhysicalSchema(config.ArchiveBundle, golem.SQLite)
	if err != nil {
		return nil, failure(CodeMaintenance, "sqlite restore archive physical schema is invalid")
	}
	sourceManifest, err := migration.PrepareReviewedStartup(config.ArchiveBundle, golem.SQLite, source)
	if err != nil {
		return nil, failure(CodeMaintenance, "sqlite restore archive history is invalid")
	}
	registry, err := policyschema.New(config.TargetBundle)
	if err != nil {
		return nil, failure(CodeMaintenance, "sqlite restore target schema is invalid")
	}
	target, err := migration.BundlePhysicalSchema(config.TargetBundle, golem.SQLite)
	if err != nil || !physical.HasDataIncarnation(target.System) {
		return nil, failure(CodeMaintenance, "sqlite restore target requires reviewed durable data identity")
	}
	manifest, err := migration.PrepareReviewedStartup(config.TargetBundle, golem.SQLite, target)
	if err != nil {
		return nil, failure(CodeMaintenance, "sqlite restore target reviewed history is invalid")
	}
	historical := make([]golem.SchemaBundle, 0, len(config.HistoricalEventBundles)+1)
	if config.ArchiveBundle.GenerationDigest() != config.TargetBundle.GenerationDigest() {
		historical = append(historical, config.ArchiveBundle)
	}
	historical = append(historical, config.HistoricalEventBundles...)
	history, err := schemahistory.New(registry, historical)
	if err != nil {
		return nil, failure(CodeMaintenance, "sqlite restore historical event schemas are invalid")
	}
	directory, err := os.MkdirTemp(filepath.Dir(destination), ".golem-restore-*")
	if err != nil {
		return nil, failure(CodeMaintenance, "sqlite restore staging could not be created")
	}
	result := &SQLiteRestore{stage: filepath.Join(directory, "restore.db"), directory: directory, destination: destination, previous: previous, target: target, manifest: manifest, history: history, mode: config.Mode}
	defer func() {
		if resultErr != nil {
			_ = result.Close()
		}
	}()
	engine := internalsqlite.New()
	archiveDB, err := engine.OpenRestoreArchive(ctx, archive)
	if err != nil {
		return nil, restoreFailure(ctx, "sqlite restore archive could not be opened")
	}
	err = engine.Snapshot(ctx, archiveDB, source, sourceManifest, result.stage)
	closeErr := archiveDB.Close()
	after, stateErr := restoreFileState(archive, true)
	if err != nil || closeErr != nil || stateErr != nil || !sameRestoreFile(sourceInfo, after) {
		return nil, restoreFailure(ctx, "sqlite restore archive copy or verification failed")
	}
	staging, err := engine.OpenRestoreStage(ctx, result.stage)
	if err != nil {
		return nil, restoreFailure(ctx, "sqlite restore staging could not be opened")
	}
	err = result.verifyFacts(ctx, staging, source.System, false)
	if err == nil {
		err = engine.ReconcileRestoreWork(ctx, staging, source.System, config.Mode)
	}
	if err == nil {
		result.queuePresent, err = engine.VerifyRestoreQueueStorage(ctx, staging)
	}
	closeErr = staging.Close()
	if err != nil || closeErr != nil {
		if errors.Is(err, internalsqlite.ErrRestoreQueueStorage) {
			return nil, restoreFailure(ctx, "sqlite restore archived queue validation failed")
		}
		return nil, restoreFailure(ctx, "sqlite restore archived facts or durable work are invalid")
	}
	// Normal generated APIs may now perform explicit domain reconciliation.
	// They borrow only this private copy; no serving process is started here.
	result.database, err = OpenSQLite(ctx, result.stage)
	if err != nil {
		return nil, restoreFailure(ctx, "sqlite restore reconciliation handle could not be opened")
	}
	return result, nil
}

func restoreFailure(ctx context.Context, message string) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return failure(CodeMaintenance, message)
}

func restoreFileState(path string, required bool) (os.FileInfo, error) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("restore namespace has an ambiguous sidecar")
		}
	}
	info, err := os.Lstat(path)
	if !required && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("restore file is not regular")
	}
	return info, nil
}

func sameRestoreFile(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && before.Mode() == after.Mode()
}

func (restore *SQLiteRestore) Database() *Database {
	if restore == nil {
		return nil
	}
	restore.mu.Lock()
	defer restore.mu.Unlock()
	if restore.closed || restore.published {
		return nil
	}
	return restore.database
}

func (restore *SQLiteRestore) StagePath() string {
	if restore == nil {
		return ""
	}
	restore.mu.Lock()
	defer restore.mu.Unlock()
	if restore.closed || restore.published {
		return ""
	}
	return restore.stage
}

func (restore *SQLiteRestore) verifyFacts(ctx context.Context, database *sqlx.DB, system physical.SystemSchema, final bool) error {
	return internalsqlite.New().WalkRestoreFacts(ctx, database, system, func(group eventprovider.Lease) error {
		if (final || restore.mode == internalsqlite.RestoreResumeAtLeastOnce) && (group.Delivery.Status == eventprovider.StatusPending || group.Delivery.Status == eventprovider.StatusLeased) {
			_, err := eventoutbox.ValidateDeliveryCausation(group.Delivery.CausationID, group.Facts, restore.history, restore.history, eventoutbox.ValidationLimits{})
			return err
		}
		_, err := eventoutbox.ValidateStoredCausation(group.Delivery.CausationID, group.Facts, restore.history, eventoutbox.ValidationLimits{})
		return err
	})
}

func (restore *SQLiteRestore) Publish(ctx context.Context) (golem.DataIncarnation, error) {
	if restore == nil || ctx == nil {
		return golem.DataIncarnation{}, failure(CodeConfig, "sqlite restore session and context are required")
	}
	restore.mu.Lock()
	defer restore.mu.Unlock()
	if restore.published {
		return restore.identity, failure(CodeRestorePublished, "sqlite restore destination has already been published")
	}
	if restore.closed || restore.database == nil {
		return golem.DataIncarnation{}, failure(CodeMaintenance, "sqlite restore session is closed")
	}
	select {
	case <-restore.database.state.closeDone:
		if restore.database.state.closeErr != nil {
			return golem.DataIncarnation{}, failure(CodeMaintenance, "sqlite restore staging close failed")
		}
	default:
		return golem.DataIncarnation{}, failure(CodeMaintenance, "close all staging application work and database handles before restore publication")
	}
	engine := internalsqlite.New()
	if err := engine.VerifyRestoreStage(ctx, restore.stage, restore.target, restore.manifest); err != nil {
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore final catalog or reviewed history verification failed")
	}
	database, err := engine.OpenRestoreStage(ctx, restore.stage)
	if err != nil {
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore final staging open failed")
	}
	queuePresent, err := engine.VerifyRestoreQueueStorage(ctx, database)
	if err == nil && restore.queuePresent && !queuePresent {
		err = errors.New("archived queue storage disappeared during restore reconciliation")
	}
	if err != nil {
		_ = database.Close()
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore final queue verification failed")
	}
	err = restore.verifyFacts(ctx, database, restore.target.System, true)
	var identity golem.DataIncarnation
	if err == nil {
		identity, err = engine.RotateRestoreIncarnation(ctx, database, restore.target.System)
	}
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore final evidence validation or identity rotation failed")
	}
	if err := engine.VerifyRestoreStage(ctx, restore.stage, restore.target, restore.manifest); err != nil {
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore final image verification failed")
	}
	file, err := os.OpenFile(restore.stage, os.O_RDWR, 0)
	if err != nil {
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore image could not be synchronized")
	}
	err, closeErr = file.Sync(), file.Close()
	if err != nil || closeErr != nil {
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore image synchronization failed")
	}
	directory, err := os.Open(filepath.Dir(restore.destination))
	if err != nil {
		return golem.DataIncarnation{}, restoreFailure(ctx, "sqlite restore destination directory is unavailable")
	}
	defer directory.Close()
	current, err := restoreFileState(restore.destination, false)
	if err != nil || !sameRestoreFile(restore.previous, current) {
		return golem.DataIncarnation{}, failure(CodeMaintenance, "sqlite restore destination changed during preparation")
	}
	if err := ctx.Err(); err != nil {
		return golem.DataIncarnation{}, err
	}
	restoreprobe.Reach(ctx, restoreprobe.BeforePublication)
	return restore.publishVerifiedImage(ctx, directory, identity)
}

// publishVerifiedImage owns the irreversible filesystem boundary after all
// evidence, closed-handle, file synchronization and namespace checks succeed.
func (restore *SQLiteRestore) publishVerifiedImage(ctx context.Context, directory *os.File, identity golem.DataIncarnation) (golem.DataIncarnation, error) {
	var err error
	if restore.previous == nil {
		err = os.Link(restore.stage, restore.destination)
	} else {
		err = os.Rename(restore.stage, restore.destination)
	}
	if err != nil {
		return golem.DataIncarnation{}, failure(CodeMaintenance, "sqlite restore destination publication failed")
	}
	restore.published, restore.identity = true, identity
	restoreprobe.Reach(ctx, restoreprobe.AfterPublication)
	// Publication is irreversible. Finish sync/cleanup even after cancellation,
	// and keep the distinct outcome if durability or cleanup is uncertain.
	syncErr, directoryCloseErr := directory.Sync(), directory.Close()
	if syncErr != nil || directoryCloseErr != nil {
		restore.finalErr = failure(CodeRestorePublished, "sqlite restore was published but directory synchronization failed")
		return identity, restore.finalErr
	}
	if err := os.RemoveAll(restore.directory); err != nil {
		restore.finalErr = failure(CodeRestorePublished, "sqlite restore was published but staging cleanup failed")
		return identity, restore.finalErr
	}
	return identity, nil
}

func (restore *SQLiteRestore) Close() error {
	if restore == nil {
		return nil
	}
	restore.mu.Lock()
	defer restore.mu.Unlock()
	if restore.closed {
		return restore.closeErr
	}
	restore.closed = true
	if restore.database != nil {
		restore.closeErr = restore.database.Close()
	}
	if restore.closeErr == nil && restore.finalErr != nil {
		restore.closeErr = restore.finalErr
	}
	if err := os.RemoveAll(restore.directory); err != nil && restore.closeErr == nil {
		restore.closeErr = failure(CodeMaintenance, "sqlite restore staging cleanup failed")
	}
	return restore.closeErr
}
