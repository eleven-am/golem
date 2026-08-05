package physical

import "github.com/eleven-am/golem/go/internal/compiler/ir"

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
		Driver:         DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"},
		MinimumVersion: Version{Major: 3, Minor: 38},
		Capabilities:   manifestCapabilities,
	}
}

func PostgreSQLManifest(capabilities ...CapabilityFact) ProviderManifest {
	return ProviderManifest{
		Provider:       ir.PostgreSQL,
		Driver:         DriverIdentity{Module: "github.com/jackc/pgx/v5/stdlib", Adapter: "sqlx"},
		MinimumVersion: Version{Major: 15},
		Capabilities:   append([]CapabilityFact(nil), capabilities...),
	}
}
