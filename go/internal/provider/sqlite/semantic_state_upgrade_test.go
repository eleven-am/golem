package sqlite

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/jmoiron/sqlx"
)

func semanticUpgradeExtension(t *testing.T, stateVersion uint16) physical.Extension {
	t.Helper()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 4, Fields: []string{"body"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	owner := physical.PhysicalTable{
		ID: "model-id", Name: "notes",
		Columns: []physical.PhysicalColumn{
			{ID: "tenant", Name: "tenant", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "serial", Name: "serial", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "model-primary", Name: "pk_notes", Columns: []ir.FieldID{"tenant", "serial"}},
	}
	extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "semanticid", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}, owner)
	if err != nil {
		t.Fatal(err)
	}
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

func TestSemanticStateUpgradeConvergesWithAFreshlyCreatedTable(t *testing.T) {
	before := semanticUpgradeExtension(t, semanticstorage.StateVersionIdentity)
	after := semanticUpgradeExtension(t, semanticstorage.StateVersionStrikes)

	original, err := renderSemanticExtension(before, false)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := renderSemanticExtension(after, false)
	if err != nil {
		t.Fatal(err)
	}
	upgrade, err := renderSemanticStateUpgrade(before, after)
	if err != nil {
		t.Fatal(err)
	}

	database := sqlx.MustOpen("sqlite", ":memory:")
	defer database.Close()
	if _, err := database.Exec(original[0]); err != nil {
		t.Fatalf("create original state table: %v", err)
	}
	for _, statement := range upgrade {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("apply upgrade %q: %v", statement, err)
		}
	}
	var altered string
	if err := database.Get(&altered, `SELECT "sql" FROM "main"."sqlite_master" WHERE "type"='table' AND "name"=?`, "_golem_semantic_semanticid_state"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(altered) != upgraded[0] {
		t.Fatalf("upgraded table does not converge with a freshly created one\naltered=%s\nfresh  =%s", altered, upgraded[0])
	}

	fresh := sqlx.MustOpen("sqlite", ":memory:")
	defer fresh.Close()
	if _, err := fresh.Exec(upgraded[0]); err != nil {
		t.Fatalf("create upgraded state table: %v", err)
	}
	var created string
	if err := fresh.Get(&created, `SELECT "sql" FROM "main"."sqlite_master" WHERE "type"='table' AND "name"=?`, "_golem_semantic_semanticid_state"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(created) != strings.TrimSpace(altered) {
		t.Fatalf("catalog text differs between upgrade and creation\ncreated=%s\naltered=%s", created, altered)
	}
}

func TestSemanticStateVersionOneRendersTheOriginalShape(t *testing.T) {
	before := semanticUpgradeExtension(t, semanticstorage.StateVersionIdentity)
	statements, err := renderSemanticExtension(before, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(statements[0], "ambiguous_strikes") {
		t.Fatalf("state version 1 rendered the strike column: %s", statements[0])
	}
	after := semanticUpgradeExtension(t, semanticstorage.StateVersionStrikes)
	statements, err = renderSemanticExtension(after, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statements[0], `"ambiguous_strikes" INTEGER NOT NULL DEFAULT 0 CHECK ("ambiguous_strikes" >= 0)`) {
		t.Fatalf("state version 2 did not render the strike column: %s", statements[0])
	}
	if strings.Index(statements[0], "ambiguous_strikes") > strings.Index(statements[0], "PRIMARY KEY") {
		t.Fatalf("strike column must precede the table constraints: %s", statements[0])
	}
}

func TestSemanticStateUpgradeRefusesUnregisteredTransitions(t *testing.T) {
	one := semanticUpgradeExtension(t, semanticstorage.StateVersionIdentity)
	two := semanticUpgradeExtension(t, semanticstorage.StateVersionStrikes)
	if _, err := renderSemanticStateUpgrade(two, one); err == nil {
		t.Fatal("downgrade was rendered")
	}
	if _, err := renderSemanticStateUpgrade(one, one); err == nil {
		t.Fatal("no-op transition was rendered")
	}
	widened := semanticUpgradeExtension(t, semanticstorage.StateVersionStrikes)
	for index := range widened.Attributes {
		if widened.Attributes[index].Name == "dimensions" {
			widened.Attributes[index].Value = physical.SemanticValue{Kind: physical.ValueInteger, Integer: 8}
		}
	}
	if _, err := renderSemanticStateUpgrade(one, widened); err == nil {
		t.Fatal("transition that also changed the vector contract was rendered")
	}
}
