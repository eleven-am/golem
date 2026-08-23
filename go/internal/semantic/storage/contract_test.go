package storage

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	physicalpkg "github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func TestLowerDecodeSemanticStorageRoundTrip(t *testing.T) {
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{"field-a", "field-b"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	extension := ir.ProviderExtensionIR{ID: "semantic-id", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}
	owner := physicalpkg.PhysicalTable{
		ID: "model-id", Name: "models",
		Columns: []physicalpkg.PhysicalColumn{
			{ID: "tenant", Name: "tenant", Storage: physicalpkg.StorageType{Kind: physicalpkg.StoragePostgreSQLVarchar, Length: 40}},
			{ID: "serial", Name: "serial", Ordinal: 1, Storage: physicalpkg.StorageType{Kind: physicalpkg.StoragePostgreSQLBigInt}},
		},
		PrimaryKey: &physicalpkg.PhysicalKey{ID: "model-primary", Name: "pk_models", Columns: []ir.FieldID{"tenant", "serial"}},
	}
	physical, err := Lower(extension, owner)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := Decode(physical)
	if err != nil {
		t.Fatal(err)
	}
	want := Descriptor{ID: "semantic-id", ModelID: "model-id", Name: "related", Space: "content", Dimensions: 3, Fields: []ir.FieldID{"field-a", "field-b"}, Metric: "cosine", Storage: "_golem_semantic_semantic-id", Identity: []IdentityColumn{
		{Name: "tenant", Storage: physicalpkg.StorageType{Kind: physicalpkg.StoragePostgreSQLVarchar, Length: 40}, NotNull: true},
		{Name: "serial", Storage: physicalpkg.StorageType{Kind: physicalpkg.StoragePostgreSQLBigInt}, NotNull: true},
	}}
	if !reflect.DeepEqual(descriptor, want) {
		t.Fatalf("descriptor=%#v", descriptor)
	}
	physical.Attributes[0].Value.Integer = 0
	if _, err := Decode(physical); err == nil {
		t.Fatal("invalid physical semantic descriptor accepted")
	}
}

// TestDecodeAcceptsRetainedSixAttributeSnapshots keeps signed histories
// replayable: snapshots written before the identity projection existed decode
// with an empty Identity, while a current compile must carry it.
func TestDecodeAcceptsRetainedSixAttributeSnapshots(t *testing.T) {
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{"field-a"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	owner := physicalpkg.PhysicalTable{
		ID: "model-id", Name: "models",
		Columns:    []physicalpkg.PhysicalColumn{{ID: "key", Name: "key", Storage: physicalpkg.StorageType{Kind: physicalpkg.StorageSQLiteText}}},
		PrimaryKey: &physicalpkg.PhysicalKey{ID: "model-primary", Name: "pk_models", Columns: []ir.FieldID{"key"}},
	}
	current, err := Lower(ir.ProviderExtensionIR{ID: "semantic-id", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}, owner)
	if err != nil {
		t.Fatal(err)
	}
	legacy := current
	legacy.Attributes = nil
	for _, attribute := range current.Attributes {
		if attribute.Name != attributeIdentity {
			legacy.Attributes = append(legacy.Attributes, attribute)
		}
	}
	retained, err := Decode(legacy)
	if err != nil || len(retained.Identity) != 0 {
		t.Fatalf("retained snapshot identity=%#v error=%v", retained.Identity, err)
	}
	compiled, err := Decode(current)
	if err != nil || len(compiled.Identity) != 1 || compiled.Identity[0].Name != "key" || compiled.Identity[0].Storage.Kind != physicalpkg.StorageSQLiteText || !compiled.Identity[0].NotNull {
		t.Fatalf("compiled identity=%#v error=%v", compiled.Identity, err)
	}
	blank := current
	blank.Attributes = append([]physicalpkg.Attribute(nil), current.Attributes...)
	for index := range blank.Attributes {
		if blank.Attributes[index].Name == attributeIdentity {
			blank.Attributes[index].Value = physicalpkg.SemanticValue{Kind: physicalpkg.ValueString, String: "key:sqlite.text:0:0:0"}
		}
	}
	if _, err := Decode(blank); err == nil {
		t.Fatal("malformed identity attribute was accepted")
	}
	if _, err := Lower(ir.ProviderExtensionIR{ID: "semantic-id", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}, physicalpkg.PhysicalTable{ID: "model-id", Name: "models"}); err == nil {
		t.Fatal("owner without a physical primary identity was accepted")
	}
}
