package imply

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestConditionConservativeStructuralCorpus(t *testing.T) {
	model := modelID(1)
	a := scalar(t, model, fieldID(1), ir.OperatorEqual, 1)
	b := scalar(t, model, fieldID(2), ir.OperatorEqual, 2)
	c := scalar(t, model, fieldID(3), ir.OperatorEqual, 3)
	notB := logical(t, model, ir.LogicalNot, b)
	greater := scalar(t, model, fieldID(1), ir.OperatorGreaterThan, 0)
	truth := constant(t, model, true)
	falsehood := constant(t, model, false)

	tests := []struct {
		name      string
		selecting ir.Condition
		required  ir.Condition
		want      bool
	}{
		{"identical leaf", a, a, true},
		{"conjunction contains requirement", logical(t, model, ir.LogicalAnd, a, b), a, true},
		{"conjunction entails same conjunction", logical(t, model, ir.LogicalAnd, a, b), logical(t, model, ir.LogicalAnd, b, a), true},
		{"requirement disjunction accepts one selected branch", a, logical(t, model, ir.LogicalOr, a, b), true},
		{"requirement disjunction branch may need whole conjunction", logical(t, model, ir.LogicalAnd, a, c), logical(t, model, ir.LogicalOr, logical(t, model, ir.LogicalAnd, a, c), b), true},
		{"held disjunction requires every branch", logical(t, model, ir.LogicalOr, a, logical(t, model, ir.LogicalAnd, a, b)), a, true},
		{"held disjunction with unrelated branch refuses", logical(t, model, ir.LogicalOr, a, b), a, false},
		{"bounded proposition proves partitioned read reach", a, logical(t, model, ir.LogicalOr, b, logical(t, model, ir.LogicalAnd, a, notB)), true},
		{"unrelated leaves refuse", a, b, false},
		{"semantic inequality knowledge is deliberately absent", a, greater, false},
		{"anything implies true", b, truth, true},
		{"true does not imply a leaf", truth, a, false},
		{"false does not prove arbitrary requirement", falsehood, a, false},
		{"false structurally implies itself", falsehood, falsehood, true},
	}

	sawTrue, sawFalse := false, false
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Condition(test.selecting, test.required)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Condition() = %v, want %v", got, test.want)
			}
			if got {
				sawTrue = true
			} else {
				sawFalse = true
			}
		})
	}
	if !sawTrue || !sawFalse {
		t.Fatal("corpus must kill both always-true and always-false implication mutants")
	}
}

func TestConditionExactPropositionalFallbackMatchesTruthTable(t *testing.T) {
	model := modelID(1)
	a := scalar(t, model, fieldID(1), ir.OperatorEqual, 1)
	b := scalar(t, model, fieldID(2), ir.OperatorEqual, 2)
	notA := logical(t, model, ir.LogicalNot, a)
	notB := logical(t, model, ir.LogicalNot, b)
	formulas := []formula{
		{condition: a, evaluate: func(a, _ bool) bool { return a }},
		{condition: b, evaluate: func(_, b bool) bool { return b }},
		{condition: notA, evaluate: func(a, _ bool) bool { return !a }},
		{condition: logical(t, model, ir.LogicalAnd, a, b), evaluate: func(a, b bool) bool { return a && b }},
		{condition: logical(t, model, ir.LogicalOr, a, b), evaluate: func(a, b bool) bool { return a || b }},
		{condition: logical(t, model, ir.LogicalOr, b, logical(t, model, ir.LogicalAnd, a, notB)), evaluate: func(a, b bool) bool { return b || a && !b }},
	}
	for selectingIndex, selecting := range formulas {
		for requiredIndex, required := range formulas {
			want := true
			for _, aValue := range []bool{false, true} {
				for _, bValue := range []bool{false, true} {
					if selecting.evaluate(aValue, bValue) && !required.evaluate(aValue, bValue) {
						want = false
					}
				}
			}
			got, err := Condition(selecting.condition, required.condition)
			if err != nil {
				t.Fatalf("formula %d => %d: %v", selectingIndex, requiredIndex, err)
			}
			if got != want {
				t.Fatalf("formula %d => %d = %v, truth-table oracle %v", selectingIndex, requiredIndex, got, want)
			}
		}
	}

	impossible := logical(t, model, ir.LogicalAnd, a, notA)
	got, err := Condition(impossible, b)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("unsatisfiable selection must not produce a vacuous authorization proof")
	}
}

func TestConditionPropositionalFallbackIsBoundedAndFailsClosed(t *testing.T) {
	model := modelID(1)
	selecting := make([]ir.Condition, maximumPropositionalAtoms+1)
	for index := range selecting {
		selecting[index] = scalar(t, model, fieldID(byte(index+1)), ir.OperatorEqual, int64(index+1))
	}
	selection := logical(t, model, ir.LogicalAnd, selecting...)
	extra := scalar(t, model, fieldID(100), ir.OperatorEqual, 100)
	required := logical(t, model, ir.LogicalOr, extra, logical(t, model, ir.LogicalNot, extra))
	got, err := Condition(selection, required)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("oversized non-structural proof must fail closed")
	}
}

type formula struct {
	condition ir.Condition
	evaluate  func(bool, bool) bool
}

func TestSameUsesNormalizedCanonicalStructure(t *testing.T) {
	model := modelID(1)
	a := scalar(t, model, fieldID(1), ir.OperatorEqual, 1)
	b := scalar(t, model, fieldID(2), ir.OperatorEqual, 2)

	same, err := Same(
		logical(t, model, ir.LogicalAnd, a, b),
		logical(t, model, ir.LogicalAnd, b, a),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("canonical commutative conjunctions must compare equal")
	}

	otherModel := modelID(2)
	foreign := scalar(t, otherModel, fieldID(1), ir.OperatorEqual, 1)
	same, err = Same(a, foreign)
	if err != nil || same {
		t.Fatalf("cross-model Same() = %v, %v", same, err)
	}
	if _, err := Condition(a, foreign); err == nil {
		t.Fatal("cross-model implication must fail closed")
	}
}

func TestSameDateTimeUsesCanonicalInstant(t *testing.T) {
	model := modelID(1)
	typ, err := ir.NewTypeRef(ir.ValueDateTime, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	instant, err := ir.NewDateTimeValue(1_700_000_000, 123_456_000)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := ir.OneOperand(instant)
	if err != nil {
		t.Fatal(err)
	}
	left, err := ir.NewScalar(model, fieldID(4), typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ir.NewScalar(model, fieldID(4), typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	same, err := Same(left, right)
	if err != nil || !same {
		t.Fatalf("same instant = %v, %v", same, err)
	}
}

func scalar(t *testing.T, model ir.ModelID, field ir.FieldID, operator ir.OperatorID, value int64) ir.Condition {
	t.Helper()
	typ, err := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	integer, err := ir.SignedValue(ir.ValueInt64, value)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := ir.OneOperand(integer)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewScalar(model, field, typ, operator, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func logical(t *testing.T, model ir.ModelID, operator ir.LogicalOperator, children ...ir.Condition) ir.Condition {
	t.Helper()
	condition, err := ir.NewLogical(model, operator, children)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func constant(t *testing.T, model ir.ModelID, truth bool) ir.Condition {
	t.Helper()
	condition, err := ir.NewConstant(model, truth)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func modelID(value byte) ir.ModelID { return ir.ModelID{value} }
func fieldID(value byte) ir.FieldID { return ir.FieldID{value} }
