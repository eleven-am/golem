package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	providerapi "github.com/eleven-am/golem/go/provider"
	"github.com/jmoiron/sqlx"
)

// p8RuntimeTestDatabase is the repository-internal bridge for legacy runtime
// fixtures that instrument an exact sqlx pool. Production and generated code
// cannot import the constructor, and runtime.Open still reproves the live
// provider capabilities and physical schema before publishing an App.
func p8RuntimeTestDatabase(database *sqlx.DB, provider golem.Provider) *providerapi.Database {
	maximumOpen := database.Stats().MaxOpenConnections
	if maximumOpen < 1 {
		if provider == golem.PostgreSQL {
			// Legacy PostgreSQL fixtures previously relied on database/sql's
			// unbounded default and include tests that pin an independent
			// connection. Seal that existing pool at the repository-wide traced
			// width instead of silently collapsing it to one slot.
			maximumOpen = 8
		} else {
			// One connection preserves private in-memory SQLite and custom traced
			// driver behavior unless a fixture deliberately configured concurrency.
			maximumOpen = 1
		}
	}
	database.SetMaxOpenConns(maximumOpen)
	database.SetMaxIdleConns(maximumOpen)
	if provider == golem.PostgreSQL {
		p8PrimeRuntimeTestPostgreSQLPool(database, maximumOpen)
	}
	internal := providerhandle.AdoptUnverifiedForTest(database, providerhandle.TestMetadata{
		Provider: provider, MaximumOpen: maximumOpen, MaximumIdle: maximumOpen,
	})
	return (*providerapi.Database)(internal)
}

func p8PrimeRuntimeTestPostgreSQLPool(database *sqlx.DB, width int) {
	ctx := context.Background()
	connections := make([]*sqlx.Conn, 0, width)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < width; index++ {
		connection, err := database.Connx(ctx)
		if err != nil {
			panic(fmt.Sprintf("prime repository PostgreSQL test pool slot %d: %v", index, err))
		}
		connections = append(connections, connection)
	}
	for index, connection := range connections {
		for _, statement := range []string{
			`SET timezone = 'UTC'`,
			`SET datestyle = 'ISO, YMD'`,
			`SET intervalstyle = 'iso_8601'`,
			`SET standard_conforming_strings = 'on'`,
		} {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				panic(fmt.Sprintf("prime repository PostgreSQL test pool slot %d: %v", index, err))
			}
		}
	}
}
