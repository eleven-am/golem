package ir

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func testModel(seed byte) policyir.ModelID { var value policyir.ModelID; value[0] = seed; return value }
func testField(seed byte) policyir.FieldID { var value policyir.FieldID; value[0] = seed; return value }
func testRelation(seed byte) policyir.RelationID {
	var value policyir.RelationID
	value[0] = seed
	return value
}
func testKey(seed byte) golem.KeyID { var value golem.KeyID; value[0] = seed; return value }

func stringType(t *testing.T, nullable bool) policyir.TypeRef {
	t.Helper()
	value, err := policyir.NewTypeRef(policyir.ValueString, nullable, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func intType(t *testing.T) policyir.TypeRef {
	t.Helper()
	value, err := policyir.NewTypeRef(policyir.ValueInt64, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func stringValue(t *testing.T, input string) policyir.Value {
	t.Helper()
	value, err := policyir.StringValue(input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func signedValue(t *testing.T, input int64) policyir.Value {
	t.Helper()
	value, err := policyir.SignedValue(policyir.ValueInt64, input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func constant(t *testing.T, model policyir.ModelID) policyir.Condition {
	t.Helper()
	value, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func target(t *testing.T, model policyir.ModelID, seed byte) Target {
	t.Helper()
	selector, err := NewSelectorValue(testField(seed), stringValue(t, "id"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewTarget(model, testKey(seed), []SelectorValue{selector}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func selection(t *testing.T, model policyir.ModelID, action policyir.Action) SelectionRequirement {
	t.Helper()
	value, err := NewSelectionRequirement(action, constant(t, model))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestScalarOperationsAreClosedAndTypeChecked(t *testing.T) {
	set, err := NewSet(testField(1), stringType(t, false), stringValue(t, "title"))
	if err != nil {
		t.Fatal(err)
	}
	if set.Kind() != ScalarSet || set.FieldID() != testField(1) {
		t.Fatalf("unexpected set: %#v", set)
	}

	if _, err := NewNull(testField(1), stringType(t, false)); err == nil {
		t.Fatal("non-nullable null accepted")
	}
	if _, err := NewIncrement(testField(1), stringType(t, false), stringValue(t, "1")); err == nil {
		t.Fatal("string increment accepted")
	}
	if _, err := NewIncrement(testField(2), intType(t), signedValue(t, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewScalarOperation(testField(2), intType(t), ScalarSet, nil); err == nil {
		t.Fatal("set without value accepted")
	}

	first, _ := NewSet(testField(2), stringType(t, false), stringValue(t, "b"))
	second, _ := NewSet(testField(1), stringType(t, false), stringValue(t, "a"))
	normalized, err := normalizeScalarOperations([]ScalarOperation{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if normalized[0].FieldID() != testField(1) || normalized[1].FieldID() != testField(2) {
		t.Fatal("scalar operations were not canonicalized")
	}
	if _, err := normalizeScalarOperations([]ScalarOperation{first, first}); err == nil {
		t.Fatal("duplicate field operation accepted")
	}
}

func TestScalarOperationsEnforceExactLogicalTypeParameters(t *testing.T) {
	var enumA, enumB policyir.EnumID
	var member policyir.EnumValueID
	enumA[0], enumB[0], member[0] = 1, 2, 1
	enumType, err := policyir.NewTypeRef(policyir.ValueEnum, false, 0, 0, enumA, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrongEnum, _ := policyir.NewEnumValue(enumB, member)
	if _, err := NewSet(testField(1), enumType, wrongEnum); err == nil {
		t.Fatal("cross-enum value accepted")
	}

	decimalType, err := policyir.NewTypeRef(policyir.ValueDecimal, false, 4, 2, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	overPrecision, _ := policyir.NewDecimalValue(12345, 2)
	if _, err := NewSet(testField(1), decimalType, overPrecision); err == nil {
		t.Fatal("decimal precision overflow accepted")
	}
	overScale, _ := policyir.NewDecimalValue(123, 3)
	if _, err := NewSet(testField(1), decimalType, overScale); err == nil {
		t.Fatal("decimal scale overflow accepted")
	}

	timeType, err := policyir.NewTypeRef(policyir.ValueTime, false, 2, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	timeValue, _ := policyir.NewTimeValue(123_000)
	if _, err := NewSet(testField(1), timeType, timeValue); err == nil {
		t.Fatal("excess time precision accepted")
	}
	dateTimeType, err := policyir.NewTypeRef(policyir.ValueDateTime, false, 2, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	dateTimeValue, _ := policyir.NewDateTimeValue(1, 123_000_000)
	if _, err := NewSet(testField(1), dateTimeType, dateTimeValue); err == nil {
		t.Fatal("excess datetime precision accepted")
	}
}

func TestTargetPreservesCompoundSelectorOrderAndValidatesGuard(t *testing.T) {
	model := testModel(1)
	a, _ := NewSelectorValue(testField(2), stringValue(t, "a"))
	b, _ := NewSelectorValue(testField(1), stringValue(t, "b"))
	values := []SelectorValue{a, b}
	guard := constant(t, model)
	value, err := NewTarget(model, testKey(3), values, &guard)
	if err != nil {
		t.Fatal(err)
	}
	values[0] = b
	got := value.Values()
	if got[0].FieldID() != testField(2) || got[1].FieldID() != testField(1) {
		t.Fatal("compound selector order or clone safety lost")
	}
	got[0] = b
	if value.Values()[0].FieldID() != testField(2) {
		t.Fatal("target accessor leaked backing storage")
	}

	wrong := constant(t, testModel(2))
	if _, err := NewTarget(model, testKey(3), []SelectorValue{a}, &wrong); err == nil {
		t.Fatal("cross-model guard accepted")
	}
	if _, err := NewTarget(model, testKey(3), []SelectorValue{a, a}, nil); err == nil {
		t.Fatal("duplicate selector field accepted")
	}
}

func TestFactIdentityOrderIsNotSorted(t *testing.T) {
	fact, err := NewFactRequirement(FactUpdated, []policyir.FieldID{testField(2), testField(1)}, []policyir.FieldID{testField(2), testField(1)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fact.BeforeIdentity(); got[0] != testField(2) || got[1] != testField(1) {
		t.Fatal("compound identity order was changed")
	}
}
