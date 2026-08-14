package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	internalSQLite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	providerapi "github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func p8RuntimeDatabaseConfig(t *testing.T) (*providerapi.Database, Config[testPrincipal, testActor]) {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	database, err := providersqlite.Open(ctx, providersqlite.Config{
		DataSourceName: "file:" + filepath.Join(t.TempDir(), "p8-runtime-database.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	fixture.Bundle = p8ApplyReviewedSQLiteFixture(t, ctx, database, fixture)

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptors, err := golem.GeneratedApplicationDescriptors(
		fixture.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()),
	)
	if err != nil {
		t.Fatal(err)
	}
	userBinding := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(
		fixture.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userBinding, postBinding}, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return database, Config[testPrincipal, testActor]{
		Database: database,
		Bundle:   fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal testPrincipal) (testActor, error) {
			return testActor{Allow: principal.Allow}, nil
		},
	}
}

func TestRuntimeBorrowsVerifiedDatabaseAndDerivesProviderFromHandle(t *testing.T) {
	database, config := p8RuntimeDatabaseConfig(t)
	app, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if app.database != database.UnsafeSQLX() || app.eventProvider != golem.SQLite {
		t.Fatal("runtime did not derive database and provider from the verified handle")
	}
	if database.UnsafeSQLX() == nil {
		t.Fatal("runtime took ownership of and closed the borrowed database handle")
	}
	if err := database.UnsafeSQLX().PingContext(context.Background()); err != nil {
		t.Fatalf("borrowed database is not usable after Open: %v", err)
	}
}

func p8ApplyReviewedSQLiteFixture(t *testing.T, ctx context.Context, database *providerapi.Database, fixture schematest.Fixture) golem.SchemaBundle {
	t.Helper()
	desired, err := physical.Normalize(fixture.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	empty := physical.PhysicalSchema{
		Version: desired.Version, CanonicalVersion: desired.CanonicalVersion,
		Provider: desired.Provider, Namespace: desired.Namespace,
	}
	empty, err = physical.Normalize(empty)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.Diff(empty, desired)
	if err != nil {
		t.Fatal(err)
	}
	emptyModelFingerprint, err := compilerir.ModelFingerprint(compilerir.CanonicalEmptyModel())
	if err != nil {
		t.Fatal(err)
	}
	allowlistFingerprint, err := physical.UnmanagedAllowlistFingerprint(desired)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{
		ID: "0001_initial", Operations: plan.Operations, Phases: plan.Phases,
		BeforeModel: migration.Digest(emptyModelFingerprint), AfterModel: migration.Digest(fixture.Bundle.Model().Fingerprint().String()),
		BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
		BeforeSnapshot: empty, AfterSnapshot: desired,
		UnmanagedAllowlistDigest: migration.Digest(allowlistFingerprint.String()),
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
	}
	script, err := internalSQLite.New().RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	const sqlPath = "migrations/sqlite/0001_initial.sql"
	files := map[string][]byte{sqlPath: []byte(script.SQL())}
	entry.Files = []migration.FileChecksum{{Path: sqlPath, SHA256: migration.Checksum(files[sqlPath])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest := migration.Manifest{
		FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
		HashAlgorithm: "sha256", GeneratorVersion: "p8-runtime-test", Provider: desired.Provider,
		Entries: []migration.ManifestEntry{entry},
	}
	encoded, err := migration.EncodeManifest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	if err := internalSQLite.New().ApplyMigration(ctx, database.UnsafeSQLX(), manifest, files); err != nil {
		t.Fatal(err)
	}
	providers := fixture.Bundle.Providers()
	for index, document := range providers {
		if document.Provider() != golem.SQLite {
			continue
		}
		migrationDocument := golem.GeneratedMigrationManifestDocument(fixture.Bundle.GenerationDigest(), golem.SQLite, encoded)
		providers[index] = golem.GeneratedProviderSchemaDocumentWithMigration(document.Provider(), document.SystemFingerprint(), document.Schema(), migrationDocument)
	}
	return golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		fixture.Bundle.Model(), fixture.Bundle.Contract(), providers...,
	)
}

func TestRuntimeRejectsNilZeroAndClosedHandlesBeforePrincipalWork(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*providerapi.Database, *Config[testPrincipal, testActor])
		code    string
	}{
		{name: "nil", prepare: func(_ *providerapi.Database, config *Config[testPrincipal, testActor]) { config.Database = nil }, code: "P3_RUNTIME_CONFIG"},
		{name: "zero", prepare: func(_ *providerapi.Database, config *Config[testPrincipal, testActor]) {
			config.Database = &providerapi.Database{}
		}, code: "P3_RUNTIME_CONFIG"},
		{name: "closed", prepare: func(database *providerapi.Database, _ *Config[testPrincipal, testActor]) { _ = database.Close() }, code: "P3_RUNTIME_CONFIG"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, config := p8RuntimeDatabaseConfig(t)
			var resolutions atomic.Int64
			config.ResolvePrincipal = func(context.Context, testPrincipal) (testActor, error) {
				resolutions.Add(1)
				return testActor{}, nil
			}
			test.prepare(database, &config)
			if _, err := Open(context.Background(), config); err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error=%v; want %s", err, test.code)
			}
			if resolutions.Load() != 0 {
				t.Fatalf("principal resolver ran %d times during rejected Open", resolutions.Load())
			}
		})
	}
}

func TestRuntimeRestoresVerifiedPoolProfileBeforeCapabilityReproof(t *testing.T) {
	database, config := p8RuntimeDatabaseConfig(t)
	database.UnsafeSQLX().SetMaxOpenConns(1)
	database.UnsafeSQLX().SetMaxIdleConns(0)
	database.UnsafeSQLX().SetConnMaxLifetime(0)
	database.UnsafeSQLX().SetConnMaxIdleTime(0)

	if _, err := Open(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	pool := database.Pool()
	if got := database.UnsafeSQLX().Stats().MaxOpenConnections; got != pool.MaximumOpen() {
		t.Fatalf("maximum open connections=%d; want restored %d", got, pool.MaximumOpen())
	}
}

func TestRuntimeRejectsHandleWithoutMatchingPhysicalSchemaAndDoesNotCloseIt(t *testing.T) {
	database, config := p8RuntimeDatabaseConfig(t)
	providers := config.Bundle.Providers()
	postgres := make([]golem.ProviderSchemaDocument, 0, 1)
	for _, document := range providers {
		if document.Provider() == golem.PostgreSQL {
			postgres = append(postgres, document)
		}
	}
	config.Bundle = golem.GeneratedSchemaBundle(
		config.Bundle.GenerationDigest(), config.Bundle.GeneratorVersion(), config.Bundle.TemplateABIVersion(),
		config.Bundle.Model(), config.Bundle.Contract(), postgres...,
	)
	if _, err := Open(context.Background(), config); err == nil || !strings.Contains(err.Error(), "P3_RUNTIME_SCHEMA") {
		t.Fatalf("mismatched handle error=%v", err)
	}
	if database.UnsafeSQLX() == nil {
		t.Fatal("failed runtime Open closed its borrowed database handle")
	}
}

func TestRuntimeStartupFailuresUseClosedSanitizedMessages(t *testing.T) {
	t.Run("ledger", func(t *testing.T) {
		database, config := p8RuntimeDatabaseConfig(t)
		if _, err := database.UnsafeSQLX().Exec(`DELETE FROM "_golem_migrations"`); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), config)
		assertP8StartupFailureSanitized(t, err, "P8_RUNTIME_MIGRATION", "_golem_migrations")
	})
	t.Run("drift", func(t *testing.T) {
		database, config := p8RuntimeDatabaseConfig(t)
		if _, err := database.UnsafeSQLX().Exec(`DROP TABLE "users"`); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), config)
		assertP8StartupFailureSanitized(t, err, "P3_RUNTIME_DRIFT", "users")
	})
	t.Run("capability", func(t *testing.T) {
		database, config := p8RuntimeDatabaseConfig(t)
		connections := make([]*sqlx.Conn, 0, database.Pool().MaximumOpen())
		for range database.Pool().MaximumOpen() {
			connection, err := database.UnsafeSQLX().Connx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			connections = append(connections, connection)
		}
		if _, err := connections[len(connections)-1].ExecContext(context.Background(), `PRAGMA busy_timeout = 1`); err != nil {
			t.Fatal(err)
		}
		for _, connection := range connections {
			_ = connection.Close()
		}
		_, err := Open(context.Background(), config)
		assertP8StartupFailureSanitized(t, err, "P3_RUNTIME_CAPABILITY", "busy_timeout")
	})
}

func assertP8StartupFailureSanitized(t *testing.T, err error, code string, canary string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Fatalf("startup error=%v; want %s", err, code)
	}
	if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(canary)) {
		t.Fatalf("startup error disclosed private detail %q: %v", canary, err)
	}
}
