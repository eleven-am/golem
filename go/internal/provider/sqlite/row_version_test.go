package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

const fixtureItemVersionField ir.FieldID = "20000000000000000000000000000099"

func rowVersionFixtureSchema(t *testing.T, versioned bool) physical.PhysicalSchema {
	t.Helper()
	schema := incrementalFixtureSchema(t, false)
	if !versioned {
		return schema
	}
	schema.Tables[0].Columns = append(schema.Tables[0].Columns, physical.PhysicalColumn{
		ID: fixtureItemVersionField, Name: physical.RowVersionColumn, Ordinal: 2, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger},
		Default: physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: "0"}},
	})
	schema.Unmanaged = append(schema.Unmanaged, physical.RowVersionUnmanagedObjects()...)
	return normalizeMigrationFixture(t, schema)
}

func TestSQLiteMigrationAddsTheVersionColumnAndCounter(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before, after := rowVersionFixtureSchema(t, false), rowVersionFixtureSchema(t, true)
	database := openMigrationFixture(t, provider, before, "row-version.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name") VALUES (1,'first')`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_row_version.sql", nil)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := database.GetContext(ctx, &version, `SELECT "golem_version" FROM "items" WHERE "id"=1`); err != nil || version != 0 {
		t.Fatalf("existing row version=%d err=%v; existing rows keep version 0", version, err)
	}
	var counter []int64
	if err := database.SelectContext(ctx, &counter, `SELECT "value" FROM "golem_row_version"`); err != nil || len(counter) != 1 || counter[0] != 0 {
		t.Fatalf("counter=%v err=%v; want exactly one row holding 0", counter, err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteVersionCounterDriftIsRefused(t *testing.T) {
	ctx := context.Background()
	provider := New()
	after := rowVersionFixtureSchema(t, true)
	database := openMigrationFixture(t, provider, after, "row-version-drift.db")
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "golem_row_version" ("value") VALUES (5)`); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("a counter with two rows was accepted: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE "golem_row_version"`); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("a missing counter was accepted: %v", err)
	}
}
