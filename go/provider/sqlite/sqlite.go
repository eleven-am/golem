// Package sqlite opens the verified SQLite database profile required by Golem.
package sqlite

import (
	"context"

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

// CheckpointForBackup prepares a verified SQLite database for an offline copy.
// It checkpoints WAL databases and verifies rollback-journal databases. The
// supplied handle must already be closed, and all other application readers
// and writers must have stopped. Busy ownership fails closed; this operation
// never deletes or truncates SQLite sidecar files directly.
func CheckpointForBackup(ctx context.Context, database *provider.Database) error {
	return providerhandle.CheckpointSQLiteForBackup(ctx, (*providerhandle.Database)(database))
}
