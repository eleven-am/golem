package storage

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	physicalpkg "github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func stateVersionOwner() physicalpkg.PhysicalTable {
	return physicalpkg.PhysicalTable{
		ID: "model-id", Name: "models",
		Columns:    []physicalpkg.PhysicalColumn{{ID: "key", Name: "key", Storage: physicalpkg.StorageType{Kind: physicalpkg.StorageSQLiteText}}},
		PrimaryKey: &physicalpkg.PhysicalKey{ID: "model-primary", Name: "pk_models", Columns: []ir.FieldID{"key"}},
	}
}

func stateVersionExtension(t *testing.T) physicalpkg.Extension {
	t.Helper()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{"field-a"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	lowered, err := Lower(ir.ProviderExtensionIR{ID: "semantic-id", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}, stateVersionOwner())
	if err != nil {
		t.Fatal(err)
	}
	return lowered
}

func withoutAttribute(extension physicalpkg.Extension, name string) physicalpkg.Extension {
	result := extension
	result.Attributes = nil
	for _, attribute := range extension.Attributes {
		if attribute.Name != name {
			result.Attributes = append(result.Attributes, attribute)
		}
	}
	return result
}

func withAttribute(extension physicalpkg.Extension, name string, value physicalpkg.SemanticValue) physicalpkg.Extension {
	result := withoutAttribute(extension, name)
	result.Attributes = append(result.Attributes, physicalpkg.Attribute{Name: name, Value: value})
	return result
}

func TestLowerCarriesCurrentShadowStateVersion(t *testing.T) {
	extension := stateVersionExtension(t)
	if len(extension.Attributes) != 8 {
		t.Fatalf("lowered attribute count=%d want 8", len(extension.Attributes))
	}
	descriptor, err := Decode(extension)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.StateVersion != StateVersionCurrent {
		t.Fatalf("descriptor state version=%d want %d", descriptor.StateVersion, StateVersionCurrent)
	}
	if StateVersionCurrent != 3 {
		t.Fatalf("current semantic storage version=%d want 3", StateVersionCurrent)
	}
}

func TestDecodeTreatsAbsentStateVersionAsTheOriginalShape(t *testing.T) {
	current := stateVersionExtension(t)
	retained := withoutAttribute(current, attributeStateVersion)
	descriptor, err := Decode(retained)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.StateVersion != 1 {
		t.Fatalf("retained seven-attribute state version=%d want 1", descriptor.StateVersion)
	}
	if len(descriptor.Identity) != 1 {
		t.Fatalf("retained seven-attribute identity=%#v", descriptor.Identity)
	}
	legacy := withoutAttribute(retained, attributeIdentity)
	descriptor, err = Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.StateVersion != 1 || len(descriptor.Identity) != 0 {
		t.Fatalf("retained six-attribute descriptor=%#v", descriptor)
	}
}

func TestDecodeRefusesUnregisteredOrUngroundedStateVersion(t *testing.T) {
	current := stateVersionExtension(t)
	for name, extension := range map[string]physicalpkg.Extension{
		"unregistered version": withAttribute(current, attributeStateVersion, physicalpkg.SemanticValue{Kind: physicalpkg.ValueInteger, Integer: 4}),
		"zero version":         withAttribute(current, attributeStateVersion, physicalpkg.SemanticValue{Kind: physicalpkg.ValueInteger, Integer: 0}),
		"negative version":     withAttribute(current, attributeStateVersion, physicalpkg.SemanticValue{Kind: physicalpkg.ValueInteger, Integer: -1}),
		"wrong value kind":     withAttribute(current, attributeStateVersion, physicalpkg.SemanticValue{Kind: physicalpkg.ValueString, String: "2"}),
		"version without identity": withoutAttribute(
			withAttribute(current, attributeStateVersion, physicalpkg.SemanticValue{Kind: physicalpkg.ValueInteger, Integer: 2}),
			attributeIdentity),
	} {
		if _, err := Decode(extension); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestLowerRejectsTheShadowStrikeColumnAsAnIdentityName(t *testing.T) {
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{"field-a"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	extension := ir.ProviderExtensionIR{ID: "semantic-id", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}
	for _, name := range []physicalpkg.PhysicalName{"ambiguous_strikes", "AMBIGUOUS_STRIKES"} {
		owner := physicalpkg.PhysicalTable{
			ID: "model-id", Name: "models",
			Columns:    []physicalpkg.PhysicalColumn{{ID: "key", Name: name, Storage: physicalpkg.StorageType{Kind: physicalpkg.StorageSQLiteText}}},
			PrimaryKey: &physicalpkg.PhysicalKey{ID: "model-primary", Name: "pk_models", Columns: []ir.FieldID{"key"}},
		}
		if _, err := Lower(extension, owner); err == nil {
			t.Fatalf("reserved identity column %q was accepted", name)
		}
	}
}
