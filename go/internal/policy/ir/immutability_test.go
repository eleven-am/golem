package ir

import (
	"bytes"
	"testing"
)

func mustCanonical(t *testing.T, condition Condition) []byte {
	t.Helper()
	encoded, err := CanonicalCondition(condition)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestConditionNodesAreIsolatedFromInputAndAccessorSlices(t *testing.T) {
	model := testModel(1)
	target := testModel(2)
	typ := mustType(t, ValueInt64, false)
	providers := PortableProviders()
	binary, _ := NewRequirement(providers, CapabilityBinaryText)
	exact, _ := NewRequirement(providers, CapabilityExactJSON)
	first, _ := SignedValue(ValueInt64, 1)
	second, _ := SignedValue(ValueInt64, 2)
	values := []Value{first, second}
	many, err := ManyOperand(values)
	if err != nil {
		t.Fatal(err)
	}
	requirements := []Requirement{binary}
	leaf, err := NewScalar(model, testField(1), typ, OperatorIn, ComparisonSensitive, many, requirements)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewScalar(model, testField(2), typ, OperatorEqual, ComparisonSensitive, mustOperand(t, first), nil)
	if err != nil {
		t.Fatal(err)
	}
	children := []Condition{leaf, other}
	and, err := NewLogical(model, LogicalAnd, children)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewScalar(target, testField(3), typ, OperatorEqual, ComparisonSensitive, mustOperand(t, second), nil)
	if err != nil {
		t.Fatal(err)
	}
	own := []Requirement{exact}
	relation, err := NewRelation(model, testField(4), testRelation(1), target, RelationToOne, OperatorRelationIs, &foreign, own)
	if err != nil {
		t.Fatal(err)
	}
	leafBytes := mustCanonical(t, leaf)
	andBytes := mustCanonical(t, and)
	relationBytes := mustCanonical(t, relation)

	values[0] = second
	requirements[0] = exact
	children[0] = other
	own[0] = binary
	replacement, _ := NewConstant(target, true)
	foreign = replacement

	_, logicalChildren, _ := and.Logical()
	logicalChildren[0] = other
	operand, _ := leaf.Operand()
	operandValues, _ := operand.Many()
	operandValues[0] = second
	leafRequirements := leaf.Requirements()
	leafRequirements[0] = exact
	_, _, _, _, relationChild, _ := relation.Relation()
	*relationChild = replacement
	ownRequirements, _ := relation.RelationOwnRequirements()
	ownRequirements[0] = binary
	encoded := mustCanonical(t, and)
	encoded[len(encoded)-1] ^= 0xff

	if !bytes.Equal(mustCanonical(t, leaf), leafBytes) {
		t.Fatal("scalar node changed after its inputs or accessor results were mutated")
	}
	if !bytes.Equal(mustCanonical(t, and), andBytes) {
		t.Fatal("logical node changed after its inputs or accessor results were mutated")
	}
	if !bytes.Equal(mustCanonical(t, relation), relationBytes) {
		t.Fatal("relation node changed after its inputs or accessor results were mutated")
	}
	if and.ChildCount() != 2 || !bytes.Equal(mustCanonical(t, and.Child(0)), leafBytes) {
		t.Fatal("zero-copy child accessor does not return the constructed child")
	}
	if child, ok := relation.RelationChild(); !ok || child.ModelID() != target || child.Kind() != ConditionScalar {
		t.Fatal("zero-copy relation child accessor does not return the constructed child")
	}
	if got := and.Requirements(); len(got) != 1 || got[0] != binary {
		t.Fatalf("logical requirement union = %#v", got)
	}
	if got := relation.Requirements(); len(got) != 1 || got[0] != exact {
		t.Fatalf("relation requirement union = %#v", got)
	}
}

func TestJSONPathAndPolicyAreIsolatedFromInputAndAccessorSlices(t *testing.T) {
	model := testModel(1)
	key, _ := JSONKeySegment("a")
	segments := []JSONPathSegment{key, JSONIndexSegment(1)}
	path, err := NewJSONPath(segments...)
	if err != nil {
		t.Fatal(err)
	}
	typ := mustType(t, ValueJSON, true)
	node, err := NewJSON(model, testField(1), typ, OperatorJSONIsNull, ComparisonSensitive, path, NoOperand(), nil)
	if err != nil {
		t.Fatal(err)
	}
	nodeBytes := mustCanonical(t, node)
	segments[0] = JSONIndexSegment(7)
	gotPath, _ := node.Path()
	gotSegments := gotPath.Segments()
	gotSegments[0] = JSONIndexSegment(9)
	if !bytes.Equal(mustCanonical(t, node), nodeBytes) {
		t.Fatal("JSON node changed after path slices were mutated")
	}

	rest := []FieldID{testField(3)}
	fieldRule, err := NewFieldRule(ActionRead, EffectGrant, model, &node, testField(2), rest, 0)
	if err != nil {
		t.Fatal(err)
	}
	modelRule, err := NewModelRule(ActionRead, EffectDeny, model, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	rules := []Rule{fieldRule, modelRule}
	policy, err := NewPolicy(model, rules)
	if err != nil {
		t.Fatal(err)
	}
	policyBytes, err := CanonicalPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	rest[0] = testField(9)
	rules[0] = modelRule
	returned := policy.Rules()
	returned[0] = modelRule
	again, err := CanonicalPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, policyBytes) {
		t.Fatal("policy changed after rule or field slices were mutated")
	}
	if policy.RuleCount() != 2 {
		t.Fatalf("rule count = %d", policy.RuleCount())
	}
	fields, modelWide := policy.RuleAt(0).Fields()
	if modelWide || len(fields) != 2 || fields[0] != testField(2) || fields[1] != testField(3) {
		t.Fatalf("rule fields = %v", fields)
	}
	condition, ok := policy.RuleAt(0).Condition()
	if !ok || !bytes.Equal(mustCanonical(t, condition), nodeBytes) {
		t.Fatal("rule condition accessor does not return the constructed condition")
	}
}

func TestSealNormalLeavesCanonicalIdentityUnchanged(t *testing.T) {
	model := testModel(1)
	condition, err := NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	before := mustCanonical(t, condition)
	if IsSealedNormal(condition) {
		t.Fatal("fresh condition reported as sealed")
	}
	sealed := SealNormal(condition)
	if !IsSealedNormal(sealed) || !IsSealedNormal(condition) {
		t.Fatal("seal did not mark the shared node")
	}
	if !bytes.Equal(mustCanonical(t, sealed), before) {
		t.Fatal("sealing changed canonical identity")
	}
	if IsSealedNormal(Condition{}) || SealNormal(Condition{}).Validate() == nil {
		t.Fatal("zero condition became sealed or valid")
	}
}
