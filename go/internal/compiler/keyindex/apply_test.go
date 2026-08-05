package keyindex

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestApplyFragmentsIsAtomicOnValidationFailure(t *testing.T) {
	_, base, ids := commonFixture()
	original := base
	fragment := ModelFragment{
		ModelID:   base.Model.Models[0].ID,
		Uniques:   []ir.KeyIR{{ID: "unique", Kind: ir.KeyUnique, Fields: []ir.FieldID{ids.handle}}},
		Selectors: []ir.SelectorContractIR{{KeyID: "wrong-key", Kind: ir.KeyUnique, Name: "Handle", Fields: []ir.FieldID{ids.handle}}},
		Generated: []GeneratedAssignment{{
			FieldID: "missing",
			Generation: ir.GeneratedColumnIR{
				Expr: ir.SchemaExprIR{Kind: "value"},
			},
		}},
	}
	if diagnostics := ApplyFragments(&base, []ModelFragment{fragment}); len(diagnostics) == 0 {
		t.Fatal("expected invalid generated assignment to fail")
	}
	if !reflect.DeepEqual(base, original) {
		t.Fatalf("failed application partially mutated ModelIR:\ngot %#v\nwant %#v", base, original)
	}
}

func TestApplyGeneratedMarksPersistenceReadOnly(t *testing.T) {
	_, base, ids := commonFixture()
	fragment := ModelFragment{ModelID: base.Model.Models[0].ID, Generated: []GeneratedAssignment{{
		FieldID:    ids.handle,
		Generation: ir.GeneratedColumnIR{Expr: ir.SchemaExprIR{Kind: "field"}, Storage: ir.GeneratedStored, Provider: ir.ProviderScopePortable},
	}}}
	if diagnostics := ApplyFragments(&base, []ModelFragment{fragment}); len(diagnostics) != 0 {
		t.Fatalf("apply diagnostics: %#v", diagnostics)
	}
	for _, field := range base.Model.Models[0].Fields {
		if field.ID == ids.handle && (field.Scalar.Generation == nil || !field.Scalar.DatabaseReadOnly) {
			t.Fatalf("generated field was not made persistence-read-only: %#v", field)
		}
	}
}
