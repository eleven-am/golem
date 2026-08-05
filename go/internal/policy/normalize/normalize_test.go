package normalize

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestConditionNormalizesAssociativeCommutativeBooleanStructure(t *testing.T) {
	model := modelID(1)
	a := scalar(t, model, fieldID(2), 2, nil)
	b := scalar(t, model, fieldID(1), 1, nil)
	trueNode := constant(t, model, true)
	nested := logical(t, model, ir.LogicalAnd, b, a)
	input := logical(t, model, ir.LogicalAnd, trueNode, nested, a)
	before := canonical(t, input)

	got := normalize(t, input)
	operator, children, ok := got.Logical()
	if !ok || operator != ir.LogicalAnd || len(children) != 2 {
		t.Fatalf("normal form = kind %d, operator %d, children %d", got.Kind(), operator, len(children))
	}
	if bytes.Compare(canonical(t, children[0]), canonical(t, children[1])) >= 0 {
		t.Fatal("and children are not in canonical byte order")
	}
	if !bytes.Equal(before, canonical(t, input)) {
		t.Fatal("normalization mutated its input")
	}

	again := normalize(t, got)
	if !bytes.Equal(canonical(t, got), canonical(t, again)) {
		t.Fatal("normalization is not idempotent")
	}
	firstFingerprint, err := ir.ConditionFingerprint(got)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := ir.ConditionFingerprint(again)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatal("normal form fingerprint is not deterministic")
	}
}

func TestConditionCommutesAllAndOrShuffles(t *testing.T) {
	model := modelID(1)
	nodes := []ir.Condition{
		scalar(t, model, fieldID(3), 3, nil),
		scalar(t, model, fieldID(1), 1, nil),
		scalar(t, model, fieldID(2), 2, nil),
	}
	permutations := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, operator := range []ir.LogicalOperator{ir.LogicalAnd, ir.LogicalOr} {
		var want []byte
		for index, permutation := range permutations {
			input := logical(t, model, operator, nodes[permutation[0]], nodes[permutation[1]], nodes[permutation[2]])
			got := canonical(t, normalize(t, input))
			if index == 0 {
				want = got
				continue
			}
			if !bytes.Equal(want, got) {
				t.Fatalf("operator %d permutation %v has a different normal form", operator, permutation)
			}
		}
	}
}

func TestConditionAppliesConstantsIdentitiesAndArityCollapse(t *testing.T) {
	model := modelID(1)
	leaf := scalar(t, model, fieldID(1), 1, nil)
	truth := constant(t, model, true)
	falsehood := constant(t, model, false)

	tests := []struct {
		name  string
		input ir.Condition
		want  ir.Condition
	}{
		{"and identity collapses one child", logical(t, model, ir.LogicalAnd, truth, leaf), leaf},
		{"or identity collapses one child", logical(t, model, ir.LogicalOr, falsehood, leaf), leaf},
		{"and absorber", logical(t, model, ir.LogicalAnd, leaf, falsehood), falsehood},
		{"or absorber", logical(t, model, ir.LogicalOr, leaf, truth), truth},
		{"all identities become true", logical(t, model, ir.LogicalAnd, truth, truth), truth},
		{"all identities become false", logical(t, model, ir.LogicalOr, falsehood, falsehood), falsehood},
		{"not true", logical(t, model, ir.LogicalNot, truth), falsehood},
		{"not false", logical(t, model, ir.LogicalNot, falsehood), truth},
		{"double not", logical(t, model, ir.LogicalNot, logical(t, model, ir.LogicalNot, leaf)), leaf},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalize(t, test.input)
			if !bytes.Equal(canonical(t, got), canonical(t, test.want)) {
				t.Fatalf("normal form differs from expected\ngot  %x\nwant %x", canonical(t, got), canonical(t, test.want))
			}
		})
	}
}

func TestConditionUsesTypedCanonicalEqualityForDuplicates(t *testing.T) {
	model := modelID(1)
	a := scalar(t, model, fieldID(1), 7, nil)
	aCopy := scalar(t, model, fieldID(1), 7, nil)
	b := scalar(t, model, fieldID(2), 7, nil)
	input := logical(t, model, ir.LogicalOr, a, aCopy, b)

	got := normalize(t, input)
	_, children, ok := got.Logical()
	if !ok || len(children) != 2 {
		t.Fatalf("typed duplicate normalization retained %d children", len(children))
	}
	if bytes.Equal(canonical(t, children[0]), canonical(t, children[1])) {
		t.Fatal("distinct typed children were collapsed")
	}
}

func TestConditionDoesNotApplyForbiddenBooleanOrNullableRewrites(t *testing.T) {
	model := modelID(1)
	typ, err := ir.NewTypeRef(ir.ValueInt64, true, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := ir.SignedValue(ir.ValueInt64, 1)
	operand, _ := ir.OneOperand(value)
	a, _ := ir.NewScalar(model, fieldID(1), typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	b := scalar(t, model, fieldID(2), 2, nil)
	input := logical(t, model, ir.LogicalNot, logical(t, model, ir.LogicalAnd, a, b))

	got := normalize(t, input)
	operator, children, ok := got.Logical()
	if !ok || operator != ir.LogicalNot || len(children) != 1 {
		t.Fatal("not(and) was rewritten")
	}
	inner, innerChildren, ok := children[0].Logical()
	if !ok || inner != ir.LogicalAnd || len(innerChildren) != 2 {
		t.Fatal("De Morgan or distribution rewrite was applied")
	}
	for _, child := range innerChildren {
		if child.Kind() == ir.ConditionLogical {
			t.Fatal("nullable comparison was pushed through not")
		}
	}

	c := scalar(t, model, fieldID(3), 3, nil)
	distributiveInput := logical(t, model, ir.LogicalAnd, a, logical(t, model, ir.LogicalOr, b, c))
	distributiveOutput := normalize(t, distributiveInput)
	outerOperator, outerChildren, ok := distributiveOutput.Logical()
	if !ok || outerOperator != ir.LogicalAnd || len(outerChildren) != 2 {
		t.Fatal("and/or expression was distributed")
	}
	foundOr := false
	for _, child := range outerChildren {
		childOperator, _, logicalChild := child.Logical()
		foundOr = foundOr || logicalChild && childOperator == ir.LogicalOr
	}
	if !foundOr {
		t.Fatal("nested or was removed by a forbidden distributive rewrite")
	}
}

func TestConditionPreservesRelationSemanticsAndRecomputesRequirements(t *testing.T) {
	root := modelID(1)
	post := modelID(2)
	comment := modelID(3)
	providers := ir.PortableProviders()
	childRequirement, _ := ir.NewRequirement(providers, ir.CapabilityBinaryText)
	relationRequirement, _ := ir.NewRequirement(providers, ir.CapabilityRelationCorrelation)

	childLeaf := scalar(t, comment, fieldID(30), 1, []ir.Requirement{childRequirement})
	childTrue := constant(t, comment, true)
	simplifiedChild := logical(t, comment, ir.LogicalOr, childLeaf, childTrue)
	inner, err := ir.NewRelation(post, fieldID(20), relationID(20), comment, ir.RelationToMany, ir.OperatorRelationEvery, &simplifiedChild, []ir.Requirement{relationRequirement})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := ir.NewRelation(root, fieldID(10), relationID(10), post, ir.RelationToMany, ir.OperatorRelationSome, &inner, []ir.Requirement{relationRequirement})
	if err != nil {
		t.Fatal(err)
	}

	got := normalize(t, outer)
	_, _, outerTarget, outerCardinality, outerChild, ok := got.Relation()
	if !ok || outerTarget != post || outerCardinality != ir.RelationToMany || outerChild == nil {
		t.Fatal("outer relation identity or cardinality changed")
	}
	outerOperator, _ := got.Operator()
	if outerOperator != ir.OperatorRelationSome {
		t.Fatal("outer relation quantifier changed")
	}
	_, _, innerTarget, innerCardinality, innerChild, ok := outerChild.Relation()
	if !ok || innerTarget != comment || innerCardinality != ir.RelationToMany || innerChild == nil {
		t.Fatal("nested relation identity or cardinality changed")
	}
	innerOperator, _ := outerChild.Operator()
	if innerOperator != ir.OperatorRelationEvery {
		t.Fatal("Every was rewritten")
	}
	truth, constantChild := innerChild.Constant()
	if !constantChild || !truth {
		t.Fatal("nested relation child was not recursively normalized")
	}
	for _, requirement := range got.Requirements() {
		if requirement.Capability() == ir.CapabilityBinaryText {
			t.Fatal("removed child left a stale capability requirement")
		}
	}
	if requirements := got.Requirements(); len(requirements) != 1 || requirements[0] != relationRequirement {
		t.Fatalf("relation requirements = %#v", requirements)
	}
}

func TestConditionPreservesRelationPresenceAndJSONWholeDocument(t *testing.T) {
	model := modelID(1)
	target := modelID(2)
	presence, err := ir.NewRelation(model, fieldID(1), relationID(1), target, ir.RelationToOne, ir.OperatorRelationIsNull, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotPresence := normalize(t, presence)
	if !bytes.Equal(canonical(t, presence), canonical(t, gotPresence)) {
		t.Fatal("to-one absence was folded from relation metadata")
	}

	typ, _ := ir.NewTypeRef(ir.ValueJSON, true, 0, 0, ir.EnumID{}, nil, ir.CapabilityExactJSON)
	path, _ := ir.NewJSONPath()
	operand, _ := ir.JSONNullOperand(ir.JSONAnyNull)
	jsonNode, err := ir.NewJSON(model, fieldID(2), typ, ir.OperatorJSONIsNull, ir.ComparisonSensitive, path, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON := normalize(t, jsonNode)
	gotPath, ok := gotJSON.Path()
	if !ok || len(gotPath.Segments()) != 0 {
		t.Fatal("whole-document JSON path was changed")
	}
}

func TestConditionRebuildsListLeafWithoutChangingTypedIdentity(t *testing.T) {
	model := modelID(1)
	element, _ := ir.NewTypeRef(ir.ValueString, false, 0, 0, ir.EnumID{}, nil, 0)
	typ, _ := ir.NewTypeRef(ir.ValueScalarList, true, 0, 0, ir.EnumID{}, &element, ir.CapabilityScalarListJSON)
	value, _ := ir.StringValue("golem")
	operand, _ := ir.OneOperand(value)
	requirement, _ := ir.NewRequirement(ir.PortableProviders(), ir.CapabilityScalarListJSON)
	input, err := ir.NewList(model, fieldID(1), typ, ir.OperatorListHas, operand, []ir.Requirement{requirement})
	if err != nil {
		t.Fatal(err)
	}
	got := normalize(t, input)
	if !bytes.Equal(canonical(t, input), canonical(t, got)) {
		t.Fatal("list leaf identity changed")
	}
}

func TestConditionRejectsZeroInput(t *testing.T) {
	if _, err := Condition(ir.Condition{}); err == nil {
		t.Fatal("zero condition accepted")
	}
}

func TestPolicyCanonicalizesConditionsWithoutChangingRuleSemantics(t *testing.T) {
	model := modelID(1)
	firstField := fieldID(10)
	secondField := fieldID(11)
	a := scalar(t, model, fieldID(1), 1, nil)
	b := scalar(t, model, fieldID(2), 2, nil)

	left := policy(t, model,
		modelRule(t, ir.ActionRead, ir.EffectGrant, model,
			logical(t, model, ir.LogicalAnd, a, b, a), 0),
		fieldRule(t, ir.ActionUpdate, ir.EffectDeny, model, nil,
			firstField, []ir.FieldID{secondField}, 1),
	)
	right := policy(t, model,
		modelRule(t, ir.ActionRead, ir.EffectGrant, model,
			logical(t, model, ir.LogicalAnd, b, a), 0),
		fieldRule(t, ir.ActionUpdate, ir.EffectDeny, model, nil,
			firstField, []ir.FieldID{secondField}, 1),
	)

	normalizedLeft := normalizePolicy(t, left)
	normalizedRight := normalizePolicy(t, right)
	if !bytes.Equal(canonicalPolicy(t, normalizedLeft), canonicalPolicy(t, normalizedRight)) {
		t.Fatal("permitted commutative shuffles produced different canonical policy bytes")
	}

	rules := normalizedLeft.Rules()
	if len(rules) != 2 {
		t.Fatalf("rule count = %d", len(rules))
	}
	if rules[0].Position() != 0 || rules[0].Action() != ir.ActionRead || rules[0].Effect() != ir.EffectGrant {
		t.Fatal("model rule identity changed")
	}
	if fields, modelWide := rules[0].Fields(); !modelWide || fields != nil {
		t.Fatal("model-wide rule became field-scoped")
	}
	fields, modelWide := rules[1].Fields()
	if modelWide || len(fields) != 2 || fields[0] != firstField || fields[1] != secondField {
		t.Fatalf("field rule order or scope changed: modelWide=%v fields=%v", modelWide, fields)
	}
	if rules[1].Position() != 1 || rules[1].Action() != ir.ActionUpdate || rules[1].Effect() != ir.EffectDeny {
		t.Fatal("field rule identity changed")
	}
	if _, present := rules[1].Condition(); present {
		t.Fatal("unconditional field rule gained a condition")
	}
}

func TestPolicyCanonicalBytesRemainSensitiveToRuleOrder(t *testing.T) {
	model := modelID(1)
	a := scalar(t, model, fieldID(1), 1, nil)
	b := scalar(t, model, fieldID(2), 2, nil)

	first := normalizePolicy(t, policy(t, model,
		modelRule(t, ir.ActionRead, ir.EffectGrant, model, a, 0),
		modelRule(t, ir.ActionRead, ir.EffectDeny, model, b, 1),
	))
	reordered := normalizePolicy(t, policy(t, model,
		modelRule(t, ir.ActionRead, ir.EffectDeny, model, b, 0),
		modelRule(t, ir.ActionRead, ir.EffectGrant, model, a, 1),
	))
	if bytes.Equal(canonicalPolicy(t, first), canonicalPolicy(t, reordered)) {
		t.Fatal("semantic rule reorder produced identical canonical policy bytes")
	}
}

func TestPolicyIsIdempotentDeterministicAndCopyIsolated(t *testing.T) {
	model := modelID(1)
	fields := []ir.FieldID{fieldID(11), fieldID(12)}
	a := scalar(t, model, fieldID(1), 1, nil)
	b := scalar(t, model, fieldID(2), 2, nil)
	input := policy(t, model,
		fieldRule(t, ir.ActionRead, ir.EffectGrant, model,
			conditionPointer(logical(t, model, ir.LogicalOr, b, a, b)), fields[0], fields[1:], 0),
	)
	before := append([]byte(nil), canonicalPolicy(t, input)...)

	first := normalizePolicy(t, input)
	second := normalizePolicy(t, input)
	again := normalizePolicy(t, first)
	firstBytes := canonicalPolicy(t, first)
	if !bytes.Equal(firstBytes, canonicalPolicy(t, second)) {
		t.Fatal("repeated construction produced different canonical policy bytes")
	}
	if !bytes.Equal(firstBytes, canonicalPolicy(t, again)) {
		t.Fatal("policy normalization is not idempotent")
	}
	if !bytes.Equal(before, canonicalPolicy(t, input)) {
		t.Fatal("policy normalization mutated its input")
	}

	readRules := first.Rules()
	readFields, _ := readRules[0].Fields()
	readFields[0] = fieldID(99)
	readCondition, _ := readRules[0].Condition()
	childrenOperator, children, ok := readCondition.Logical()
	if !ok || childrenOperator != ir.LogicalOr {
		t.Fatal("normalized condition is not the expected logical node")
	}
	children[0] = constant(t, model, false)
	if !bytes.Equal(firstBytes, canonicalPolicy(t, first)) {
		t.Fatal("mutating getter results changed normalized policy")
	}
}

func TestPolicyRejectsZeroInput(t *testing.T) {
	if _, err := Policy(ir.Policy{}); err == nil {
		t.Fatal("zero policy accepted")
	}
}

func normalize(t *testing.T, input ir.Condition) ir.Condition {
	t.Helper()
	output, err := Condition(input)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func canonical(t *testing.T, input ir.Condition) []byte {
	t.Helper()
	output, err := ir.CanonicalCondition(input)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func normalizePolicy(t *testing.T, input ir.Policy) ir.Policy {
	t.Helper()
	output, err := Policy(input)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func canonicalPolicy(t *testing.T, input ir.Policy) []byte {
	t.Helper()
	output, err := ir.CanonicalPolicy(input)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func policy(t *testing.T, model ir.ModelID, rules ...ir.Rule) ir.Policy {
	t.Helper()
	output, err := ir.NewPolicy(model, rules)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func modelRule(t *testing.T, action ir.Action, effect ir.Effect, model ir.ModelID, condition ir.Condition, position uint32) ir.Rule {
	t.Helper()
	var conditionPointer *ir.Condition
	if condition.Kind() != 0 {
		conditionPointer = &condition
	}
	output, err := ir.NewModelRule(action, effect, model, conditionPointer, position)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func fieldRule(t *testing.T, action ir.Action, effect ir.Effect, model ir.ModelID, condition *ir.Condition, first ir.FieldID, rest []ir.FieldID, position uint32) ir.Rule {
	t.Helper()
	output, err := ir.NewFieldRule(action, effect, model, condition, first, rest, position)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func conditionPointer(condition ir.Condition) *ir.Condition { return &condition }

func logical(t *testing.T, model ir.ModelID, operator ir.LogicalOperator, children ...ir.Condition) ir.Condition {
	t.Helper()
	output, err := ir.NewLogical(model, operator, children)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func constant(t *testing.T, model ir.ModelID, truth bool) ir.Condition {
	t.Helper()
	output, err := ir.NewConstant(model, truth)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func scalar(t *testing.T, model ir.ModelID, field ir.FieldID, value int64, requirements []ir.Requirement) ir.Condition {
	t.Helper()
	typ, err := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	scalar, err := ir.SignedValue(ir.ValueInt64, value)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := ir.OneOperand(scalar)
	if err != nil {
		t.Fatal(err)
	}
	output, err := ir.NewScalar(model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func modelID(last byte) (value ir.ModelID)       { value[15] = last; return }
func fieldID(last byte) (value ir.FieldID)       { value[15] = last; return }
func relationID(last byte) (value ir.RelationID) { value[15] = last; return }
