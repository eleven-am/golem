package postgresql

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func versionedPostgreSQLSchema(t *testing.T, base physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	versioned := base
	versioned.Tables = append([]physical.PhysicalTable(nil), base.Tables...)
	columns := append([]physical.PhysicalColumn(nil), base.Tables[0].Columns...)
	zero := ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: "0"}
	columns = append(columns, physical.PhysicalColumn{
		ID: ir.FieldID(id(960)), Name: physical.RowVersionColumn, Ordinal: uint32(len(columns)),
		Storage: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt},
		Default: physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &zero},
	})
	versioned.Tables[0].Columns = columns
	versioned.Unmanaged = append(append([]physical.UnmanagedObject(nil), base.Unmanaged...), physical.RowVersionUnmanagedObjects()...)
	return normalizePostgreSQLMigrationSchema(t, versioned)
}

func TestLiveReviewedPostgreSQLMigrationAddsTheVersionColumnAndSequence(t *testing.T) {
	const namespace = "golem_row_version_migrate"
	dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
	provider := New()
	ctx := context.Background()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cleanup := func() {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "` + namespace + `" CASCADE`)
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	}
	cleanup()
	defer cleanup()
	empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
	initial := livePostgreSQLMigrationSchema(t, namespace, false, false)
	versioned := versionedPostgreSQLSchema(t, initial)
	first, firstFiles := finalizePostgreSQLEntry(t, provider, reviewedPostgreSQLEntry(t, "001_initial", empty, initial, nil))
	second := reviewedPostgreSQLEntry(t, "002_row_version", initial, versioned, &first)
	plan, err := provider.PlanIncremental(second)
	if err != nil {
		t.Fatal(err)
	}
	sequence := strings.Index(plan.SQL(), `CREATE SEQUENCE IF NOT EXISTS "_golem"."golem_row_version" AS bigint INCREMENT BY 1 MINVALUE 1 START WITH 1 CACHE 1 NO CYCLE`)
	column := strings.Index(plan.SQL(), `"golem_version"`)
	if sequence < 0 || column < 0 || sequence > column {
		t.Fatalf("the migration must create the CACHE 1 sequence before the first versioned column:\n%s", plan.SQL())
	}
	second, secondFiles := finalizePostgreSQLEntry(t, provider, second)
	manifest := reviewedPostgreSQLManifest(provider, first, second)
	files := mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
	firstOnly := reviewedPostgreSQLManifest(provider, first)
	if err := provider.ApplyMigration(ctx, database, firstOnly, firstFiles); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO "` + namespace + `"."items" ("name") VALUES ('existing')`); err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatalf("row version migration: %v", err)
	}
	var existing int64
	if err := database.Get(&existing, `SELECT "golem_version" FROM "`+namespace+`"."items" WHERE "name"='existing'`); err != nil || existing != 0 {
		t.Fatalf("existing row version=%d err=%v; existing rows keep version 0", existing, err)
	}
	var drawn int64
	if err := database.Get(&drawn, `SELECT nextval('"_golem"."golem_row_version"')`); err != nil || drawn != 1 {
		t.Fatalf("first draw=%d err=%v; the sequence must start at 1", drawn, err)
	}
	if err := provider.Verify(ctx, database, versioned); err != nil {
		t.Fatal(err)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		t.Fatal(err)
	}
}
