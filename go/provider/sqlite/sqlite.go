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
