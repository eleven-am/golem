package ir

import (
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestUnorderedRequestIsAPlainFindManyThatSurvivesCloning(t *testing.T) {
	model := policyir.ModelID{0x01}
	field := policyir.FieldID{0x02}
	scalar, err := NewScalarSelection(field)
	if err != nil {
		t.Fatal(err)
	}
	order, err := NewOrder(field, Ascending)
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(RequestInput{Operation: FindMany, Model: model, Selection: []Selection{scalar}, Unordered: true})
	if err != nil {
		t.Fatal(err)
	}
	if !request.Unordered() || !request.clone().Unordered() {
		t.Fatal("an unordered request lost its marker")
	}
	ordered, err := NewRequest(RequestInput{Operation: FindMany, Model: model, Selection: []Selection{scalar}})
	if err != nil || ordered.Unordered() {
		t.Fatalf("a plain request is unordered: %v", err)
	}
	take, skip := 1, 1
	cursor := Cursor{predicate: mustConstant(t, model)}
	for name, input := range map[string]RequestInput{
		"count":      {Operation: Count, Model: model, Unordered: true},
		"findFirst":  {Operation: FindFirst, Model: model, Selection: []Selection{scalar}, Unordered: true},
		"orderBy":    {Operation: FindMany, Model: model, Selection: []Selection{scalar}, OrderBy: []Order{order}, Unordered: true},
		"take":       {Operation: FindMany, Model: model, Selection: []Selection{scalar}, Take: &take, Unordered: true},
		"skip":       {Operation: FindMany, Model: model, Selection: []Selection{scalar}, Skip: &skip, Unordered: true},
		"distinct":   {Operation: FindMany, Model: model, Selection: []Selection{scalar}, Distinct: []policyir.FieldID{field}, Unordered: true},
		"withCursor": {Operation: FindMany, Model: model, Selection: []Selection{scalar}, Cursor: &cursor, Unordered: true},
	} {
		if _, err := NewRequest(input); err == nil {
			t.Fatalf("%s: an unordered request carrying order, window, distinct or cursor was accepted", name)
		}
	}
}

func mustConstant(t *testing.T, model policyir.ModelID) policyir.Condition {
	t.Helper()
	condition, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}
