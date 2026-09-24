package sqlite

import (
	"bytes"
	"context"
	"reflect"
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

func TestSemanticStateUpgradePreservesVectorsAndRows(t *testing.T) {
	database := openVectorDatabase(t)
	before := semanticUpgradeExtension(t, semanticstorage.StateVersionIdentity)
	after := semanticUpgradeExtension(t, semanticstorage.StateVersionStrikes)

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

	var stored []byte
	if err := database.Get(&stored, `SELECT "embedding" FROM "_golem_semantic_semanticid_vec" WHERE "record_key"='k'`); err != nil {
		t.Fatalf("the vector did not survive the upgrade: %v", err)
	}
	if !bytes.Equal(stored, vector) {
		t.Fatalf("the stored vector changed across the upgrade: got %x want %x", stored, vector)
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
	if row.Status != "ready" || row.Attempts != 3 || row.Updated != 77 || row.Tenant != "acme" {
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
	want := []string{`ALTER TABLE "_golem_semantic_` + id(70) + `_state" ADD COLUMN ` + semanticStrikeColumnDefinition}
	if !reflect.DeepEqual(script.statements, want) {
		t.Fatalf("reviewed migration statements=%#v want=%#v", script.statements, want)
	}
}
