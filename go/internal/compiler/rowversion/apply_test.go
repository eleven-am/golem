package rowversion

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func compilation(subscriptions bool, fields ...ir.FieldIR) *ir.CompilationIR {
	contract := ir.ModelContractIR{ModelID: "m1", Subscriptions: subscriptions}
	for _, field := range fields {
		contract.Fields = append(contract.Fields, ir.FieldContractIR{FieldID: field.ID, GraphQLName: field.GoName, Modes: []ir.FieldMode{ir.ModeVisible}})
	}
	return &ir.CompilationIR{
		Model:    ir.ModelIR{Models: []ir.ModelDeclIR{{ID: "m1", LogicalName: "Post", Fields: fields}}},
		Contract: ir.ContractIR{Models: []ir.ModelContractIR{contract}},
	}
}

func scalarField(id, goName, column string) ir.FieldIR {
	return ir.FieldIR{ID: ir.FieldID(id), GoName: goName, LogicalName: goName, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: ir.SQLIdentifier(column), Type: ir.LogicalTypeIR{Kind: ir.TypeString}}}
}

func TestApplyAddsReadOnlyVersionToSubscriptionModelsOnly(t *testing.T) {
	subscribed := compilation(true, scalarField("f1", "Title", "title"))
	if diagnostics := Apply(subscribed); len(diagnostics) != 0 {
		t.Fatalf("diagnostics: %v", diagnostics)
	}
	model := subscribed.Model.Models[0]
	if len(model.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(model.Fields))
	}
	added := model.Fields[1]
	if !ir.IsRowVersionField(added) || !added.Scalar.DatabaseReadOnly || added.Scalar.Nullable || added.Scalar.Type.Kind != ir.TypeInt64 || added.Scalar.Default == nil {
		t.Fatalf("unexpected row version field: %+v", added.Scalar)
	}
	contract := subscribed.Contract.Models[0].Fields[1]
	if contract.FieldID != added.ID || contract.GraphQLName != "_version" || !ir.HasMode(contract.Modes, ir.ModeReadOnly) || !ir.ModesReadable(contract.Modes) {
		t.Fatalf("unexpected contract: %+v", contract)
	}
	plain := compilation(false, scalarField("f1", "Title", "title"))
	if diagnostics := Apply(plain); len(diagnostics) != 0 || len(plain.Model.Models[0].Fields) != 1 {
		t.Fatalf("model without subscriptions changed: %v", diagnostics)
	}
}

func TestApplyRejectsUserDeclaredReservedNames(t *testing.T) {
	cases := map[string]ir.FieldIR{
		"column":       scalarField("f2", "Other", "golem_version"),
		"legacyColumn": scalarField("f2", "Other", "_golem_version"),
		"shortColumn":  scalarField("f2", "Other", "_version"),
		"goName":       scalarField("f2", "GolemVersion", "other"),
		"graphql":      scalarField("f2", "_version", "other"),
	}
	for name, field := range cases {
		for _, subscriptions := range []bool{true, false} {
			input := compilation(subscriptions, scalarField("f1", "Title", "title"), field)
			if name == "graphql" {
				input.Contract.Models[0].Fields[1].GraphQLName = "_version"
				input.Model.Models[0].Fields[1].GoName = "Other"
				input.Model.Models[0].Fields[1].LogicalName = "Other"
			}
			diagnostics := Apply(input)
			if len(diagnostics) == 0 || diagnostics[0].Code != "P1_ROW_VERSION_RESERVED" {
				t.Fatalf("%s subscriptions=%v: diagnostics = %v", name, subscriptions, diagnostics)
			}
		}
	}
}
