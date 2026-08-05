package ir

import (
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestRequestOwnsNestedReadShape(t *testing.T) {
	model := policyir.ModelID{0x01}
	target := policyir.ModelID{0x02}
	field := policyir.FieldID{0x03}
	relationField := policyir.FieldID{0x04}
	relation := policyir.RelationID{0x05}
	order, err := NewOrder(field, Descending)
	if err != nil {
		t.Fatal(err)
	}
	childField, _ := NewScalarSelection(policyir.FieldID{0x06})
	child, err := NewRequest(RequestInput{Operation: FindMany, Model: target, Selection: []Selection{childField}})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := NewRelationSelection(relationField, relation, target, child)
	if err != nil {
		t.Fatal(err)
	}
	scalar, _ := NewScalarSelection(field)
	take := 10
	request, err := NewRequest(RequestInput{Operation: FindMany, Model: model, OrderBy: []Order{order}, Take: &take, Distinct: []policyir.FieldID{field}, Selection: []Selection{scalar, nested}})
	if err != nil {
		t.Fatal(err)
	}
	take = 99
	if got, _ := request.Take(); got != 10 {
		t.Fatalf("take=%d", got)
	}
	selection := request.Selection()
	if len(selection) != 2 || selection[1].TargetModelID() != target {
		t.Fatalf("selection=%#v", selection)
	}
	childCopy, ok := selection[1].Request()
	if !ok || childCopy.ModelID() != target || len(childCopy.Selection()) != 1 {
		t.Fatalf("child=%#v ok=%t", childCopy, ok)
	}
	selection[0] = Selection{}
	if request.Selection()[0].FieldID() != field {
		t.Fatal("selection slice mutated request")
	}
}

func TestRequestRejectsCrossModelWhereAndRelation(t *testing.T) {
	model := policyir.ModelID{0x01}
	target := policyir.ModelID{0x02}
	other := policyir.ModelID{0x03}
	condition, err := policyir.NewConstant(other, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRequest(RequestInput{Operation: FindMany, Model: model, Where: &condition}); err == nil {
		t.Fatal("cross-model where was accepted")
	}
	child, _ := NewRequest(RequestInput{Operation: FindMany, Model: target})
	if _, err := NewRelationSelection(policyir.FieldID{1}, policyir.RelationID{1}, other, child); err == nil {
		t.Fatal("relation with mismatched child was accepted")
	}
}
