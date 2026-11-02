package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
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
	if err == nil || !strings.Contains(err.Error(), "SQLITE_VEC_LEGACY_STATE") || !strings.Contains(err.Error(), "go/v0.7.2") {
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
