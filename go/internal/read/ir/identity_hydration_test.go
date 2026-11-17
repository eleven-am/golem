package ir

import (
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestIdentityHydrationIsClosedAndSurvivesRequestClones(t *testing.T) {
	model := policyir.ModelID{1}
	key := policyir.FieldID{2}
	other := policyir.FieldID{3}
	typ, err := policyir.NewTypeRef(policyir.ValueUUID, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := policyir.OneOperand(policyir.UUIDValue([16]byte{15: 1}))
	if err != nil {
		t.Fatal(err)
	}
	leaf := func(field policyir.FieldID, operator policyir.OperatorID) policyir.Condition {
		t.Helper()
		value, err := policyir.NewScalar(model, field, typ, operator, policyir.ComparisonSensitive, operand, nil)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	equal := leaf(key, policyir.OperatorEqual)
	hydration, err := NewIdentityHydration(equal, []policyir.FieldID{key})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := NewScalarSelection(other)
	if err != nil {
		t.Fatal(err)
	}
	take := 1
	request, err := NewRequest(RequestInput{Operation: FindMany, Model: model, Selection: []Selection{selection}, Take: &take, Hydration: &hydration})
	if err != nil {
		t.Fatal(err)
	}
	hydration.fields[0] = other
	cloned := request.clone()
	nested, err := NewRelationSelection(policyir.FieldID{4}, policyir.RelationID{5}, model, request)
	if err != nil {
		t.Fatal(err)
	}
	child, ok := nested.clone().Request()
	if !ok {
		t.Fatal("nested request absent")
	}
	for _, value := range []Request{request, cloned, child} {
		h, present := value.IdentityHydration()
		if !present || h.Count() != 1 || h.Fields()[0] != key {
			t.Fatal("clone lost or aliased private correlation")
		}
		fields := h.Fields()
		fields[0] = other
		if next, _ := value.IdentityHydration(); next.Fields()[0] != key {
			t.Fatal("getter exposed key inventory")
		}
		if _, present := value.Where(); present {
			t.Fatal("clone promoted correlation to caller predicate")
		}
	}
	falseCondition, _ := policyir.NewConstant(model, false)
	arbitrary, _ := policyir.NewLogical(model, policyir.LogicalOr, []policyir.Condition{equal, falseCondition})
	duplicate, _ := policyir.NewLogical(model, policyir.LogicalAnd, []policyir.Condition{equal, equal})
	for label, test := range map[string]struct {
		condition policyir.Condition
		fields    []policyir.FieldID
	}{
		"non equality":        {leaf(key, policyir.OperatorNotEqual), []policyir.FieldID{key}},
		"constant branch":     {arbitrary, []policyir.FieldID{key}},
		"partial composite":   {equal, []policyir.FieldID{key, other}},
		"duplicate component": {duplicate, []policyir.FieldID{key, other}},
		"different field":     {equal, []policyir.FieldID{other}},
	} {
		if _, err := NewIdentityHydration(test.condition, test.fields); err == nil {
			t.Fatalf("%s admitted as private identity", label)
		}
	}
}
