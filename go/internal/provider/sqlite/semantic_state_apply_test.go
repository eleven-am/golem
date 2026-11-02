package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/jmoiron/sqlx"
)

func withSemanticStateVersion(extension physical.Extension, stateVersion uint16) physical.Extension {
	attributes := make([]physical.Attribute, 0, len(extension.Attributes))
	for _, attribute := range extension.Attributes {
		if attribute.Name == "state_version" {
			if stateVersion == semanticstorage.StateVersionIdentity {
				continue
			}
			attribute.Value = physical.SemanticValue{Kind: physical.ValueInteger, Integer: int64(stateVersion)}
		}
		attributes = append(attributes, attribute)
	}
	extension.Attributes = attributes
	return extension
}

func legacyStateFixture(t *testing.T, stateVersion uint16) (physical.PhysicalSchema, physical.PhysicalSchema) {
	t.Helper()
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
	after.Extensions = []physical.Extension{withSemanticStateVersion(extension, stateVersion)}
	return before, normalizeMigrationFixture(t, after)
}

func openBlankFixture(t *testing.T, provider *Provider, name string) *sqlx.DB {
	t.Helper()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func requireLegacyStateRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "SQLITE_VEC_LEGACY_STATE") || !strings.Contains(err.Error(), "go/v0.7.1") {
		t.Fatalf("legacy semantic state error = %v", err)
	}
}

func TestSQLiteRefusesToCreateLegacySemanticVectorState(t *testing.T) {
	for _, stateVersion := range []uint16{semanticstorage.StateVersionIdentity, semanticstorage.StateVersionStrikes} {
		ctx := context.Background()
		provider := New()
		before, after := legacyStateFixture(t, stateVersion)
		requireLegacyStateRefusal(t, provider.ApplyInitial(ctx, openBlankFixture(t, provider, "legacy-initial.db"), after))

		database := openMigrationFixture(t, provider, before, "legacy-incremental.db")
		manifest, files := migrationFixtureManifest(t, before, after, "001_legacy_semantic_index.sql", nil)
		script, err := provider.RenderMigration(manifest.Entries[0])
		if err != nil || !strings.Contains(script.SQL(), "USING vec0(") {
			t.Fatalf("reviewed history no longer renders its legacy vec0 DDL: %v\n%s", err, script.SQL())
		}
		requireLegacyStateRefusal(t, provider.ApplyMigration(ctx, database, manifest, files))
		var vectors int
		if err := database.GetContext(ctx, &vectors, `SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE '%_vec%'`); err != nil || vectors != 0 {
			t.Fatalf("refused migration left %d vector objects: %v", vectors, err)
		}
		requireLegacyStateRefusal(t, provider.Verify(ctx, database, after))
	}
}

func TestSQLiteVerifyRefusesALegacyVec0VectorTable(t *testing.T) {
	ctx := context.Background()
	provider := New()
	_, after := legacyStateFixture(t, semanticstorage.StateVersionExactVectors)
	database := openBlankFixture(t, provider, "legacy-vec0.db")
	if err := provider.ApplyInitial(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatal(err)
	}
	vectors := "_golem_semantic_70000000000000000000000000000001_vec"
	for _, statement := range []string{
		`DROP TABLE "` + vectors + `"`,
		`PRAGMA writable_schema=ON`,
		`INSERT INTO sqlite_schema (type,name,tbl_name,rootpage,sql) VALUES ('table','` + vectors + `','` + vectors + `',0,'CREATE VIRTUAL TABLE "` + vectors + `" USING vec0(record_key TEXT PRIMARY KEY, embedding float[3] distance_metric=cosine)')`,
		`PRAGMA writable_schema=OFF`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	requireLegacyStateRefusal(t, provider.Verify(ctx, database, after))
}

func chainMigrationFixtures(t *testing.T, heads ...physical.PhysicalSchema) (migration.Manifest, map[string][]byte) {
	t.Helper()
	var manifest migration.Manifest
	files := map[string][]byte{}
	for index := 1; index < len(heads); index++ {
		var parent *migration.ManifestEntry
		if index > 1 {
			parent = &manifest.Entries[len(manifest.Entries)-1]
		}
		next, nextFiles := migrationFixtureManifest(t, heads[index-1], heads[index], fmt.Sprintf("%03d_history.sql", index), parent)
		if index == 1 {
			manifest = next
		} else {
			manifest.Entries = append(manifest.Entries, next.Entries[0])
		}
		for path, content := range nextFiles {
			files[path] = content
		}
	}
	return manifest, files
}

func TestSQLiteBootstrapsAFreshDatabaseThroughLegacySemanticStateHistory(t *testing.T) {
	ctx := context.Background()
	provider := New()
	_, stateOne := legacyStateFixture(t, semanticstorage.StateVersionIdentity)
	_, stateTwo := legacyStateFixture(t, semanticstorage.StateVersionStrikes)
	_, stateThree := legacyStateFixture(t, semanticstorage.StateVersionExactVectors)
	manifest, files := chainMigrationFixtures(t, canonicalEmptyMigrationSchema(t, stateOne), stateOne, stateTwo, stateThree)
	if len(manifest.Entries) != 3 {
		t.Fatalf("history has %d entries", len(manifest.Entries))
	}
	reviewed := string(files["001_history.sql"])
	if !strings.Contains(reviewed, "USING vec0(") {
		t.Fatalf("reviewed bootstrap SQL no longer records the legacy vec0 table:\n%s", reviewed)
	}
	database := openBlankFixture(t, provider, "legacy-history.db")
	vectors := "_golem_semantic_70000000000000000000000000000001_vec"
	for step := range manifest.Entries {
		if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
			t.Fatalf("history entry %d: %v", step+1, err)
		}
		var ddl string
		if err := database.GetContext(ctx, &ddl, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, vectors); err != nil {
			t.Fatalf("history entry %d vector table: %v", step+1, err)
		}
		if step < 2 && ddl != `CREATE TABLE "`+vectors+`" ("record_key" TEXT PRIMARY KEY, "embedding" BLOB)` {
			t.Fatalf("history entry %d vector table is not the legacy placeholder: %s", step+1, ddl)
		}
		if step == 2 && ddl != renderExactVectorTable(physical.PhysicalName(vectors), 3) {
			t.Fatalf("final vector table is not exact: %s", ddl)
		}
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || migration.VerifyLedger(manifest, ledger) != nil {
		t.Fatalf("ledger=%d error=%v", len(ledger), err)
	}
	if err := provider.Verify(ctx, database, stateThree); err != nil {
		t.Fatal(err)
	}
	var legacyObjects int
	if err := database.GetContext(ctx, &legacyObjects, `SELECT COUNT(*) FROM sqlite_schema WHERE sql LIKE '%vec0%' OR name LIKE '%\_exact\_upgrade' ESCAPE '\'`); err != nil || legacyObjects != 0 {
		t.Fatalf("legacy vector objects=%d error=%v", legacyObjects, err)
	}
	for key, values := range map[string][]float32{"near": {1, 0.1, 0}, "far": {0, 1, 0}, "exact": {2, 0, 0}} {
		encoded, err := sqlitevec.Serialize(values, 3)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO "`+vectors+`" (record_key,embedding) VALUES (?,?)`, key, encoded); err != nil {
			t.Fatal(err)
		}
	}
	query, _ := sqlitevec.Serialize([]float32{1, 0, 0}, 3)
	var ranked []string
	if err := database.SelectContext(ctx, &ranked, `SELECT record_key FROM "`+vectors+`" ORDER BY vec1_cos_distance(embedding,?),record_key`, query); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ranked, []string{"exact", "near", "far"}) {
		t.Fatalf("ranked=%v", ranked)
	}
	if err := provider.Verify(ctx, database, stateThree); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteMigrationRefusesADatabaseHoldingALiveVec0Table(t *testing.T) {
	ctx := context.Background()
	provider := New()
	_, stateThree := legacyStateFixture(t, semanticstorage.StateVersionExactVectors)
	database := openBlankFixture(t, provider, "live-vec0.db")
	for _, statement := range []string{
		`PRAGMA writable_schema=ON`,
		`INSERT INTO sqlite_schema (type,name,tbl_name,rootpage,sql) VALUES ('table','legacy_vec','legacy_vec',0,'CREATE VIRTUAL TABLE "legacy_vec" USING vec0(record_key TEXT PRIMARY KEY, embedding float[3] distance_metric=cosine)')`,
		`PRAGMA writable_schema=OFF`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	manifest, files := chainMigrationFixtures(t, canonicalEmptyMigrationSchema(t, stateThree), stateThree)
	requireLegacyStateRefusal(t, provider.ApplyMigration(ctx, database, manifest, files))
}
