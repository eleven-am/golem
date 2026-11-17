package ir

import (
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"testing"
)

func TestDataIncarnationRequiresClosedCompleteProviderInventory(t *testing.T) {
	model := ModelIR{Schema: SchemaIdentityIR{ID: "10000000000000000000000000000000"}, Providers: []Provider{SQLite, PostgreSQL}}
	contract := ContractIR{DataIncarnation: &DataIncarnationContractIR{Kind: "provider", Version: 1}}
	for _, provider := range model.Providers {
		identity, _ := NewIDRegistry().Register(ObjectExtension, OwnedIdentity(string(model.Schema.ID), dataincarnation.Kind+"\x00"+string(provider)), SourceSpan{})
		model.Extensions = append(model.Extensions, ProviderExtensionIR{ID: ExtensionIDFrom(identity), Provider: provider, Version: 1, Owner: ObjectID(model.Schema.ID), Kind: dataincarnation.Kind, Payload: "{}"})
	}
	if err := ValidateDataIncarnation(CompilationIR{Model: model, Contract: contract}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ModelIR){
		func(m *ModelIR) { m.Extensions = m.Extensions[:1] },
		func(m *ModelIR) { m.Extensions[0].Payload = `{"ignored":true}` },
		func(m *ModelIR) { m.Extensions[0].Owner = "foreign" },
		func(m *ModelIR) { m.Extensions[0].ID = "foreign" },
		func(m *ModelIR) { m.Extensions = append(m.Extensions, m.Extensions[0]) },
	} {
		copy := model
		copy.Extensions = append([]ProviderExtensionIR(nil), model.Extensions...)
		mutate(&copy)
		if _, err := ModelDataIncarnation(copy); err == nil {
			t.Fatal("invalid inventory accepted")
		}
	}
	if err := ValidateDataIncarnation(CompilationIR{Model: model}); err == nil {
		t.Fatal("missing contract projection accepted")
	}
	page := pageContractFixture()
	page.DataIncarnation = nil
	if err := ValidateGraphQLTypeDefinitions(page); err == nil {
		t.Fatal("page without opt-in accepted")
	}
}
