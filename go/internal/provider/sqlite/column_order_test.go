package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
)

func TestSQLiteFieldReorderRebuildsTheTableInTheReviewedOrder(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, true)
	after := incrementalFixtureSchema(t, true)
	after.Tables = append([]physical.PhysicalTable(nil), after.Tables...)
	columns := append([]physical.PhysicalColumn(nil), after.Tables[0].Columns...)
	for index := range columns {
		switch columns[index].ID {
		case fixtureItemNameField:
			columns[index].Ordinal = 2
		case fixtureItemNoteField:
			columns[index].Ordinal = 1
		}
	}
	after.Tables[0].Columns = columns
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "reorder.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name","note") VALUES (7,'kept',x'01')`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_reorder_items.sql", nil)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var order []string
	if err := database.SelectContext(ctx, &order, `SELECT "name" FROM pragma_table_info('items') ORDER BY "cid"`); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "id,note,name" {
		t.Fatalf("physical column order = %v", order)
	}
	var name string
	if err := database.GetContext(ctx, &name, `SELECT "name" FROM "items" WHERE "id"=7`); err != nil || name != "kept" {
		t.Fatalf("row name=%q err=%v", name, err)
	}
	if _, err := provider.introspect(ctx, database, after); err != nil {
		t.Fatalf("reordered table drifted: %v", err)
	}
}
