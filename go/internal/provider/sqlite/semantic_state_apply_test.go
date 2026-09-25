package sqlite

import (
	"context"
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

func openVectorDatabase(t *testing.T) *sqlx.DB {
	t.Helper()
	handle, err := sqlitevec.Open("file:" + t.TempDir() + "/semantic-upgrade.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	database := sqlx.NewDb(handle, "sqlite")
	t.Cleanup(func() { database.Close() })
	return database
}

func TestSemanticExactVectorUpgradeInvalidatesLegacyVectorsAndPreservesRows(t *testing.T) {
	database := openVectorDatabase(t)
	before := semanticUpgradeExtension(t, semanticstorage.StateVersionStrikes)
	after := semanticUpgradeExtension(t, semanticstorage.StateVersionExactVectors)

	original, err := renderSemanticExtension(before, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range original {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("create original semantic storage %q: %v", statement, err)
		}
	}

	vector, err := sqlitevec.Serialize([]float32{1, 0, 0, 0}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO "_golem_semantic_semanticid_state" ("record_key","source_hash","space_fingerprint","status","attempt_count","error_code","updated_at","tenant","serial") VALUES ('k',X'0102','fp','ready',3,NULL,77,'acme',9)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO "_golem_semantic_semanticid_vec" ("record_key","embedding") VALUES ('k',?)`, vector); err != nil {
		t.Fatal(err)
	}

	upgrade, err := renderSemanticStateUpgrade(before, after)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range upgrade {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("apply upgrade %q: %v", statement, err)
		}
	}

	var vectors int
	if err := database.Get(&vectors, `SELECT COUNT(*) FROM "_golem_semantic_semanticid_vec"`); err != nil {
		t.Fatal(err)
	}
	if vectors != 0 {
		t.Fatalf("upgrade retained %d vectors produced under the old embedding contract", vectors)
	}
	var vectorDDL string
	if err := database.Get(&vectorDDL, `SELECT "sql" FROM "main"."sqlite_master" WHERE "type"='table' AND "name"='_golem_semantic_semanticid_vec'`); err != nil {
		t.Fatal(err)
	}
	fresh, err := renderSemanticExtension(after, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(vectorDDL) != fresh[len(fresh)-1] {
		t.Fatalf("upgraded vector table does not converge with a fresh table\nupgraded=%s\nfresh   =%s", vectorDDL, fresh[len(fresh)-1])
	}
	if _, err := database.Exec(`INSERT INTO "_golem_semantic_semanticid_vec" ("record_key","embedding") VALUES ('wrong',X'00')`); err == nil {
		t.Fatal("exact vector storage accepted the wrong dimensions")
	}

	var row struct {
		Status   string `db:"status"`
		Attempts int64  `db:"attempt_count"`
		Strikes  int64  `db:"ambiguous_strikes"`
		Updated  int64  `db:"updated_at"`
		Tenant   string `db:"tenant"`
	}
	if err := database.Get(&row, `SELECT "status","attempt_count","ambiguous_strikes","updated_at","tenant" FROM "_golem_semantic_semanticid_state" WHERE "record_key"='k'`); err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" || row.Attempts != 0 || row.Updated != 77 || row.Tenant != "acme" {
		t.Fatalf("existing shadow row changed across the upgrade: %+v", row)
	}
	if row.Strikes != 0 {
		t.Fatalf("existing shadow row did not adopt a zero strike count: %+v", row)
	}
}

func TestSemanticStateUpgradeIsRenderedByTheReviewedMigrationPipeline(t *testing.T) {
	provider := New()
	model := socialModelIR()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{id(21)}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	model.Extensions = append(model.Extensions, ir.ProviderExtensionIR{ID: ir.ExtensionID(id(70)), Provider: ir.SQLite, Version: semanticcontract.Version, Owner: ir.ObjectID(id(2)), Kind: semanticcontract.IndexKind, Payload: payload})
	after, err := provider.Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := after
	before.Extensions = append([]physical.Extension(nil), after.Extensions...)
	attributes := make([]physical.Attribute, 0, len(after.Extensions[0].Attributes))
	for _, attribute := range after.Extensions[0].Attributes {
		if attribute.Name != "state_version" {
			attributes = append(attributes, attribute)
		}
	}
	before.Extensions[0].Attributes = attributes

	plan, err := migration.DiffReviewed(before, after)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{
		ID: "0007_semantic_strikes", BeforeSnapshot: before, AfterSnapshot: after,
		Operations: plan.Operations, Phases: plan.Phases,
		BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
	}
	script, err := provider.RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	base := `_golem_semantic_` + id(70)
	want := []string{
		`ALTER TABLE "` + base + `_state" ADD COLUMN ` + semanticStrikeColumnDefinition,
		`UPDATE "` + base + `_state" SET "status"='pending', "attempt_count"=0, "error_code"=NULL, "ambiguous_strikes"=0`,
		`CREATE TABLE "` + base + `_vec_exact_upgrade" ("record_key" TEXT NOT NULL PRIMARY KEY, "embedding" BLOB NOT NULL, CHECK (length("embedding") = 12)) STRICT`,
		`DROP TABLE "` + base + `_vec"`,
		`ALTER TABLE "` + base + `_vec_exact_upgrade" RENAME TO "` + base + `_vec"`,
	}
	if !reflect.DeepEqual(script.statements, want) {
		t.Fatalf("reviewed migration statements=%#v want=%#v", script.statements, want)
	}
}
