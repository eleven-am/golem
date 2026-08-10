package runtime_test

import (
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	providerapi "github.com/eleven-am/golem/go/provider"
	"github.com/jmoiron/sqlx"
)

// p8AdoptTracedProviderHandle keeps legacy SQL-boundary evidence on the exact
// pool borrowed by runtime.Open. The constructor is module-internal and
// deliberately unverified; runtime.Open still reproves live provider and
// physical-schema capabilities before publishing the App.
func p8AdoptTracedProviderHandle(database *sqlx.DB, profile p5ExtensionProviderProfile) *providerapi.Database {
	maximumOpen := database.Stats().MaxOpenConnections
	if maximumOpen < 1 {
		maximumOpen = 8
	}
	internal := providerhandle.AdoptUnverifiedForTest(database, providerhandle.TestMetadata{
		Provider: profile.provider, MaximumOpen: maximumOpen, MaximumIdle: maximumOpen,
	})
	return (*providerapi.Database)(internal)
}
