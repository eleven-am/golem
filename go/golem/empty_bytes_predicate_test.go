package golem

import "testing"

type emptyBytesPredicateModel struct{}

func TestFrozenPredicatesKeepAnEmptyBytesOperandEmpty(t *testing.T) {
	model, field := ModelID{1}, FieldID{2}
	rules := NewRules[emptyBytesPredicateModel]()
	rules.CanRead(GeneratedBytesField[emptyBytesPredicateModel](field).Eq([]byte{}))
	policy, err := rules.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.View().Rules() {
		condition, present := rule.Condition()
		if !present {
			t.Fatal("rule lost its condition")
		}
		assertEmptyBytesOperand(t, "policy rule", condition.Root())
	}
	bridged, err := RuntimeFreezePredicate(model, RuntimePredicateNode{Kind: FrozenConditionScalar, Operator: FrozenOperatorEq, Mode: FrozenComparisonSensitive, Field: field,
		Operand: RuntimePredicateOperand{Kind: FrozenOperandOne, One: RuntimePredicateValue{Kind: FrozenValueBytes, Value: []byte{}}}})
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyBytesOperand(t, "runtime predicate", bridged.View().Root())
}

func assertEmptyBytesOperand(t *testing.T, path string, condition FrozenConditionView) {
	t.Helper()
	value, present := condition.Operand().One()
	if !present {
		t.Fatalf("%s has no single operand", path)
	}
	data, ok := value.Bytes()
	if !ok || data == nil || len(data) != 0 {
		t.Fatalf("%s operand bytes=%#v; want a non-nil empty slice", path, data)
	}
}
