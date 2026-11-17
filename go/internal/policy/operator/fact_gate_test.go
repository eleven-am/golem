package operator

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestOnlyResolvedFactsCanValidateForExecution(t *testing.T) {
	model := ir.ModelID{1}
	id, err := ir.FactIdentity(model, "membership")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := ir.NewFactReference(model, id)
	if err != nil {
		t.Fatal(err)
	}
	truth, _ := ir.NewConstant(model, true)
	resolved, err := ir.NewFact(model, id, truth, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCondition(reference, ir.PortableProviders()); err == nil {
		t.Fatal("unresolved fact reference validated for execution")
	}
	if err := ValidateCondition(resolved, ir.PortableProviders()); err != nil {
		t.Fatal(err)
	}
	typ, _ := ir.NewTypeRef(ir.ValueBool, false, 0, 0, ir.EnumID{}, nil, 0)
	operand, _ := ir.OneOperand(ir.BoolValue(true))
	invalid, err := ir.NewScalar(model, ir.FieldID{1}, typ, ir.OperatorContains, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	invalidFact, err := ir.NewFact(model, id, invalid, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCondition(invalidFact, ir.PortableProviders()); err == nil {
		t.Fatal("fact boundary bypassed body operator validation")
	}
}
