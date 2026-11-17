package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/jmoiron/sqlx"
)

func TestApplyMigrationRefreshesSQLitePlannerStatistics(t *testing.T) {
	ctx := context.Background()
	provider := New()
	indexed := func(schema physical.PhysicalSchema) physical.PhysicalSchema {
		name := fixtureItemNameField
		schema.Tables[0].Indexes = []physical.PhysicalIndex{{
			ID: "30000000000000000000000000000002", Name: "idx_items_name", Method: physical.IndexBTree,
			Keys: []physical.IndexKey{{Column: &name, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional,
		}}
		return normalizeMigrationFixture(t, schema)
	}
	before := indexed(incrementalFixtureSchema(t, false))
	after := indexed(incrementalFixtureSchema(t, true))
	database := openMigrationFixture(t, provider, before, "planner.db")
	if _, err := database.ExecContext(ctx, `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<2000) INSERT INTO items(id,name) SELECT n,printf('m%d',n%10) FROM seq`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_add_note.sql", nil)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var statistic string
	if err := database.GetContext(ctx, &statistic, `SELECT stat FROM sqlite_stat1 WHERE tbl='items' AND idx='idx_items_name'`); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statistic, "2000 ") {
		t.Fatalf("name statistics=%q", statistic)
	}
}

func TestHistoricalV1SQLiteReviewedInitialIgnoresExtensionMetadataAndBindsSealedFacts(t *testing.T) {
	readSnapshot := func(name string) physical.PhysicalSchema {
		t.Helper()
		raw, err := os.ReadFile("testdata/historical-v1/0001_initial." + name + ".snapshot.json")
		if err != nil {
			t.Fatal(err)
		}
		var schema physical.PhysicalSchema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		return schema
	}
	before := readSnapshot("before")
	after := readSnapshot("after")
	after.Extensions = append(after.Extensions, physical.Extension{
		ID:       ir.ExtensionID(id(996)),
		Provider: ir.SQLite,
		Kind:     "historical.metadata",
		Version:  1,
		Owner:    physical.ObjectRef{Kind: ir.ObjectModel, ModelID: after.Tables[0].ID},
	})
	before, err := physical.NormalizeHistoricalV1(before)
	if err != nil {
		t.Fatal(err)
	}
	after, err = physical.NormalizeHistoricalV1(after)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.DiffHistorical(before, after)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, err := physical.HistoricalPhysicalFingerprint(before)
	if err != nil {
		t.Fatal(err)
	}
	afterFingerprint, err := physical.HistoricalPhysicalFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{
		ID: "0001_initial", Operations: plan.Operations, Phases: plan.Phases,
		BeforePhysical: migration.Digest(beforeFingerprint.String()), AfterPhysical: migration.Digest(afterFingerprint.String()),
		BeforeSnapshot: before, AfterSnapshot: after,
	}
	script, err := New().RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/historical-v1/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if script.SQL() != string(want) {
		t.Fatal("historical v1 extension metadata changed retained SQLite DDL")
	}
	if strings.Contains(script.SQL(), "vec0") || strings.Contains(script.SQL(), "_vec") {
		t.Fatal("historical v1 renderer invented current semantic-extension DDL")
	}

	for name, mutate := range map[string]func(*migration.ManifestEntry){
		"before fingerprint": func(value *migration.ManifestEntry) { value.BeforePhysical = migration.Digest(strings.Repeat("0", 64)) },
		"after fingerprint":  func(value *migration.ManifestEntry) { value.AfterPhysical = migration.Digest(strings.Repeat("f", 64)) },
		"provider": func(value *migration.ManifestEntry) {
			value.AfterSnapshot.Provider.Provider = ir.PostgreSQL
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := entry
			mutate(&forged)
			if _, err := New().RenderMigration(forged); err == nil {
				t.Fatal("forged sealed typed fact was accepted")
			}
		})
	}
}

func TestSQLiteIncrementalSemanticIndexCreatesManagedExactVectorsAtomically(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{string(fixtureItemNameField)}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{
		ID: "70000000000000000000000000000001", Provider: ir.SQLite, Version: semanticcontract.Version,
		Owner: ir.ObjectID(fixtureItemTable), Kind: semanticcontract.IndexKind, Payload: payload,
	}, migrationFixtureTable(t, before, fixtureItemTable))
	if err != nil {
		t.Fatal(err)
	}
	after := normalizeMigrationFixture(t, before)
	after.Extensions = []physical.Extension{extension}
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "semantic-index.db")
	manifest, files := migrationFixtureManifest(t, before, after, "001_semantic_index.sql", nil)
	if len(manifest.Entries[0].Operations) != 2 || manifest.Entries[0].Operations[0].Kind != migration.CreateProviderExtension {
		t.Fatalf("semantic operations = %#v", manifest.Entries[0].Operations)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := database.GetContext(ctx, &version, "SELECT vec1_info()"); err != nil || version == "" {
		t.Fatalf("vec1 info=%q error=%v", version, err)
	}
}

func TestSQLiteIncrementalFullTextIndexBackfillsAndVerifies(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Prefix: []uint8{}, Fields: []fulltextcontract.Field{{ID: string(fixtureItemNameField), Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := fulltextstorage.Lower(ir.ProviderExtensionIR{ID: "70000000000000000000000000000002", Provider: ir.SQLite, Version: fulltextcontract.Version, Owner: ir.ObjectID(fixtureItemTable), Kind: fulltextcontract.IndexKind, Payload: payload}, migrationFixtureTable(t, before, fixtureItemTable))
	if err != nil {
		t.Fatal(err)
	}
	after := normalizeMigrationFixture(t, before)
	after.Extensions = []physical.Extension{extension}
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "fulltext-index.db")
	table := migrationFixtureTable(t, before, fixtureItemTable)
	if _, err := database.ExecContext(ctx, "INSERT INTO "+quote(table.Name)+" (\"id\",\"name\") VALUES (?,?)", 1, "Renée mailbox"); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_fulltext_index.sql", nil)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.GetContext(ctx, &count, `SELECT count(*) FROM "_golem_fulltext_70000000000000000000000000000002_fts" WHERE "_golem_fulltext_70000000000000000000000000000002_fts" MATCH 'renee'`); err != nil || count != 1 {
		t.Fatalf("backfilled full-text rows=%d err=%v", count, err)
	}
}

func TestSQLiteOwnerRebuildRecreatesFullTextStorageAfterTheNewTable(t *testing.T) {
	for _, test := range []struct {
		name          string
		renameIndexed bool
		rebuildOwner  bool
	}{
		{name: "unchanged projection rebuild", rebuildOwner: true},
		{name: "changed projection rebuild", renameIndexed: true, rebuildOwner: true},
		{name: "changed projection direct", renameIndexed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			provider := New()
			before := incrementalFixtureSchema(t, test.rebuildOwner)
			payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Fields: []fulltextcontract.Field{{ID: string(fixtureItemNameField), Weight: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			declaration := ir.ProviderExtensionIR{ID: "70000000000000000000000000000002", Provider: ir.SQLite, Version: fulltextcontract.Version, Owner: ir.ObjectID(fixtureItemTable), Kind: fulltextcontract.IndexKind, Payload: payload}
			beforeExtension, err := fulltextstorage.Lower(declaration, migrationFixtureTable(t, before, fixtureItemTable))
			if err != nil {
				t.Fatal(err)
			}
			before.Extensions = []physical.Extension{beforeExtension}
			before = normalizeMigrationFixture(t, before)
			after := incrementalFixtureSchema(t, false)
			indexedName := physical.PhysicalName("name")
			if test.renameIndexed {
				for tableIndex := range after.Tables {
					if after.Tables[tableIndex].ID != fixtureItemTable {
						continue
					}
					for columnIndex := range after.Tables[tableIndex].Columns {
						if after.Tables[tableIndex].Columns[columnIndex].ID == fixtureItemNameField {
							after.Tables[tableIndex].Columns[columnIndex].Name = "content"
							indexedName = "content"
						}
					}
				}
			}
			afterExtension, err := fulltextstorage.Lower(declaration, migrationFixtureTable(t, after, fixtureItemTable))
			if err != nil {
				t.Fatal(err)
			}
			after.Extensions = []physical.Extension{afterExtension}
			after = normalizeMigrationFixture(t, after)
			database := openMigrationFixture(t, provider, before, strings.ReplaceAll(test.name, " ", "-")+".db")
			insert := `INSERT INTO "items" ("id","name") VALUES (7,'before rebuild')`
			if test.rebuildOwner {
				insert = `INSERT INTO "items" ("id","name","note") VALUES (7,'before rebuild',x'01')`
			}
			if _, err := database.ExecContext(ctx, insert); err != nil {
				t.Fatal(err)
			}
			manifest, files := migrationFixtureManifest(t, before, after, "002_rebuild_fulltext.sql", nil)
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			if err := provider.Verify(ctx, database, after); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `UPDATE "items" SET `+quote(indexedName)+`='after rebuilt' WHERE "id"=7`); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := database.GetContext(ctx, &count, `SELECT count(*) FROM "_golem_fulltext_70000000000000000000000000000002_fts" WHERE "_golem_fulltext_70000000000000000000000000000002_fts" MATCH 'rebuilt'`); err != nil || count != 1 {
				t.Fatalf("full-text rows after owner rebuild=%d err=%v", count, err)
			}
		})
	}
}

func TestSQLiteIncrementalSemanticIndexRewritePreservesOwnerRowsAndClearsDerivedState(t *testing.T) {
	ctx := context.Background()
	provider := New()
	base := incrementalFixtureSchema(t, false)
	extensionID := ir.ExtensionID("70000000000000000000000000000001")
	semanticExtension := func(dimensions uint16) physical.Extension {
		t.Helper()
		payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: dimensions, Fields: []string{string(fixtureItemNameField)}, Metric: "cosine"})
		if err != nil {
			t.Fatal(err)
		}
		extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: extensionID, Provider: ir.SQLite, Version: semanticcontract.Version, Owner: ir.ObjectID(fixtureItemTable), Kind: semanticcontract.IndexKind, Payload: payload}, migrationFixtureTable(t, base, fixtureItemTable))
		if err != nil {
			t.Fatal(err)
		}
		return extension
	}
	before := base
	before.Extensions = []physical.Extension{semanticExtension(3)}
	before = normalizeMigrationFixture(t, before)
	after := base
	after.Extensions = []physical.Extension{semanticExtension(4)}
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "semantic-rewrite.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name") VALUES (1,'preserved')`); err != nil {
		t.Fatal(err)
	}
	baseName := "_golem_semantic_" + string(extensionID)
	encoded, err := sqlitevec.Serialize([]float32{1, 0, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "`+baseName+`_vec" (record_key,embedding) VALUES ('old',?)`, encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "`+baseName+`_state" (record_key,source_hash,space_fingerprint,status,attempt_count,error_code,updated_at,"id") VALUES ('old',X'01','old','ready',1,NULL,1,1)`); err != nil {
		t.Fatal(err)
	}

	manifest, files := migrationFixtureManifest(t, before, after, "002_semantic_rewrite.sql", nil)
	operations := manifest.Entries[0].Operations
	if len(operations) != 3 || operations[0].Kind != migration.DropProviderExtension || operations[1].Kind != migration.CreateProviderExtension || operations[0].Risk != migration.RiskRewrite || operations[1].Risk != migration.RiskRewrite {
		t.Fatalf("semantic rewrite operations=%#v", operations)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := database.GetContext(ctx, &name, `SELECT "name" FROM "items" WHERE "id"=1`); err != nil || name != "preserved" {
		t.Fatalf("owner row name=%q error=%v", name, err)
	}
	for _, suffix := range []string{"_state", "_vec"} {
		var count int
		if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "`+baseName+suffix+`"`); err != nil || count != 0 {
			t.Fatalf("rebuilt %s count=%d error=%v", suffix, count, err)
		}
	}
}

func TestSQLiteIncrementalAdditiveMigrationAndExactLedger(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := incrementalFixtureSchema(t, true)
	database := openMigrationFixture(t, provider, before, "additive.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name") VALUES (1,'first')`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_add_note.sql", nil)
	plan, err := provider.PlanIncremental(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rebuilds) != 0 {
		t.Fatalf("safe nullable add unexpectedly rebuilt table: %#v", plan.Rebuilds)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var row struct {
		Name string  `db:"name"`
		Note *string `db:"note"`
	}
	if err := database.GetContext(ctx, &row, `SELECT "name","note" FROM "items" WHERE "id"=1`); err != nil {
		t.Fatal(err)
	}
	if row.Name != "first" || row.Note != nil {
		t.Fatalf("row changed across additive migration: %#v", row)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		t.Fatal(err)
	}
	assertForeignKeysState(t, database, 1)
}

func TestSystemOutboxV1MigratesIntrospectsAndFingerprintsSQLite(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := before
	after.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.OutboxSystemObjectV1())
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "outbox-v1.db")
	manifest, files := migrationFixtureManifest(t, before, after, "001_outbox_v1.sql", nil)
	script, err := provider.RenderMigration(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`CREATE TABLE "_golem_outbox"`, `"before_identity" BLOB`, `"delete_snapshot" BLOB`, `UNIQUE ("causation_id", "transaction_ordinal")`, `CREATE INDEX "_golem_outbox_pending"`} {
		if !strings.Contains(script.SQL(), fragment) {
			t.Fatalf("outbox migration missing %q:\n%s", fragment, script.SQL())
		}
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	valid := `INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES ('event-1',1,'golem.fact.v1','fingerprint','model','created',X'01','cause',0,X'02',1)`
	if _, err := database.ExecContext(ctx, valid); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, strings.Replace(valid, "event-1", "event-2", 1)); err == nil {
		t.Fatal("duplicate causation ordinal was accepted")
	}
	invalidShape := `INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES ('event-3',1,'golem.fact.v1','fingerprint','model','created',X'00',X'01','other',0,X'02',1)`
	if _, err := database.ExecContext(ctx, invalidShape); err == nil {
		t.Fatal("invalid created identity shape was accepted")
	}
}

func TestSystemUpsertGuardV1MigratesIntrospectsAndRollsBackSQLite(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := before
	after.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.UpsertGuardSystemObjectV1())
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "upsert-guard-v1.db")
	manifest, files := migrationFixtureManifest(t, before, after, "001_upsert_guard_v1.sql", nil)
	script, err := provider.RenderMigration(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	fragment := `CREATE TABLE "_golem_upsert_guard" ("guard_token" BLOB NOT NULL, PRIMARY KEY ("guard_token")) STRICT`
	if !strings.Contains(script.SQL(), fragment) {
		t.Fatalf("upsert guard migration missing %q:\n%s", fragment, script.SQL())
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO "_golem_upsert_guard" ("guard_token") VALUES (X'0102')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.GetContext(ctx, &count, `SELECT count(*) FROM "_golem_upsert_guard"`); err != nil || count != 0 {
		t.Fatalf("rolled-back guard rows=%d error=%v", count, err)
	}
}

func TestDeliverySystemObjectMigratesBackfillsAndDriftChecksSQLite(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	before.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.OutboxSystemObjectV1(), physical.UpsertGuardSystemObjectV1())
	before = normalizeMigrationFixture(t, before)
	after := before
	after.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.OutboxDeliverySystemObjectV1())
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "p7-delivery.db")
	const causation = "00000000-0000-4000-8000-000000000001"
	for ordinal, recorded := range []int64{20, 10} {
		_, err := database.ExecContext(ctx, `INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("00000000-0000-4000-8000-%012d", ordinal+1), 1, "golem.fact.v1", "fingerprint", "model", "created", []byte{1}, causation, ordinal, []byte{byte(ordinal + 1)}, recorded)
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_p7_delivery.sql", nil)
	script, err := provider.RenderMigration(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`CREATE TABLE "_golem_outbox_delivery"`, `"status" IN ('pending','leased','delivered','blocked','retired')`, `CREATE INDEX "_golem_outbox_delivery_pending"`, `INSERT OR IGNORE INTO "_golem_outbox_delivery"`} {
		if !strings.Contains(script.SQL(), fragment) {
			t.Fatalf("delivery migration missing %q:\n%s", fragment, script.SQL())
		}
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	var delivery struct {
		Causation string `db:"causation_id"`
		Status    string `db:"status"`
		First     int64  `db:"first_recorded_at"`
		Available int64  `db:"available_at"`
		Updated   int64  `db:"updated_at"`
		Attempts  int64  `db:"attempt_count"`
	}
	if err := database.GetContext(ctx, &delivery, `SELECT "causation_id","status","first_recorded_at","available_at","updated_at","attempt_count" FROM "_golem_outbox_delivery"`); err != nil {
		t.Fatal(err)
	}
	if delivery.Causation != causation || delivery.Status != "pending" || delivery.First != 10 || delivery.Available != 10 || delivery.Updated != 10 || delivery.Attempts != 0 {
		t.Fatalf("backfilled delivery=%#v", delivery)
	}
	if _, err := database.ExecContext(ctx, sqliteOutboxDeliveryBackfill(after.System)); err != nil {
		t.Fatal(err)
	}
	var deliveryCount, factCount int
	if err := database.GetContext(ctx, &deliveryCount, `SELECT count(*) FROM "_golem_outbox_delivery"`); err != nil || deliveryCount != 1 {
		t.Fatalf("idempotent backfill rows=%d error=%v", deliveryCount, err)
	}
	if err := database.GetContext(ctx, &factCount, `SELECT count(*) FROM "_golem_outbox"`); err != nil || factCount != 2 {
		t.Fatalf("preserved facts=%d error=%v", factCount, err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "_golem_outbox_delivery" ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") VALUES ('00000000-0000-4000-8000-000000000002','leased',1,0,1,1)`); err == nil {
		t.Fatal("leased state without lease identity was accepted")
	}
	if _, err := database.ExecContext(ctx, `DROP INDEX "_golem_outbox_delivery_pending"`); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("delivery index drift error=%v", err)
	}
}

func TestSQLiteReviewedBackfillStagesFillsChecksAndRebuilds(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before, after := sqliteRequiredBackfillSchemas(t)
	database := openMigrationFixture(t, provider, before, "reviewed-backfill.db")
	seedSQLiteRequiredBackfill(t, database)
	reviewed := []byte("UPDATE \"items\" SET \"removed\" = CASE WHEN \"name\" = 'retired' THEN 1 ELSE 0 END WHERE \"removed\" IS NULL;\n")
	manifest, files := sqliteRequiredBackfillManifest(t, before, after, reviewed)
	script := string(files["001_required_removed.sql"])
	staging := `ALTER TABLE "items" ADD COLUMN "removed" INTEGER`
	positions := []int{strings.Index(script, staging), strings.Index(script, "-- golem reviewed backfill companion "), strings.Index(script, "-- golem generated postcondition "), strings.Index(script, `INSERT INTO "_golem_tmp_`)}
	for index, position := range positions {
		if position < 0 || index > 0 && position <= positions[index-1] {
			t.Fatalf("reviewed script must stage/fill/check before rebuilding, positions=%v:\n%s", positions, script)
		}
	}
	stagedStatement := strings.SplitN(script[positions[0]:], ";", 2)[0]
	if strings.Contains(stagedStatement, "NOT NULL") || strings.Contains(stagedStatement, "DEFAULT") || strings.Contains(script, string(reviewed)) {
		t.Fatalf("staging must be nullable without an invented default; companion stays separate:\n%s", script)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatalf("reviewed required-column migration: %v", err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatalf("final schema, indexes and managed FTS differ from reviewed snapshot: %v", err)
	}
	var rows []struct {
		ID      int64  `db:"id"`
		Name    string `db:"name"`
		Version int64  `db:"golem_version"`
		Removed int64  `db:"removed"`
	}
	if err := database.SelectContext(ctx, &rows, `SELECT id,name,golem_version,removed FROM items ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != 7 || rows[0].Name != "retired" || rows[0].Version != 41 || rows[0].Removed != 1 || rows[1].ID != 9 || rows[1].Name != "mailbox" || rows[1].Version != 57 || rows[1].Removed != 0 {
		t.Fatalf("backfill changed identity/version or derived wrong values: %#v", rows)
	}
	var counter, child int64
	if err := database.GetContext(ctx, &counter, `SELECT value FROM golem_row_version`); err != nil || counter != 57 {
		t.Fatalf("version source=%d error=%v", counter, err)
	}
	if err := database.GetContext(ctx, &child, `SELECT item_id FROM children WHERE id=1`); err != nil || child != 7 {
		t.Fatalf("child identity=%d error=%v", child, err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO children(id,item_id) VALUES (2,999)`); err == nil {
		t.Fatal("rebuilt FK accepted a missing parent")
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO items(id,name) VALUES (10,'missing removed')`); err == nil {
		t.Fatal("final table accepted NULL in the required field")
	}
	assertSQLiteBackfillFTS(t, database, "mailbox", 1)
	if _, err := database.ExecContext(ctx, `UPDATE items SET name='refreshed' WHERE id=9`); err != nil {
		t.Fatal(err)
	}
	assertSQLiteBackfillFTS(t, database, "mailbox", 0)
	assertSQLiteBackfillFTS(t, database, "refreshed", 1)
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger=%#v error=%v", ledger, err)
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		t.Fatal(err)
	}
	assertForeignKeysState(t, database, 1)
}

func TestSQLiteReviewedBackfillNoNullFailureRollsBackDataSchemaAndLedger(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before, after := sqliteRequiredBackfillSchemas(t)
	database := openMigrationFixture(t, provider, before, "reviewed-backfill-rollback.db")
	seedSQLiteRequiredBackfill(t, database)
	// One row is filled and one remains NULL. The generated check must fail
	// before rebuilding, rolling back both the staging column and its values.
	reviewed := []byte("UPDATE \"items\" SET \"removed\"=1 WHERE \"id\"=7;\n")
	manifest, files := sqliteRequiredBackfillManifest(t, before, after, reviewed)
	err := provider.ApplyMigration(ctx, database, manifest, files)
	if err == nil || !strings.Contains(err.Error(), "postcondition") || !strings.Contains(err.Error(), "failed") || strings.Contains(err.Error(), "UPDATE") {
		t.Fatalf("want sanitized generated-postcondition failure, got %v", err)
	}
	if err := provider.Verify(ctx, database, before); err != nil {
		t.Fatalf("failed fill changed original schema/indexes/FTS: %v", err)
	}
	var name string
	if err := database.GetContext(ctx, &name, `SELECT name FROM items WHERE id=7`); err != nil || name != "retired" {
		t.Fatalf("companion data escaped rollback: name=%q error=%v", name, err)
	}
	var versions []int64
	if err := database.SelectContext(ctx, &versions, `SELECT golem_version FROM items ORDER BY id`); err != nil || !reflect.DeepEqual(versions, []int64{41, 57}) {
		t.Fatalf("versions=%v error=%v", versions, err)
	}
	var staged, temporary, child, counter int64
	for _, check := range []struct {
		target    *int64
		statement string
		want      int64
	}{
		{&staged, `SELECT count(*) FROM pragma_table_info('items') WHERE name='removed'`, 0},
		{&temporary, `SELECT count(*) FROM sqlite_schema WHERE name LIKE '_golem_tmp_%'`, 0},
		{&child, `SELECT item_id FROM children WHERE id=1`, 7},
		{&counter, `SELECT value FROM golem_row_version`, 57},
	} {
		if err := database.GetContext(ctx, check.target, check.statement); err != nil || *check.target != check.want {
			t.Fatalf("rollback invariant %q=%d want=%d error=%v", check.statement, *check.target, check.want, err)
		}
	}
	assertSQLiteBackfillFTS(t, database, "retired", 1)
	assertSQLiteBackfillFTS(t, database, "mailbox", 1)
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(ledger) != 0 {
		t.Fatalf("failed migration advanced ledger=%#v error=%v", ledger, err)
	}
	assertForeignKeysState(t, database, 1)
}

func TestSQLiteReviewedBackfillRestrictsCompanionAuthorityAndRestoresConnection(t *testing.T) {
	for _, test := range []struct{ name, sql string }{
		{"multiple statements", `UPDATE items SET removed=0; COMMIT;`},
		{"commit", `COMMIT;`},
		{"rollback", `ROLLBACK;`},
		{"savepoint", `SAVEPOINT private_savepoint;`},
		{"pragma", `PRAGMA foreign_keys=OFF;`},
		{"attach", `ATTACH DATABASE ':memory:' AS private_attached;`},
		{"ddl", `CREATE TABLE private_extra(id INTEGER);`},
		{"metadata write", `UPDATE golem_row_version SET value=0;`},
		{"metadata read", `UPDATE items SET removed=(SELECT value FROM golem_row_version);`},
		{"old column", `UPDATE items SET removed=0,name='private_rewrite';`},
		{"other model", `UPDATE children SET item_id=999;`},
		{"delete", `DELETE FROM items;`},
		{"insert", `INSERT INTO items(id,name,removed) VALUES(10,'private_insert',0);`},
		{"select", `SELECT 1;`},
		{"explain", `EXPLAIN UPDATE items SET removed=0;`},
		{"parameter", `UPDATE items SET removed=?;`},
		{"returning", `UPDATE items SET removed=0 RETURNING id;`},
		{"unsafe function", `UPDATE items SET removed=load_extension('private_extension');`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			provider := New()
			before, after := sqliteRequiredBackfillSchemas(t)
			database := openMigrationFixture(t, provider, before, "restricted-backfill.db")
			// Reuse the same physical connection after the failed companion so
			// leaking its authorizer cannot be hidden by borrowing another one.
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(1)
			seedSQLiteRequiredBackfill(t, database)
			manifest, files := sqliteRequiredBackfillManifest(t, before, after, []byte(test.sql+"\n"))
			err := provider.ApplyMigration(ctx, database, manifest, files)
			if err == nil || !strings.Contains(err.Error(), "restricted UPDATE contract") || strings.Contains(err.Error(), "private_") || strings.Contains(err.Error(), test.sql) {
				t.Fatalf("want sanitized restricted-companion refusal, got %v", err)
			}
			if err := provider.Verify(ctx, database, before); err != nil {
				t.Fatalf("refused companion escaped rollback: %v", err)
			}
			var counter int64
			if err := database.GetContext(ctx, &counter, `SELECT value FROM golem_row_version`); err != nil || counter != 57 {
				t.Fatalf("metadata changed: counter=%d error=%v", counter, err)
			}
			ledger, err := provider.ReadLedger(ctx, database)
			if err != nil || len(ledger) != 0 {
				t.Fatalf("refused companion advanced ledger=%#v error=%v", ledger, err)
			}
			if _, err := database.ExecContext(ctx, `UPDATE items SET name='restored' WHERE id=9`); err != nil {
				t.Fatalf("companion authorizer escaped its execution scope: %v", err)
			}
			assertSQLiteBackfillFTS(t, database, "restored", 1)
			assertForeignKeysState(t, database, 1)
		})
	}
}

func TestSQLiteReviewedBackfillAllowsPureFunctionsAndApplicationSubqueries(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before, after := sqliteRequiredBackfillSchemas(t)
	database := openMigrationFixture(t, provider, before, "backfill-expression.db")
	seedSQLiteRequiredBackfill(t, database)
	manifest, files := sqliteRequiredBackfillManifest(t, before, after, []byte("UPDATE items SET removed=(SELECT length(lower(source.name)) FROM items AS source WHERE source.id=items.id);\n"))
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var values []int64
	if err := database.SelectContext(ctx, &values, `SELECT removed FROM items ORDER BY id`); err != nil || !reflect.DeepEqual(values, []int64{7, 7}) {
		t.Fatalf("reviewed application expression values=%v error=%v", values, err)
	}
}

func TestSQLiteReviewedBackfillRefusesSemanticInputWithoutInvalidation(t *testing.T) {
	before, after := sqliteRequiredBackfillSchemas(t)
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "removed", Space: "text", Dimensions: 3, Fields: []string{string(fixtureItemNoteField)}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "70000000000000000000000000000003", Provider: ir.SQLite, Version: semanticcontract.Version, Owner: ir.ObjectID(fixtureItemTable), Kind: semanticcontract.IndexKind, Payload: payload}, migrationFixtureTable(t, after, fixtureItemTable))
	if err != nil {
		t.Fatal(err)
	}
	after.Extensions = append(after.Extensions, extension)
	after = normalizeMigrationFixture(t, after)
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{ID: "001_semantic_backfill", BeforeSnapshot: before, AfterSnapshot: after, Operations: plan.Operations}
	if _, err := planSQLiteReviewedBackfill(entry); err == nil || !strings.Contains(err.Error(), "semantic input") {
		t.Fatalf("semantic input filled outside runtime invalidation was accepted: %v", err)
	}
}

func sqliteRequiredBackfillSchemas(t *testing.T) (physical.PhysicalSchema, physical.PhysicalSchema) {
	t.Helper()
	before := foreignKeyMigrationFixtureSchema(t, false)
	versioned := rowVersionFixtureSchema(t, true)
	before.Unmanaged = versioned.Unmanaged
	name := fixtureItemNameField
	owner := migrationFixtureTable(t, versioned, fixtureItemTable)
	owner.Indexes = []physical.PhysicalIndex{{ID: "30000000000000000000000000000002", Name: "idx_items_name", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &name, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional}}
	for index := range before.Tables {
		if before.Tables[index].ID == fixtureItemTable {
			before.Tables[index] = owner
		}
	}
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingDiacritics, Fields: []fulltextcontract.Field{{ID: string(name), Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := fulltextstorage.Lower(ir.ProviderExtensionIR{ID: "70000000000000000000000000000002", Provider: ir.SQLite, Version: fulltextcontract.Version, Owner: ir.ObjectID(fixtureItemTable), Kind: fulltextcontract.IndexKind, Payload: payload}, owner)
	if err != nil {
		t.Fatal(err)
	}
	before.Extensions = []physical.Extension{extension}
	before = normalizeMigrationFixture(t, before)
	after := normalizeMigrationFixture(t, before)
	for index := range after.Tables {
		if after.Tables[index].ID == fixtureItemTable {
			after.Tables[index].Columns = append(after.Tables[index].Columns, physical.PhysicalColumn{ID: fixtureItemNoteField, Name: "removed", Ordinal: uint32(len(after.Tables[index].Columns)), Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
		}
	}
	return before, normalizeMigrationFixture(t, after)
}

func sqliteRequiredBackfillManifest(t *testing.T, before, after physical.PhysicalSchema, reviewed []byte) (migration.Manifest, map[string][]byte) {
	t.Helper()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	var operation migration.Operation
	for _, value := range plan.Operations {
		if value.Kind == migration.BackfillColumn {
			operation = value
		}
	}
	if operation.ID == "" {
		t.Fatal("required-column fixture lacks a semantic backfill")
	}
	path := "001_required_removed.backfill.sql"
	companion := migration.ManualCompanion{OperationID: operation.ID, File: migration.FileChecksum{Path: path, SHA256: migration.Checksum(reviewed)}, Postcondition: migration.BackfillPostcondition(fixtureItemTable, fixtureItemNoteField)}
	manifest, files := migrationFixtureManifest(t, before, after, "001_required_removed.sql", nil, companion)
	files[path] = reviewed
	if err := migration.VerifyManifest(manifest, files); err != nil {
		t.Fatal(err)
	}
	return manifest, files
}

func seedSQLiteRequiredBackfill(t *testing.T, database *sqlx.DB) {
	t.Helper()
	for _, statement := range []string{`INSERT INTO items(id,name,golem_version) VALUES (7,'retired',41),(9,'mailbox',57)`, `INSERT INTO children(id,item_id) VALUES (1,7)`, `UPDATE golem_row_version SET value=57`} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func assertSQLiteBackfillFTS(t *testing.T, database *sqlx.DB, query string, want int) {
	t.Helper()
	var count int
	if err := database.Get(&count, `SELECT count(*) FROM "_golem_fulltext_70000000000000000000000000000002_fts" WHERE "_golem_fulltext_70000000000000000000000000000002_fts" MATCH ?`, query); err != nil || count != want {
		t.Fatalf("FTS query=%q count=%d want=%d error=%v", query, count, want, err)
	}
}

func TestSQLiteIncrementalRebuildUsesStableFieldMapping(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, true)
	after := incrementalFixtureSchema(t, false)
	database := openMigrationFixture(t, provider, before, "rebuild.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name","note") VALUES (7,'kept',x'0102')`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_drop_note.sql", nil)
	plan, err := provider.PlanIncremental(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rebuilds) != 1 {
		t.Fatalf("got %d rebuilds, want 1", len(plan.Rebuilds))
	}
	mapping := plan.Rebuilds[0].Columns
	if len(mapping) != 2 || mapping[0].FieldID != fixtureItemIDField || mapping[1].FieldID != fixtureItemNameField {
		t.Fatalf("unexpected explicit mapping: %#v", mapping)
	}
	for _, step := range plan.steps {
		for _, statement := range step.statements {
			if strings.Contains(strings.ToUpper(statement), "SELECT *") {
				t.Fatalf("rebuild rendered SELECT *: %s", statement)
			}
		}
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := database.GetContext(ctx, &name, `SELECT "name" FROM "items" WHERE "id"=7`); err != nil {
		t.Fatal(err)
	}
	if name != "kept" {
		t.Fatalf("mapped row changed: %q", name)
	}
	var temporaryCount int
	if err := database.GetContext(ctx, &temporaryCount, `SELECT count(*) FROM sqlite_schema WHERE name LIKE '_golem_tmp_%'`); err != nil || temporaryCount != 0 {
		t.Fatalf("temporary table residue count=%d error=%v", temporaryCount, err)
	}
	assertForeignKeysState(t, database, 1)
}

func TestSQLiteIncrementalInitializesConcurrencyWithProviderLiteralOne(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := normalizeMigrationFixture(t, before)
	field := ir.FieldID("20000000000000000000000000000019")
	after.Tables[0].Columns = append(after.Tables[0].Columns, physical.PhysicalColumn{ID: field, Name: "version", Ordinal: uint32(len(after.Tables[0].Columns)), Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	after.Tables[0].OptimisticConcurrency = &field
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "concurrency.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name") VALUES (7,'kept')`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_concurrency.sql", nil)
	plan, err := provider.PlanIncremental(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rebuilds) != 1 {
		t.Fatalf("rebuilds=%d, want one provider-owned rebuild", len(plan.Rebuilds))
	}
	var initialized int
	for _, mapping := range plan.Rebuilds[0].Columns {
		if mapping.FieldID == field && mapping.InitializeConcurrency && mapping.Source == "" {
			initialized++
		}
	}
	if initialized != 1 {
		t.Fatalf("concurrency literal mappings=%d, want exactly one: %#v", initialized, plan.Rebuilds[0].Columns)
	}
	if sql := string(files["001_concurrency.sql"]); !strings.Contains(sql, `SELECT "id", "name", 1 FROM "items"`) || strings.Contains(sql, "DEFAULT 1") {
		t.Fatalf("SQLite concurrency migration did not use the sole literal-one copy path:\n%s", sql)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := database.GetContext(ctx, &version, `SELECT "version" FROM "items" WHERE "id"=7`); err != nil || version != 1 {
		t.Fatalf("version=%d error=%v, want exact provider-initialized 1", version, err)
	}
}

func TestSQLiteIncrementalFailureRollsBackSchemaLedgerAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := before
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	after.Tables[0].Columns = append([]physical.PhysicalColumn(nil), before.Tables[0].Columns...)
	after.Tables[0].Columns[1].Nullable = false
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "failure.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "items" ("id","name") VALUES (1,NULL)`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_require_name.sql", nil)
	err := provider.ApplyMigration(ctx, database, manifest, files)
	if err == nil || !strings.Contains(err.Error(), "NOT NULL") {
		t.Fatalf("got error %v, want failed rebuild copy", err)
	}
	ledger, readErr := provider.ReadLedger(ctx, database)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(ledger) != 0 {
		t.Fatalf("failed migration advanced ledger: %#v", ledger)
	}
	if _, introspectErr := provider.Introspect(ctx, database, before); introspectErr != nil {
		t.Fatalf("failed migration changed schema: %v", introspectErr)
	}
	var nulls int
	if err := database.GetContext(ctx, &nulls, `SELECT count(*) FROM "items" WHERE "name" IS NULL`); err != nil || nulls != 1 {
		t.Fatalf("failed migration changed rows count=%d error=%v", nulls, err)
	}
	assertForeignKeysState(t, database, 1)
}

func TestSQLiteIncrementalForeignKeyCheckFailureRollsBackLedger(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := foreignKeyMigrationFixtureSchema(t, true)
	after := foreignKeyMigrationFixtureSchema(t, false)
	database := openMigrationFixture(t, provider, before, "foreign-key-failure.db")
	connection, err := database.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO "children" ("id","item_id") VALUES (1,999)`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_rebuild_items.sql", nil)
	err = provider.ApplyMigration(ctx, database, manifest, files)
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("got error %v, want foreign_key_check failure", err)
	}
	ledger, readErr := provider.ReadLedger(ctx, database)
	if readErr != nil || len(ledger) != 0 {
		t.Fatalf("failed foreign-key check ledger=%#v error=%v", ledger, readErr)
	}
	if _, introspectErr := provider.Introspect(ctx, database, before); introspectErr != nil {
		t.Fatalf("failed foreign-key check changed schema: %v", introspectErr)
	}
	assertForeignKeysState(t, database, 1)
}

func TestSQLiteIncrementalRejectsTamperedLedgerChain(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := incrementalFixtureSchema(t, true)
	database := openMigrationFixture(t, provider, before, "tampered-ledger.db")
	manifest, files := migrationFixtureManifest(t, before, after, "001_add_note.sql", nil)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	tampered := strings.Repeat("a", 64)
	if _, err := database.ExecContext(ctx, `UPDATE "_golem_migrations" SET "chain_hash"=?`, tampered); err != nil {
		t.Fatal(err)
	}
	err := provider.ApplyMigration(ctx, database, manifest, files)
	if err == nil || !strings.Contains(err.Error(), "does not match reviewed chain") {
		t.Fatalf("got error %v, want exact ledger-chain rejection", err)
	}
}

func TestSQLiteIncrementalRebuildRefusesUnmanagedDependentTrigger(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, true)
	after := incrementalFixtureSchema(t, false)
	unmanaged := physical.UnmanagedObject{Kind: "trigger", Name: "external_items_trigger"}
	before.Unmanaged = []physical.UnmanagedObject{unmanaged}
	after.Unmanaged = []physical.UnmanagedObject{unmanaged}
	before = normalizeMigrationFixture(t, before)
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "unmanaged-trigger.db")
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER "external_items_trigger" AFTER UPDATE ON "items" BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_rebuild_items.sql", nil)
	err := provider.ApplyMigration(ctx, database, manifest, files)
	if err == nil || !strings.Contains(err.Error(), "refuses unmanaged dependent trigger") {
		t.Fatalf("got error %v, want unmanaged dependency refusal", err)
	}
	var triggerCount int
	if err := database.GetContext(ctx, &triggerCount, `SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name='external_items_trigger'`); err != nil || triggerCount != 1 {
		t.Fatalf("dependent trigger count=%d error=%v", triggerCount, err)
	}
	ledger, readErr := provider.ReadLedger(ctx, database)
	if readErr != nil || len(ledger) != 0 {
		t.Fatalf("refused rebuild ledger=%#v error=%v", ledger, readErr)
	}
}

func TestSQLiteIncrementalRenameRefusesUnmanagedDependentTrigger(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := before
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	after.Tables[0].Columns = append([]physical.PhysicalColumn(nil), before.Tables[0].Columns...)
	after.Tables[0].Columns[1].Name = "display_name"
	unmanaged := physical.UnmanagedObject{Kind: "trigger", Name: "external_items_trigger"}
	before.Unmanaged = []physical.UnmanagedObject{unmanaged}
	after.Unmanaged = []physical.UnmanagedObject{unmanaged}
	before = normalizeMigrationFixture(t, before)
	after = normalizeMigrationFixture(t, after)
	database := openMigrationFixture(t, provider, before, "unmanaged-rename.db")
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER "external_items_trigger" AFTER UPDATE ON "items" BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_rename_name.sql", nil)
	err := provider.ApplyMigration(ctx, database, manifest, files)
	if err == nil || !strings.Contains(err.Error(), "renameColumn") || !strings.Contains(err.Error(), "refuses unmanaged dependent trigger") {
		t.Fatalf("got error %v, want rename dependency refusal", err)
	}
	if _, introspectErr := provider.Introspect(ctx, database, before); introspectErr != nil {
		t.Fatalf("refused rename changed schema: %v", introspectErr)
	}
}

func TestSQLiteIncrementalRebuildPreservesRecursiveSelfForeignKey(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := recursiveMigrationFixtureSchema(t, true)
	after := recursiveMigrationFixtureSchema(t, false)
	database := openMigrationFixture(t, provider, before, "recursive.db")
	if _, err := database.ExecContext(ctx, `INSERT INTO "nodes" ("id","parent_id","note") VALUES (1,NULL,'root'),(2,1,'child')`); err != nil {
		t.Fatal(err)
	}
	manifest, files := migrationFixtureManifest(t, before, after, "001_rebuild_nodes.sql", nil)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var parent int64
	if err := database.GetContext(ctx, &parent, `SELECT "parent_id" FROM "nodes" WHERE "id"=2`); err != nil {
		t.Fatal(err)
	}
	if parent != 1 {
		t.Fatalf("recursive parent=%d want=1", parent)
	}
	if err := verifyForeignKeys(ctx, database); err != nil {
		t.Fatal(err)
	}
	var referencedTable string
	if err := database.GetContext(ctx, &referencedTable, `SELECT "table" FROM pragma_foreign_key_list('nodes') LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if referencedTable != "nodes" {
		t.Fatalf("self foreign key references %q", referencedTable)
	}
}

func TestSQLiteIncrementalConcurrentRunnersAdvanceLedgerOnce(t *testing.T) {
	ctx := context.Background()
	provider := New()
	before := incrementalFixtureSchema(t, false)
	after := incrementalFixtureSchema(t, true)
	database := openMigrationFixture(t, provider, before, "race.db")
	manifest, files := migrationFixtureManifest(t, before, after, "001_add_note.sql", nil)
	start := make(chan struct{})
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errors <- provider.ApplyMigration(ctx, database, manifest, files)
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	successes := 0
	for err := range errors {
		if err == nil {
			successes++
			continue
		}
		if !strings.Contains(err.Error(), "no unapplied") && !strings.Contains(err.Error(), "ledger changed") {
			t.Fatalf("unexpected concurrent runner error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful runners=%d, want exactly one", successes)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger entries=%d error=%v", len(ledger), err)
	}
}

func TestSQLiteIncrementalPlanningIsDeterministic(t *testing.T) {
	before := incrementalFixtureSchema(t, true)
	after := incrementalFixtureSchema(t, false)
	manifest, _ := migrationFixtureManifest(t, before, after, "001_drop_note.sql", nil)
	first, err := New().PlanIncremental(manifest.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 50; iteration++ {
		current, currentErr := New().PlanIncremental(manifest.Entries[0])
		if currentErr != nil {
			t.Fatal(currentErr)
		}
		if !reflect.DeepEqual(first, current) {
			t.Fatalf("planning changed at iteration %d", iteration)
		}
	}
}

func TestSQLiteIncrementalPlannerRefusesInventedCast(t *testing.T) {
	before := incrementalFixtureSchema(t, false)
	after := before
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	after.Tables[0].Columns = append([]physical.PhysicalColumn(nil), before.Tables[0].Columns...)
	after.Tables[0].Columns[1].Storage = physical.StorageType{Kind: physical.StorageSQLiteInteger}
	after = normalizeMigrationFixture(t, after)
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, _ := physical.PhysicalFingerprint(before)
	afterFingerprint, _ := physical.PhysicalFingerprint(after)
	entry := migration.ManifestEntry{
		ID: "001_change_type", Operations: plan.Operations, Phases: plan.Phases,
		BeforePhysical: migration.Digest(beforeFingerprint.String()), AfterPhysical: migration.Digest(afterFingerprint.String()),
		BeforeSnapshot: before, AfterSnapshot: after,
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	_, err = New().PlanIncremental(entry)
	if err == nil || !strings.Contains(err.Error(), "explicit reviewed cast") {
		t.Fatalf("got error %v, want cast refusal", err)
	}
}

func TestSQLiteReviewedInitialMigrationBootstrapsTrulyEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	provider := New()
	after := incrementalFixtureSchema(t, false)
	before := canonicalEmptyMigrationSchema(t, after)
	manifest, files := migrationFixtureManifest(t, before, after, "001_initial.sql", nil)
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "initial.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Introspect(ctx, database, after); err != nil {
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

func TestSQLiteReviewedInitialThenSecondIncrementalEntry(t *testing.T) {
	ctx := context.Background()
	provider := New()
	headOne := incrementalFixtureSchema(t, false)
	headTwo := incrementalFixtureSchema(t, true)
	empty := canonicalEmptyMigrationSchema(t, headOne)
	firstManifest, firstFiles := migrationFixtureManifest(t, empty, headOne, "001_initial.sql", nil)
	first := firstManifest.Entries[0]
	secondManifest, secondFiles := migrationFixtureManifest(t, headOne, headTwo, "002_add_note.sql", &first)
	manifest := firstManifest
	manifest.Entries = append(manifest.Entries, secondManifest.Entries[0])
	files := firstFiles
	for path, content := range secondFiles {
		files[path] = content
	}
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(ledger) != 2 {
		t.Fatalf("ledger entries=%d error=%v", len(ledger), err)
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Introspect(ctx, database, headTwo); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteReviewedInitialFailureRollsBackToEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	provider := New()
	after := incrementalFixtureSchema(t, false)
	before := canonicalEmptyMigrationSchema(t, after)
	manifest, files := migrationFixtureManifest(t, before, after, "001_initial.sql", nil)
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "initial-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var limit int
	if err := database.GetContext(ctx, &limit, "PRAGMA max_page_count = 1"); err != nil || limit != 1 {
		t.Fatalf("max_page_count=%d error=%v", limit, err)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil {
		t.Fatal("initial migration unexpectedly succeeded under one-page limit")
	}
	var objects int
	if err := database.GetContext(ctx, &objects, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'"); err != nil || objects != 0 {
		t.Fatalf("failed initial migration left %d objects, error=%v", objects, err)
	}
	assertForeignKeysState(t, database, 1)
}

func TestSQLiteReviewedInitialRejectsRewrittenManifestBeforeDDL(t *testing.T) {
	ctx := context.Background()
	provider := New()
	after := incrementalFixtureSchema(t, false)
	before := canonicalEmptyMigrationSchema(t, after)
	manifest, files := migrationFixtureManifest(t, before, after, "001_initial.sql", nil)
	files["001_initial.sql"] = []byte("rewritten")
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "rewritten.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil || !strings.Contains(err.Error(), "rewritten") {
		t.Fatalf("error=%v, want rewritten manifest refusal", err)
	}
	var objects int
	if err := database.GetContext(ctx, &objects, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'"); err != nil || objects != 0 {
		t.Fatalf("rewritten initial migration left %d objects, error=%v", objects, err)
	}
}

func TestSQLiteReviewedInitialConcurrentRunnersBootstrapOnce(t *testing.T) {
	ctx := context.Background()
	provider := New()
	after := incrementalFixtureSchema(t, false)
	before := canonicalEmptyMigrationSchema(t, after)
	manifest, files := migrationFixtureManifest(t, before, after, "001_initial.sql", nil)
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "initial-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	start := make(chan struct{})
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errors <- provider.ApplyMigration(ctx, database, manifest, files)
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	successes := 0
	for err := range errors {
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "no unapplied reviewed entry") {
			t.Fatalf("unexpected concurrent initial error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful initial runners=%d want=1", successes)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger entries=%d error=%v", len(ledger), err)
	}
}

const (
	fixtureItemTable     ir.ModelID = "10000000000000000000000000000001"
	fixtureItemIDField   ir.FieldID = "20000000000000000000000000000001"
	fixtureItemNameField ir.FieldID = "20000000000000000000000000000002"
	fixtureItemNoteField ir.FieldID = "20000000000000000000000000000003"
)

func incrementalFixtureSchema(t *testing.T, withNote bool) physical.PhysicalSchema {
	t.Helper()
	table := physical.PhysicalTable{
		ID:   fixtureItemTable,
		Name: "items",
		Columns: []physical.PhysicalColumn{
			{ID: fixtureItemIDField, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: fixtureItemNameField, Name: "name", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "30000000000000000000000000000001", Name: "pk_items", Columns: []ir.FieldID{fixtureItemIDField}},
	}
	if withNote {
		table.Columns = append(table.Columns, physical.PhysicalColumn{ID: fixtureItemNoteField, Name: "note", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StorageSQLiteBlob}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	}
	return normalizeMigrationFixture(t, physical.PhysicalSchema{
		Version:          physical.SchemaFormatVersion,
		CanonicalVersion: physical.CanonicalFormatVersion,
		Provider:         New().Manifest(),
		Namespace:        physical.Namespace{Name: "main"},
		System: physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{
			{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
			{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
		}},
		Tables: []physical.PhysicalTable{table},
	})
}

func foreignKeyMigrationFixtureSchema(t *testing.T, withNote bool) physical.PhysicalSchema {
	t.Helper()
	schema := incrementalFixtureSchema(t, withNote)
	childID := ir.FieldID("20000000000000000000000000000011")
	itemID := ir.FieldID("20000000000000000000000000000012")
	schema.Tables = append(schema.Tables, physical.PhysicalTable{
		ID:   "10000000000000000000000000000002",
		Name: "children",
		Columns: []physical.PhysicalColumn{
			{ID: childID, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: itemID, Name: "item_id", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		},
		PrimaryKey:  &physical.PhysicalKey{ID: "30000000000000000000000000000011", Name: "pk_children", Columns: []ir.FieldID{childID}},
		ForeignKeys: []physical.PhysicalForeignKey{{ID: "40000000000000000000000000000011", Name: "fk_children_item", Columns: []ir.FieldID{itemID}, ReferencedTable: fixtureItemTable, ReferencedColumns: []ir.FieldID{fixtureItemIDField}, OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionNoAction, Deferrable: ir.NotDeferrable}},
	})
	return normalizeMigrationFixture(t, schema)
}

func recursiveMigrationFixtureSchema(t *testing.T, withNote bool) physical.PhysicalSchema {
	t.Helper()
	nodeTable := ir.ModelID("10000000000000000000000000000021")
	nodeID := ir.FieldID("20000000000000000000000000000021")
	parentID := ir.FieldID("20000000000000000000000000000022")
	noteID := ir.FieldID("20000000000000000000000000000023")
	table := physical.PhysicalTable{
		ID: nodeTable, Name: "nodes",
		Columns: []physical.PhysicalColumn{
			{ID: nodeID, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: parentID, Name: "parent_id", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		},
		PrimaryKey:  &physical.PhysicalKey{ID: "30000000000000000000000000000021", Name: "pk_nodes", Columns: []ir.FieldID{nodeID}},
		ForeignKeys: []physical.PhysicalForeignKey{{ID: "40000000000000000000000000000021", Name: "fk_nodes_parent", Columns: []ir.FieldID{parentID}, ReferencedTable: nodeTable, ReferencedColumns: []ir.FieldID{nodeID}, OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionNoAction, Deferrable: ir.NotDeferrable}},
	}
	if withNote {
		table.Columns = append(table.Columns, physical.PhysicalColumn{ID: noteID, Name: "note", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	}
	base := incrementalFixtureSchema(t, false)
	base.Tables = []physical.PhysicalTable{table}
	return normalizeMigrationFixture(t, base)
}

func normalizeMigrationFixture(t *testing.T, schema physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	normalized, err := physical.Normalize(schema)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func canonicalEmptyMigrationSchema(t *testing.T, desired physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	empty := physical.PhysicalSchema{
		Version:          physical.SchemaFormatVersion,
		CanonicalVersion: physical.CanonicalFormatVersion,
		Provider:         desired.Provider,
		Namespace:        desired.Namespace,
	}
	return normalizeMigrationFixture(t, empty)
}

func openMigrationFixture(t *testing.T, provider *Provider, schema physical.PhysicalSchema, name string) *sqlx.DB {
	t.Helper()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(context.Background(), database, schema); err != nil {
		t.Fatal(err)
	}
	return database
}

func migrationFixtureManifest(t *testing.T, before, after physical.PhysicalSchema, filePath string, parent *migration.ManifestEntry, companions ...migration.ManualCompanion) (migration.Manifest, map[string][]byte) {
	t.Helper()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, _ := physical.PhysicalFingerprint(before)
	afterFingerprint, _ := physical.PhysicalFingerprint(after)
	allowlistFingerprint, _ := physical.UnmanagedAllowlistFingerprint(after)
	entry := migration.ManifestEntry{
		ID:                       migration.MigrationID(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))),
		Operations:               plan.Operations,
		Phases:                   plan.Phases,
		BeforeModel:              migration.Checksum([]byte("model:" + beforeFingerprint.String())),
		AfterModel:               migration.Checksum([]byte("model:" + afterFingerprint.String())),
		BeforePhysical:           migration.Digest(beforeFingerprint.String()),
		AfterPhysical:            migration.Digest(afterFingerprint.String()),
		BeforeSnapshot:           before,
		AfterSnapshot:            after,
		UnmanagedAllowlistDigest: migration.Digest(allowlistFingerprint.String()),
		Manual:                   append([]migration.ManualCompanion(nil), companions...),
	}
	if parent != nil {
		entry.ParentID = parent.ID
		entry.ParentChainHash = parent.ChainHash
		entry.BeforeModel = parent.AfterModel
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	script, err := New().RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(script.SQL())
	entry.Files = []migration.FileChecksum{{Path: filePath, SHA256: migration.Checksum(content)}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest := migration.Manifest{FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion, HashAlgorithm: "sha256", GeneratorVersion: "sqlite-test-v1", Provider: before.Provider, Entries: []migration.ManifestEntry{entry}}
	return manifest, map[string][]byte{filePath: content}
}

func assertForeignKeysState(t *testing.T, database *sqlx.DB, want int) {
	t.Helper()
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var got int
	if err := connection.GetContext(context.Background(), &got, "PRAGMA foreign_keys"); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("PRAGMA foreign_keys=%d want=%d", got, want)
	}
}

func migrationFixtureTable(t *testing.T, schema physical.PhysicalSchema, model ir.ModelID) physical.PhysicalTable {
	t.Helper()
	for _, table := range schema.Tables {
		if table.ID == model {
			return table
		}
	}
	t.Fatalf("fixture table %s is absent", model)
	return physical.PhysicalTable{}
}
