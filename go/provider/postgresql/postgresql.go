// Package postgresql opens the verified PostgreSQL database profile required
// by Golem.
package postgresql

import (
	"context"
	"time"

	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	"github.com/eleven-am/golem/go/provider"
)

type Config struct {
	DataSourceName string
	Pool           PoolConfig
}

type PoolConfig struct {
	MaximumOpen               int
	MaximumIdle               int
	ConnectionMaximumLifetime time.Duration
	ConnectionMaximumIdleTime time.Duration
}

func Open(ctx context.Context, config Config) (*provider.Database, error) {
	database, err := providerhandle.OpenPostgreSQL(ctx, config.DataSourceName, providerhandle.PostgreSQLPoolConfig{
		MaximumOpen:               config.Pool.MaximumOpen,
		MaximumIdle:               config.Pool.MaximumIdle,
		ConnectionMaximumLifetime: config.Pool.ConnectionMaximumLifetime,
		ConnectionMaximumIdleTime: config.Pool.ConnectionMaximumIdleTime,
	})
	if err != nil {
		return nil, err
	}
	return (*provider.Database)(database), nil
}
