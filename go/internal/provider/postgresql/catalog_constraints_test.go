package postgresql

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

type notNullCatalogFixture struct {
	provider *Provider
	database *sqlx.DB
	schema   physical.PhysicalSchema
	major    uint32
}

func newNotNullCatalogFixture(t *testing.T) notNullCatalogFixture {
	t.Helper()
	ctx := context.Background()
	provider := New()
	dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
	database, report, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	schema := incarnationPostgreSQLSchema(t, false)
	schema.Tables[0].Columns[1].Nullable = false
	schema = normalizePostgreSQLMigrationSchema(t, schema)
	before := normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{Version: schema.Version, CanonicalVersion: schema.CanonicalVersion, Provider: schema.Provider, Namespace: schema.Namespace})
	entry := reviewedPostgreSQLEntry(t, "001_initial", before, schema, nil)
	entry, files := finalizePostgreSQLEntry(t, provider, entry)
	if err := provider.ApplyMigration(ctx, database, reviewedPostgreSQLManifest(provider, entry), files); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL %d.%d", report.Version.Major, report.Version.Minor)
	return notNullCatalogFixture{provider: provider, database: database, schema: schema, major: report.Version.Major}
}

func TestPostgreSQLNotNullCatalogRoundTrip(t *testing.T) {
	fixture := newNotNullCatalogFixture(t)
	if err := fixture.provider.Verify(context.Background(), fixture.database, fixture.schema); err != nil {
		t.Fatal(err)
	}
	if fixture.major >= 18 {
		var count int
		if err := fixture.database.Get(&count, `SELECT count(*) FROM pg_catalog.pg_constraint WHERE contype='n' AND conrelid='incarnation_gate.items'::regclass`); err != nil || count != 2 {
			t.Fatalf("not-null catalog entries=%d error=%v", count, err)
		}
	}
}

func TestPostgreSQLNotNullCatalogRejectsDrift(t *testing.T) {
	for _, test := range []struct {
		name    string
		minimum uint32
		change  string
	}{
		{name: "missing", change: `ALTER TABLE incarnation_gate.items ALTER COLUMN name DROP NOT NULL`},
		{name: "unvalidated", minimum: 18, change: `ALTER TABLE incarnation_gate.items ALTER COLUMN name DROP NOT NULL; ALTER TABLE incarnation_gate.items ADD CONSTRAINT pending_nullability NOT NULL name NOT VALID`},
		{name: "no_inherit", minimum: 18, change: `ALTER TABLE incarnation_gate.items ALTER COLUMN name DROP NOT NULL; ALTER TABLE incarnation_gate.items ADD CONSTRAINT local_nullability NOT NULL name NO INHERIT`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNotNullCatalogFixture(t)
			if fixture.major < test.minimum {
				t.Skipf("requires PostgreSQL %d catalog features", test.minimum)
			}
			if _, err := fixture.database.Exec(test.change); err != nil {
				t.Fatal(err)
			}
			if err := fixture.provider.Verify(context.Background(), fixture.database, fixture.schema); err == nil {
				t.Fatal("unexpected nullability was accepted")
			}
		})
	}
}

func TestPostgreSQLCatalogRejectsUnenforcedConstraints(t *testing.T) {
	for _, table := range []string{"_golem_outbox", "_golem_outbox_delivery", "_golem_data_incarnation"} {
		t.Run(table, func(t *testing.T) {
			fixture := newNotNullCatalogFixture(t)
			if fixture.major < 18 {
				t.Skip("requires PostgreSQL 18 enforcement catalog features")
			}
			var constraint, definition string
			if err := fixture.database.QueryRowx(`SELECT conname,pg_catalog.pg_get_constraintdef(oid) FROM pg_catalog.pg_constraint WHERE conrelid=to_regclass($1) AND contype='c' ORDER BY conname LIMIT 1`, "_golem."+table).Scan(&constraint, &definition); err != nil {
				t.Fatal(err)
			}
			constraintName := quote(physical.PhysicalName(constraint))
			statement := `ALTER TABLE ` + qualified("_golem", physical.PhysicalName(table)) + ` DROP CONSTRAINT ` + constraintName + `, ADD CONSTRAINT ` + constraintName + ` ` + definition + ` NOT ENFORCED`
			if _, err := fixture.database.Exec(statement); err != nil {
				t.Fatal(err)
			}
			var validated, enforced bool
			if err := fixture.database.QueryRowx(`SELECT convalidated,conenforced FROM pg_catalog.pg_constraint WHERE conrelid=to_regclass($1) AND conname=$2`, "_golem."+table, constraint).Scan(&validated, &enforced); err != nil || enforced {
				t.Fatalf("constraint enforcement=%v error=%v", enforced, err)
			}
			t.Logf("validated=%v enforced=%v", validated, enforced)
			if err := fixture.provider.Verify(context.Background(), fixture.database, fixture.schema); err == nil {
				t.Fatal("unenforced storage constraint was accepted")
			}
		})
	}
}

func TestPostgreSQLQueueCatalogRejectsUnvalidatedNullability(t *testing.T) {
	store, database := legacyPostgreSQLQueueStorageFixture(t, false, false)
	var version int
	if err := database.Get(&version, `SHOW server_version_num`); err != nil {
		t.Fatal(err)
	}
	if version < 180000 {
		t.Skip("requires PostgreSQL 18 nullability catalog features")
	}
	if _, err := store.InspectStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`ALTER TABLE ` + store.table() + ` ALTER COLUMN payload DROP NOT NULL; ALTER TABLE ` + store.table() + ` ADD CONSTRAINT pending_payload NOT NULL payload NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InspectStorage(context.Background()); !errors.Is(err, queueprovider.ErrStorageUnrecognized) {
		t.Fatalf("unvalidated queue inspection=%v", err)
	}
}
