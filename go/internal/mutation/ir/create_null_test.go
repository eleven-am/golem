package ir

import (
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestCreateGraphAcceptsExplicitNullButRejectsArithmetic(t *testing.T) {
	model := testModel(1)
	nullable := stringType(t, true)
	null, err := NewNull(testField(1), nullable)
	if err != nil {
		t.Fatal(err)
	}
	postcondition := constant(t, model)
	if _, err := NewGraph(NodeInput{
		Operation: Create, Model: model, ScalarOperations: []ScalarOperation{null},
		RowPostcondition: &postcondition, Identity: IdentityProduced,
	}); err != nil {
		t.Fatalf("explicit-null create graph was rejected: %v", err)
	}
	numeric, err := policyir.NewTypeRef(policyir.ValueInt64, true, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	value, err := policyir.SignedValue(policyir.ValueInt64, 1)
	if err != nil {
		t.Fatal(err)
	}
	increment, err := NewIncrement(testField(1), numeric, value)
	if err != nil {
		t.Fatal(err)
	}
	if _, graphErr := NewGraph(NodeInput{
		Operation: Create, Model: model, ScalarOperations: []ScalarOperation{increment},
		RowPostcondition: &postcondition, Identity: IdentityProduced,
	}); graphErr == nil {
		t.Fatal("arithmetic create graph was accepted")
	}
}
