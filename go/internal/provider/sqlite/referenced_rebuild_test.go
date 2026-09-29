package sqlite

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
)

func respelledParentFixture(t *testing.T, tableName, keyName physical.PhysicalName) physical.PhysicalSchema {
	t.Helper()
	schema := foreignKeyMigrationFixtureSchema(t, false)
	schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
	for index := range schema.Tables {
		if schema.Tables[index].ID != fixtureItemTable {
			continue
		}
		table := schema.Tables[index]
		table.Name = tableName
		table.Columns = append([]physical.PhysicalColumn(nil), table.Columns...)
		for columnIndex := range table.Columns {
			if table.Columns[columnIndex].ID == fixtureItemIDField {
				table.Columns[columnIndex].Name = keyName
			}
		}
		schema.Tables[index] = table
	}
	return normalizeMigrationFixture(t, schema)
}

func TestSQLiteRebuildOfARespelledParentRebuildsItsReferencingTables(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		tableName physical.PhysicalName
		keyName   physical.PhysicalName
	}{
		{name: "renamed table", tableName: "things", keyName: "id"},
		{name: "renamed key column", tableName: "items", keyName: "item_key"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			provider := New()
			before := foreignKeyMigrationFixtureSchema(t, true)
			after := respelledParentFixture(t, testCase.tableName, testCase.keyName)
			database := openMigrationFixture(t, provider, before, "respelled.db")
			if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name","note") VALUES (7,'kept',x'01')`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `INSERT INTO "children" ("id","item_id") VALUES (1,7)`); err != nil {
				t.Fatal(err)
			}
			manifest, files := migrationFixtureManifest(t, before, after, "001_respell_items.sql", nil)
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			var target string
			if err := database.GetContext(ctx, &target, `SELECT "table" || '.' || "to" FROM pragma_foreign_key_list('children')`); err != nil {
				t.Fatal(err)
			}
			if target != string(testCase.tableName)+"."+string(testCase.keyName) {
				t.Fatalf("children reference %q", target)
			}
			var child int
			if err := database.GetContext(ctx, &child, `SELECT "item_id" FROM "children" WHERE "id"=1`); err != nil || child != 7 {
				t.Fatalf("child row=%d err=%v", child, err)
			}
			if _, err := database.ExecContext(ctx, `INSERT INTO "children" ("id","item_id") VALUES (2,7)`); err != nil {
				t.Fatal(err)
			}
			assertForeignKeysState(t, database, 1)
		})
	}
}

func TestSQLiteRebuildOfAnUnrespelledParentLeavesReferencingTablesAlone(t *testing.T) {
	provider := New()
	before := foreignKeyMigrationFixtureSchema(t, true)
	after := foreignKeyMigrationFixtureSchema(t, false)
	manifest, _ := migrationFixtureManifest(t, before, after, "001_drop_note.sql", nil)
	plan, err := provider.PlanIncremental(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rebuilds) != 1 || plan.Rebuilds[0].TableID != fixtureItemTable {
		t.Fatalf("rebuilds=%#v", plan.Rebuilds)
	}
}
