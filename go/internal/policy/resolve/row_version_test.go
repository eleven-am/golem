package resolve

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

var versionField = fieldID(9)

func TestRowVersionIsReadableWheneverTheRowIsReadableWithoutAGrant(t *testing.T) {
	conditionA := scalarBoolCondition(t, fieldA, true)
	conditionB := scalarBoolCondition(t, fieldB, true)
	onlyA, err := ir.NewFieldRule(ir.ActionRead, ir.EffectGrant, testModel, &conditionA, fieldA, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	denyRow, err := ir.NewModelRule(ir.ActionRead, ir.EffectDeny, testModel, &conditionB, 1)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ir.NewPolicy(testModel, []ir.Rule{onlyA, denyRow})
	if err != nil {
		t.Fatal(err)
	}
	policy = policy.WithRowVersion(versionField)
	normalized, err := normalize.Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []ir.Policy{policy, normalized} {
		rows, err := RowConstraint(candidate, ir.ActionRead, testModel)
		if err != nil {
			t.Fatal(err)
		}
		version, err := FieldCondition(candidate, ir.ActionRead, testModel, versionField)
		if err != nil {
			t.Fatal(err)
		}
		other, err := FieldCondition(candidate, ir.ActionRead, testModel, fieldB)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range []testRow{{fieldA: false, fieldB: false}, {fieldA: false, fieldB: true}, {fieldA: true, fieldB: false}, {fieldA: true, fieldB: true}} {
			if got, want := evaluate(t, version, row), evaluate(t, rows, row); got != want {
				t.Fatalf("row %v: row version readable=%t, row readable=%t", row, got, want)
			}
			if evaluate(t, other, row) {
				t.Fatalf("row %v: a field with no grant became readable", row)
			}
		}
	}
}

func TestRowVersionIsNeverWritable(t *testing.T) {
	conditionA := scalarBoolCondition(t, fieldA, true)
	rule, err := ir.NewModelRule(ir.ActionUpdate, ir.EffectGrant, testModel, &conditionA, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ir.NewPolicy(testModel, []ir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	policy = policy.WithRowVersion(versionField)
	for _, action := range []ir.Action{ir.ActionCreate, ir.ActionUpdate} {
		condition, err := FieldCondition(policy, action, testModel, versionField)
		if err != nil {
			t.Fatal(err)
		}
		if truth, constant := condition.Constant(); !constant || truth {
			t.Fatalf("action %d grants the row version", action)
		}
	}
}
