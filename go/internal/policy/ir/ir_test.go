package ir

import (
	"bytes"
	"math"
	"testing"
)

func testModel(value byte) ModelID       { var id ModelID; id[15] = value; return id }
func testField(value byte) FieldID       { var id FieldID; id[15] = value; return id }
func testRelation(value byte) RelationID { var id RelationID; id[15] = value; return id }

func mustType(t *testing.T, kind ValueKind, nullable bool) TypeRef {
	t.Helper()
	value, err := NewTypeRef(kind, nullable, 0, 0, EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustOperand(t *testing.T, value Value) Operand {
	t.Helper()
	operand, err := OneOperand(value)
	if err != nil {
		t.Fatal(err)
	}
	return operand
}

func TestProviderSetIsClosedAndOrdered(t *testing.T) {
	set, err := NewProviderSet(ProviderPostgreSQL, ProviderSQLite, ProviderPostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := set.Providers(), []Provider{ProviderSQLite, ProviderPostgreSQL}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("providers = %v", got)
	}
	if _, err := NewProviderSet(); err == nil {
		t.Fatal("empty provider set accepted")
	}
	if _, err := NewProviderSet(99); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestValuesNormalizeExactRepresentations(t *testing.T) {
	negativeZero, err := Float64Value(math.Copysign(0, -1))
	if err != nil {
		t.Fatal(err)
	}
	bits, _ := negativeZero.Float64Bits()
	if bits != 0 {
		t.Fatalf("negative zero bits = %x", bits)
	}
	decimal, err := NewDecimalValue(12300, 4)
	if err != nil {
		t.Fatal(err)
	}
	coefficient, scale, _ := decimal.Decimal()
	if coefficient != 123 || scale != 2 {
		t.Fatalf("decimal = %d/%d", coefficient, scale)
	}
	zero, err := NewDecimalValue(0, 18)
	if err != nil {
		t.Fatal(err)
	}
	coefficient, scale, _ = zero.Decimal()
	if coefficient != 0 || scale != 0 {
		t.Fatalf("decimal zero = %d/%d", coefficient, scale)
	}
	if _, err := NewDecimalValue(math.MaxInt64, 0); err == nil {
		t.Fatal("19-digit decimal accepted")
	}
	if _, err := Float32Value(float32(math.Inf(1))); err == nil {
		t.Fatal("infinite float accepted")
	}
	if _, err := NewDateValue(10_000, 1, 1); err == nil {
		t.Fatal("year 10000 accepted")
	}
}

func TestScalarListCarriesNoInventedElementIdentity(t *testing.T) {
	empty, err := NewListValue(nil)
	if err != nil {
		t.Fatal(err)
	}
	values, ok := empty.List()
	if !ok || values == nil || len(values) != 0 {
		t.Fatalf("empty list = %#v, %v", values, ok)
	}
	one, _ := SignedValue(ValueInt64, 1)
	two, _ := SignedValue(ValueInt32, 2)
	if _, err := NewListValue([]Value{one, two}); err == nil {
		t.Fatal("heterogeneous list accepted")
	}
}

func TestCompositeValuesAreCopyIsolated(t *testing.T) {
	input := []byte{1, 2, 3}
	bytesValue := BytesValue(input)
	input[0] = 9
	got, _ := bytesValue.Bytes()
	if got[0] != 1 {
		t.Fatal("byte input aliases value")
	}
	got[1] = 9
	again, _ := bytesValue.Bytes()
	if again[1] != 2 {
		t.Fatal("byte getter aliases value")
	}
	number, err := NewJSONNumber(false, []byte("123"), -2)
	if err != nil {
		t.Fatal(err)
	}
	jsonNumber, err := JSONNumberValueOf(number)
	if err != nil {
		t.Fatal(err)
	}
	member, err := NewJSONMember("n", jsonNumber)
	if err != nil {
		t.Fatal(err)
	}
	object, err := JSONObjectValue([]JSONMember{member})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := NewJSONValue(object)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := wrapped.JSON()
	members, _ := first.Object()
	members[0].value = JSONNullValue()
	second, _ := wrapped.JSON()
	membersAgain, _ := second.Object()
	if membersAgain[0].value.Kind() != JSONNumber {
		t.Fatal("JSON getter aliases value")
	}
}

func TestInactiveJSONUnionStorageIsRejected(t *testing.T) {
	malformed := BoolValue(true)
	malformed.json.array = []JSONValue{JSONNullValue()}
	if err := malformed.Validate(); err == nil {
		t.Fatal("inactive hidden JSON array accepted")
	}
	malformed = BoolValue(true)
	malformed.json.object = []JSONMember{{key: "x", value: JSONNullValue()}}
	if err := malformed.Validate(); err == nil {
		t.Fatal("inactive hidden JSON object accepted")
	}
}

func TestJSONObjectCanonicalizesMemberOrderAndRejectsDuplicates(t *testing.T) {
	one, _ := JSONStringValue("one")
	two, _ := JSONStringValue("two")
	mB, _ := NewJSONMember("b", two)
	mA, _ := NewJSONMember("a", one)
	object, err := JSONObjectValue([]JSONMember{mB, mA})
	if err != nil {
		t.Fatal(err)
	}
	members, _ := object.Object()
	if members[0].Key() != "a" || members[1].Key() != "b" {
		t.Fatalf("members = %#v", members)
	}
	if _, err := JSONObjectValue([]JSONMember{mA, mA}); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if _, err := NewJSONNumber(true, []byte("0"), 0); err == nil {
		t.Fatal("negative JSON zero accepted")
	}
	if _, err := NewJSONNumber(false, []byte("120"), 0); err == nil {
		t.Fatal("non-normalized JSON coefficient accepted")
	}
}

func TestPortableJSONDomainRejectsNULInStringsAndKeys(t *testing.T) {
	if _, err := JSONStringValue("value\x00suffix"); err == nil {
		t.Fatal("JSON string containing NUL was accepted")
	}
	value, err := JSONStringValue("portable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewJSONMember("key\x00suffix", value); err == nil {
		t.Fatal("JSON key containing NUL was accepted")
	}
}

func TestConditionConstructionEnforcesClosedShapes(t *testing.T) {
	model := testModel(1)
	other := testModel(2)
	typ := mustType(t, ValueInt64, false)
	value, _ := SignedValue(ValueInt64, 7)
	leaf, err := NewScalar(model, testField(1), typ, OperatorEqual, ComparisonSensitive, mustOperand(t, value), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewLogical(model, LogicalAnd, []Condition{leaf}); err == nil {
		t.Fatal("one-child and accepted")
	}
	foreign, _ := NewConstant(other, true)
	if _, err := NewLogical(model, LogicalNot, []Condition{foreign}); err == nil {
		t.Fatal("foreign logical child accepted")
	}
	if _, err := NewRelation(model, testField(2), testRelation(1), other, RelationToOne, OperatorRelationIs, &leaf, nil); err == nil {
		t.Fatal("relation child rooted at parent accepted")
	}
}

func TestRelationOwnRequirementsRemainDistinctFromDerivedChildUnion(t *testing.T) {
	parent := testModel(1)
	childModel := testModel(2)
	providers := PortableProviders()
	childRequirement, _ := NewRequirement(providers, CapabilityBinaryText)
	ownedRequirement, _ := NewRequirement(providers, CapabilityRelationCorrelation)
	typ := mustType(t, ValueString, false)
	value, _ := StringValue("child")
	child, err := NewScalar(childModel, testField(3), typ, OperatorEqual, ComparisonSensitive, mustOperand(t, value), []Requirement{childRequirement})
	if err != nil {
		t.Fatal(err)
	}
	relation, err := NewRelation(parent, testField(2), testRelation(1), childModel, RelationToOne, OperatorRelationIs, &child, []Requirement{ownedRequirement})
	if err != nil {
		t.Fatal(err)
	}
	owned, ok := relation.RelationOwnRequirements()
	if !ok || len(owned) != 1 || owned[0] != ownedRequirement {
		t.Fatalf("relation-owned requirements = %#v, %v", owned, ok)
	}
	if total := relation.Requirements(); len(total) != 2 {
		t.Fatalf("derived relation requirement union = %#v", total)
	}
	owned[0] = childRequirement
	again, _ := relation.RelationOwnRequirements()
	if again[0] != ownedRequirement {
		t.Fatal("relation-owned requirement accessor leaked mutable storage")
	}
	if err := relation.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalConditionIsDeterministicAndShapeSensitive(t *testing.T) {
	model := testModel(1)
	typ := mustType(t, ValueString, true)
	value, _ := StringValue("alpha")
	operand := mustOperand(t, value)
	providers := PortableProviders()
	requirement, _ := NewRequirement(providers, CapabilityBinaryText)
	first, err := NewScalar(model, testField(1), typ, OperatorEqual, ComparisonSensitive, operand, []Requirement{requirement, requirement})
	if err != nil {
		t.Fatal(err)
	}
	one, err := CanonicalCondition(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := CanonicalCondition(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("canonical bytes changed")
	}
	if !bytes.HasPrefix(one, []byte(conditionDomain)) {
		t.Fatal("missing condition domain")
	}
	second, err := NewScalar(model, testField(1), typ, OperatorNotEqual, ComparisonSensitive, operand, []Requirement{requirement})
	if err != nil {
		t.Fatal(err)
	}
	different, _ := CanonicalCondition(second)
	if bytes.Equal(one, different) {
		t.Fatal("operator identity omitted from canonical bytes")
	}
	fingerprintA, _ := ConditionFingerprint(first)
	fingerprintB, _ := ConditionFingerprint(first)
	if fingerprintA != fingerprintB {
		t.Fatal("condition fingerprint changed")
	}
}

func TestOperandManyKeepsEmptyDistinctFromNone(t *testing.T) {
	empty, err := ManyOperand(nil)
	if err != nil {
		t.Fatal(err)
	}
	values, ok := empty.Many()
	if !ok || values == nil || len(values) != 0 {
		t.Fatalf("empty many = %#v, %v", values, ok)
	}
	model := testModel(1)
	typ := mustType(t, ValueInt64, false)
	many, err := NewScalar(model, testField(1), typ, OperatorIn, ComparisonSensitive, empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	none, err := NewScalar(model, testField(1), typ, OperatorIsNull, ComparisonSensitive, NoOperand(), nil)
	if err != nil {
		t.Fatal(err)
	}
	manyBytes, _ := CanonicalCondition(many)
	noneBytes, _ := CanonicalCondition(none)
	if bytes.Equal(manyBytes, noneBytes) {
		t.Fatal("empty many collapsed into no operand")
	}
}

func TestPolicyPreservesRuleAndFieldOrder(t *testing.T) {
	model := testModel(1)
	condition, _ := NewConstant(model, true)
	first, err := NewFieldRule(ActionRead, EffectDeny, model, &condition, testField(2), []FieldID{testField(1)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewModelRule(ActionRead, EffectGrant, model, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewPolicy(model, []Rule{first, second})
	if err != nil {
		t.Fatal(err)
	}
	rules := policy.Rules()
	fields, modelWide := rules[0].Fields()
	if modelWide || fields[0] != testField(2) || fields[1] != testField(1) {
		t.Fatalf("field order = %v", fields)
	}
	fields[0] = testField(9)
	again, _ := policy.Rules()[0].Fields()
	if again[0] != testField(2) {
		t.Fatal("policy getter aliases fields")
	}
	encodedA, _ := CanonicalPolicy(policy)
	reversedFirst, _ := NewModelRule(ActionRead, EffectGrant, model, nil, 0)
	reversedSecond, _ := NewFieldRule(ActionRead, EffectDeny, model, &condition, testField(2), []FieldID{testField(1)}, 1)
	reversed, _ := NewPolicy(model, []Rule{reversedFirst, reversedSecond})
	encodedB, _ := CanonicalPolicy(reversed)
	if bytes.Equal(encodedA, encodedB) {
		t.Fatal("canonical policy sorted semantic rule order")
	}
	if _, err := NewFieldRule(ActionDelete, EffectGrant, model, nil, testField(1), nil, 0); err == nil {
		t.Fatal("delete field rule accepted")
	}
}
