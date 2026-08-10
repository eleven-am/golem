package p8mutation

import "time"

func providerRuntimeMutations() []Mutation {
	gate := func(pkg, test string) Gate { return Gate{Directory: "go", Package: pkg, Test: test} }
	providerGate := func(pkg, test string) Gate {
		return Gate{Directory: "go", Package: pkg, Test: test, Required: []string{"GOLEM_TEST_POSTGRES_DSN", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}}
	}
	return []Mutation{
		{
			Label: "PUBLIC_PROVIDER_RETURNS_UNVERIFIED_DB", Summary: "publish a public constructor around an unverified SQLX pool",
			Patches: []Patch{{Path: "go/provider/provider.go", Before: "func (database *Database) UnsafeSQLX() *sqlx.DB {\n\treturn (*providerhandle.Database)(database).UnsafeSQLX()\n}\n", After: "func (database *Database) UnsafeSQLX() *sqlx.DB {\n\treturn (*providerhandle.Database)(database).UnsafeSQLX()\n}\nfunc OpenUnverified(database *sqlx.DB) *Database {\n\treturn (*Database)(providerhandle.AdoptUnverifiedForTest(database, providerhandle.TestMetadata{Provider: golem.SQLite, MaximumOpen: 1, MaximumIdle: 1}))\n}\n"}},
			Gate:    gate("./provider", "TestP8PublicPackageInventoryHasNoInternalTypeLeak"), Timeout: 2 * time.Minute,
		},
		{
			Label: "SECOND_PROVIDER_ENUM_WINS", Summary: "replace the verified handle provider with a second runtime-selected enum",
			Patches: []Patch{{Path: "go/runtime/runtime.go", Before: "\tproviderIdentity := databaseHandle.Provider()\n", After: "\tproviderIdentity := databaseHandle.Provider()\n\tif providerIdentity == golem.SQLite { providerIdentity = golem.PostgreSQL } else { providerIdentity = golem.SQLite }\n"}},
			Gate:    gate("./runtime", "TestP8RuntimeBorrowsVerifiedDatabaseAndDerivesProviderFromHandle"), Timeout: 3 * time.Minute,
		},
		{
			Label: "ADOPT_ARBITRARY_SQLX_POOL", Summary: "expose arbitrary raw pool adoption as a public provider operation",
			Patches: []Patch{{Path: "go/provider/provider.go", Before: "func (database *Database) UnsafeSQLX() *sqlx.DB {\n\treturn (*providerhandle.Database)(database).UnsafeSQLX()\n}\n", After: "func (database *Database) UnsafeSQLX() *sqlx.DB {\n\treturn (*providerhandle.Database)(database).UnsafeSQLX()\n}\nfunc AdoptSQLX(database *sqlx.DB) *Database {\n\treturn (*Database)(providerhandle.AdoptUnverifiedForTest(database, providerhandle.TestMetadata{Provider: golem.SQLite, MaximumOpen: 1, MaximumIdle: 1}))\n}\n"}},
			Gate:    gate("./provider", "TestP8PublicPackageInventoryHasNoInternalTypeLeak"), Timeout: 2 * time.Minute,
		},
		{
			Label: "SAFE_NAMED_RAW_SQLX_ESCAPE", Summary: "add a raw SQLX escape without an unsafe name",
			Patches: []Patch{{Path: "go/provider/provider.go", Before: "func (database *Database) UnsafeSQLX() *sqlx.DB {\n\treturn (*providerhandle.Database)(database).UnsafeSQLX()\n}\n", After: "func (database *Database) UnsafeSQLX() *sqlx.DB {\n\treturn (*providerhandle.Database)(database).UnsafeSQLX()\n}\nfunc (database *Database) SQLX() *sqlx.DB { return database.UnsafeSQLX() }\n"}},
			Gate:    gate("./provider", "TestP8UnsafeSQLXIsOnlyRawPoolEscape"), Timeout: 2 * time.Minute,
		},
		{
			Label: "SQLITE_SKIP_CONNECTION_PRAGMAS", Summary: "initialize and verify one SQLite slot instead of applying provider pragmas to every connection",
			Patches: []Patch{
				{Path: "go/internal/provider/sqlite/lifecycle.go", Before: "\treturn dataSourceName + separator + \"_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate\", nil\n", After: "\treturn dataSourceName + separator + \"_txlock=immediate\", nil\n"},
				{Path: "go/internal/provider/sqlite/lifecycle.go", Before: "\treport, err := provider.VerifyPool(ctx, database, 4)\n", After: "\treport, err := provider.VerifyPool(ctx, database, 1)\n"},
				{Path: "go/internal/provider/sqlite/lifecycle.go", Before: "\tvar foreignKeys int\n\tif err := connection.GetContext(ctx, &foreignKeys, \"PRAGMA foreign_keys\"); err != nil {\n", After: "\tif _, err := connection.ExecContext(ctx, \"PRAGMA foreign_keys = ON\"); err != nil {\n\t\treturn CapabilityReport{}, fmt.Errorf(\"sqlite probe %s connection configure foreign_keys: %w\", label, err)\n\t}\n\tif _, err := connection.ExecContext(ctx, \"PRAGMA busy_timeout = 5000\"); err != nil {\n\t\treturn CapabilityReport{}, fmt.Errorf(\"sqlite probe %s connection configure busy_timeout: %w\", label, err)\n\t}\n\tvar foreignKeys int\n\tif err := connection.GetContext(ctx, &foreignKeys, \"PRAGMA foreign_keys\"); err != nil {\n"},
			},
			Gate: gate("./provider/sqlite", "TestP8SQLitePublicOpenAppliesInvariantToEveryPooledConnection"), Timeout: 3 * time.Minute,
		},
		{
			Label: "SQLITE_DEFERRED_DEFAULT", Summary: "replace provider-owned immediate SQLite transactions with deferred mode",
			Patches: []Patch{{Path: "go/internal/provider/sqlite/lifecycle.go", Before: "_txlock=immediate", After: "_txlock=deferred"}},
			Gate:    gate("./provider/sqlite", "TestP8SQLitePublicOpenUsesImmediateWriteTransactions"), Timeout: 3 * time.Minute,
		},
		{
			Label: "POSTGRES_FIRST_CONNECTION_ONLY", Summary: "reprove only the first PostgreSQL pool slot during runtime startup",
			Patches: []Patch{{Path: "go/runtime/runtime.go", Before: "proof, err := proveCapabilities(ctx, database, provider, [32]byte(registry.ModelFingerprint()), pool.MaximumOpen())", After: "proof, err := proveCapabilities(ctx, database, provider, [32]byte(registry.ModelFingerprint()), 1)"}},
			Gate:    providerGate("./runtime", "TestP8GeneratedAppOpenRejectsEveryPoisonedPoolSlotAcrossProviders"), Timeout: 5 * time.Minute,
		},
		{
			Label: "POSTGRES_UNBOUNDED_POOL_DEFAULT", Summary: "expand the PostgreSQL default pool to the maximum instead of the bounded default",
			Patches: []Patch{{Path: "go/internal/provider/handle/handle.go", Before: "\tpostgreSQLDefaultMaximumOpen = 16\n", After: "\tpostgreSQLDefaultMaximumOpen = postgreSQLMaximumPoolWidth\n"}},
			Gate:    providerGate("./provider/postgresql", "TestP8PostgreSQLPoolDefaultsAndHardLimits"), Timeout: 3 * time.Minute,
		},
		{
			Label: "LEAK_DSN_IN_ERROR", Summary: "include the PostgreSQL connection string in a public provider failure",
			Patches: []Patch{{Path: "go/internal/provider/handle/handle.go", Before: "\t\treturn nil, failure(CodeOpen, \"postgresql provider open failed\")\n", After: "\t\treturn nil, failure(CodeOpen, \"postgresql provider open failed: \"+dataSourceName)\n"}},
			Gate:    gate("./provider/postgresql", "TestP8PostgreSQLOpenFailureClosesAllResourcesAndRedactsDSN"), Timeout: 2 * time.Minute,
		},
		{
			Label: "APP_OPEN_APPLIES_MIGRATION", Summary: "perform an automatic schema write during runtime Open",
			Patches: []Patch{{Path: "go/runtime/runtime.go", Before: "\tmigrationStartup, err := prepareReviewedMigrationStartup(databaseHandle, config.Bundle, providerIdentity, expected)\n", After: "\t_, _ = database.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS \"_golem_p8_auto_migration\" (\"id\" INTEGER)`)\n\tmigrationStartup, err := prepareReviewedMigrationStartup(databaseHandle, config.Bundle, providerIdentity, expected)\n"}},
			Gate:    gate("./runtime", "TestP8AppOpenIsReadOnlyAndStartsNoBackgroundWork"), Timeout: 5 * time.Minute,
		},
		{
			Label: "APP_OPEN_STARTS_WORKER", Summary: "start an unowned background worker from runtime Open",
			Patches: []Patch{{Path: "go/runtime/runtime.go", Before: "\treturn app, nil\n}\n\nfunc validateEventConfiguration", After: "\tgo func() { select {} }()\n\treturn app, nil\n}\n\nfunc validateEventConfiguration"}},
			Gate:    gate("./runtime", "TestP8AppOpenIsReadOnlyAndStartsNoBackgroundWork"), Timeout: 5 * time.Minute,
		},
		{
			Label: "APP_CLOSES_BORROWED_DATABASE", Summary: "close the borrowed verified database when runtime Open returns",
			Patches: []Patch{{Path: "go/runtime/runtime.go", Before: "\tdatabaseHandle := config.Database\n\tif databaseHandle == nil {\n", After: "\tdatabaseHandle := config.Database\n\tif databaseHandle != nil { defer databaseHandle.Close() }\n\tif databaseHandle == nil {\n"}},
			Gate:    gate("./runtime", "TestP8ApplicationNeverClosesBorrowedDatabase"), Timeout: 5 * time.Minute,
		},
		{
			Label: "DOCTOR_REPAIRS_STATE", Summary: "write a repair marker during the read-only doctor command",
			Patches: []Patch{{Path: "go/cmd/golem/diagnostics_command.go", Before: "\tmodule, err := resolveModule(directory)\n\tif err != nil {\n", After: "\tmodule, err := resolveModule(directory)\n\tif err == nil { _ = os.WriteFile(filepath.Join(module.directory, \".golem-doctor-repair\"), []byte(\"repair\"), 0o600) }\n\tif err != nil {\n"}},
			Gate:    gate("./cmd/golem", "TestP8DoctorIsReadOnlyAndUsesPublicProviderLifecycle"), Timeout: 5 * time.Minute,
		},
		{
			Label: "DOCTOR_EMITS_SOURCE_OR_SCHEMA_NAME", Summary: "copy the caller DSN into a public doctor diagnostic",
			Patches: []Patch{{Path: "go/cmd/golem/diagnostics_command.go", Before: "\toutput := newDoctorOutput(*providerName)\n", After: "\toutput := newDoctorOutput(*providerName)\n\toutput.add(*dsn, \"error\")\n"}},
			Gate:    gate("./cmd/golem", "TestP8DoctorOutputRedactionCanary"), Timeout: 5 * time.Minute,
		},
	}
}
