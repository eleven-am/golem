// Package sqlite opens the verified SQLite database profile required by Golem.
package sqlite

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/provider"
)

type Config struct {
	DataSourceName string
}

// Open creates a verified SQLite handle and registers every deterministic SQL
// function required by Golem-owned schema objects. Every connection that writes
// a Golem database must be opened through this package, including connections
// in another service and raw SQL writers.
func Open(ctx context.Context, config Config) (*provider.Database, error) {
	database, err := providerhandle.OpenSQLite(ctx, config.DataSourceName)
	if err != nil {
		return nil, err
	}
	return (*provider.Database)(database), nil
}

// OpenNetworkFilesystem creates a verified SQLite handle for a database placed
// deliberately on a network filesystem. It enforces rollback journaling and a
// single connection; Open remains the WAL-backed local-filesystem default.
func OpenNetworkFilesystem(ctx context.Context, config Config) (*provider.Database, error) {
	database, err := providerhandle.OpenSQLiteWithDurability(ctx, config.DataSourceName, internalsqlite.DurabilityNetworkFilesystem)
	if err != nil {
		return nil, err
	}
	return (*provider.Database)(database), nil
}

func OpenNetworkFilesystemWAL(ctx context.Context, config Config) (*provider.Database, error) {
	database, err := providerhandle.OpenSQLiteWithDurability(ctx, config.DataSourceName, internalsqlite.DurabilityNetworkFilesystemWAL)
	if err != nil {
		return nil, err
	}
	return (*provider.Database)(database), nil
}

// CheckpointForBackup prepares a verified SQLite database for an offline copy.
// It checkpoints WAL databases and verifies rollback-journal databases. The
// supplied handle must already be closed, and all other application readers
// and writers must have stopped. Busy ownership fails closed; this operation
// never deletes or truncates SQLite sidecar files directly.
func CheckpointForBackup(ctx context.Context, database *provider.Database) error {
	return providerhandle.CheckpointSQLiteForBackup(ctx, (*providerhandle.Database)(database))
}

// Snapshot copies a consistent database image to a new destination file in an
// existing, trusted local directory. The source must be an open verified local
// SQLite handle. It remains usable; the copy occupies one pooled connection.
// Network-filesystem profiles are not supported by this operation.
//
// Before publication, the private copy must pass integrity, foreign-key,
// physical-schema and exact reviewed-ledger verification against expected.
// SQLite normalizes only that private copy to DELETE journal mode. Verification
// uses a query-only connection; it requires filesystem write access for SQLite
// to validate CHECK constraints, but prohibits ordinary SQL writes.
// No existing destination, including an empty file or symlink, is overwritten.
// The destination and its -wal, -shm and -journal names must all be unused.
// Callers must exclude concurrent writers to that destination namespace;
// sidecar checks and publication are not one atomic filesystem operation.
// Cancellation before publication removes private staging. After publication,
// a finalization failure has provider.CodeSnapshotPublished and the destination
// remains present. Callers must distinguish that outcome before retrying.
//
// Snapshot does not restore a database, reconcile jobs or pending events, or
// change versions or client identity. It does not establish restore safety.
func Snapshot(ctx context.Context, database *provider.Database, expected golem.SchemaBundle, destination string) error {
	return providerhandle.SnapshotSQLite(ctx, (*providerhandle.Database)(database), expected, destination)
}
