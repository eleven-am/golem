package golem

import "testing"

type customBridgeModel struct{}

func TestCustomRuntimeBridgeRestoresOnlyMatchingTypedP3P4Values(t *testing.T) {
	model := ModelID{15: 1}
	field := FieldID{15: 2}
	key := KeyID{15: 3}
	predicate, err := RuntimeFreezePredicate(model, RuntimePredicateNode{
		Kind: FrozenConditionScalar, Operator: FrozenOperatorEq, Mode: FrozenComparisonSensitive, Field: field,
		Operand: RuntimePredicateOperand{Kind: FrozenOperandOne, One: RuntimePredicateValue{Kind: FrozenValueString, Value: "exact"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	typedPredicate, err := RuntimeTypedPredicate[customBridgeModel](model, predicate)
	if err != nil {
		t.Fatal(err)
	}
	again, err := typedPredicate.freezeForModel(model)
	if err != nil || string(again.CanonicalBytes()) != string(predicate.CanonicalBytes()) {
		t.Fatalf("typed predicate changed: err=%v", err)
	}
	target, err := RuntimeMutationTargetFromPredicate(model, key, []FieldID{field}, predicate)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := RuntimeTypedUniqueSelector[customBridgeModel](model, target)
	if err != nil || selector.model != model || selector.key != key || len(selector.components) != 1 {
		t.Fatalf("typed selector=%#v err=%v", selector, err)
	}
	typedTarget, err := RuntimeTypedMutationTarget[customBridgeModel](model, target)
	if err != nil {
		t.Fatal(err)
	}
	frozenTarget, err := RuntimeFreezeMutationTarget(typedTarget)
	if err != nil || frozenTarget.Selector().KeyID() != key {
		t.Fatalf("typed target key=%x err=%v", frozenTarget.Selector().KeyID(), err)
	}

	create, err := RuntimeMutationInputFromValues(RuntimeMutationCreateInput, model, []RuntimeMutationFieldValue{{Field: field, Operation: MutationFieldCreate, Value: "value", HasValue: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := RuntimeCustomMutationInputValue(RuntimeMutationCreateInput, create)
	if err != nil {
		t.Fatal(err)
	}
	typedCreate, err := RuntimeTypedCreateInput[customBridgeModel](model, envelope)
	if err != nil {
		t.Fatal(err)
	}
	refrozen, err := RuntimeFreezeCreateInput(typedCreate)
	if err != nil || refrozen.ModelID() != model || len(refrozen.Fields()) != 1 {
		t.Fatalf("typed create=%#v err=%v", refrozen, err)
	}
	if _, err := RuntimeTypedUpdateInput[customBridgeModel](model, envelope); err == nil {
		t.Fatal("create envelope was accepted as update input")
	}
	if _, err := RuntimeTypedPredicate[customBridgeModel](ModelID{15: 9}, predicate); err == nil {
		t.Fatal("predicate was rebound to another model")
	}
}
