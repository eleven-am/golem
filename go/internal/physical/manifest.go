package physical

import (
	"reflect"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	CapabilitySQLiteForeignKeys      ir.CapabilityID = "sqlite.foreign_keys"
	CapabilitySQLiteJSON1            ir.CapabilityID = "sqlite.json1"
	CapabilitySQLiteGeneratedColumns ir.CapabilityID = "sqlite.generated_columns"
	CapabilityPostgreSQLGenerated    ir.CapabilityID = "postgresql.generated_columns"
)

func SQLiteManifest(capabilities ...CapabilityFact) ProviderManifest {
	manifestCapabilities := []CapabilityFact{{
		ID:           CapabilitySQLiteForeignKeys,
		Version:      1,
		Verification: VerificationRuntimeProbe,
	}}
	manifestCapabilities = append(manifestCapabilities, capabilities...)
	return ProviderManifest{
		Provider:       ir.SQLite,
		Driver:         DriverIdentity{Module: "github.com/ncruces/go-sqlite3", Adapter: "sqlx"},
		MinimumVersion: Version{Major: 3, Minor: 38},
		Capabilities:   manifestCapabilities,
	}
}

// CompatibleProviderHistory accepts an exact current manifest or the one
// reviewed SQLite runtime transition from modernc to the CGO-free ncruces
// sqlite-vec host. Historical capabilities must be preserved exactly; the new
// runtime may only add verified capabilities.
func CompatibleProviderHistory(previous, current ProviderManifest) bool {
	if reflect.DeepEqual(previous, current) {
		return true
	}
	if previous.Provider != ir.SQLite || current.Provider != ir.SQLite ||
		previous.Driver != (DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}) ||
		current.Driver != (DriverIdentity{Module: "github.com/ncruces/go-sqlite3", Adapter: "sqlx"}) ||
		previous.MinimumVersion != current.MinimumVersion {
		return false
	}
	currentFacts := make(map[ir.CapabilityID]CapabilityFact, len(current.Capabilities))
	for _, fact := range current.Capabilities {
		currentFacts[fact.ID] = fact
	}
	for _, fact := range previous.Capabilities {
		if currentFacts[fact.ID] != fact {
			return false
		}
	}
	return true
}

func PostgreSQLManifest(capabilities ...CapabilityFact) ProviderManifest {
	return ProviderManifest{
		Provider:       ir.PostgreSQL,
		Driver:         DriverIdentity{Module: "github.com/jackc/pgx/v5/stdlib", Adapter: "sqlx"},
		MinimumVersion: Version{Major: 15},
		Capabilities:   append([]CapabilityFact(nil), capabilities...),
	}
}
