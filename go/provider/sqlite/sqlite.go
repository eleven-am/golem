// Package sqlite opens the verified SQLite database profile required by Golem.
package sqlite

import (
	"context"

	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	"github.com/eleven-am/golem/go/provider"
)

type Config struct {
	DataSourceName string
}

func Open(ctx context.Context, config Config) (*provider.Database, error) {
	database, err := providerhandle.OpenSQLite(ctx, config.DataSourceName)
	if err != nil {
		return nil, err
	}
	return (*provider.Database)(database), nil
}

// CheckpointForBackup performs the bounded SQLite WAL checkpoint required
// before copying a database file. The supplied verified SQLite handle must
// already be closed, and all other application readers and writers must have
// stopped. Busy ownership fails closed; this operation never deletes or
// truncates SQLite sidecar files directly.
func CheckpointForBackup(ctx context.Context, database *provider.Database) error {
	return providerhandle.CheckpointSQLiteForBackup(ctx, (*providerhandle.Database)(database))
}
