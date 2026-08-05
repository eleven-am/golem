package resolve

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

var (
	testModel = modelID(1)
	fieldA    = fieldID(1)
	fieldB    = fieldID(2)
	fieldC    = fieldID(3)
)

type testRow map[ir.FieldID]bool

type ruleScope uint8

const (
	scopeModel ruleScope = iota + 1
	scopeFieldA
)

type declaration struct {
	name      string
	effect    ir.Effect
	scope     ruleScope
	condition *ir.Condition
}

func TestResolutionAgreesWithIndependentFirstMatchOracle(t *testing.T) {
	conditionA := scalarBoolCondition(t, fieldA, true)
	conditionB := scalarBoolCondition(t, fieldB, true)
	alphabet := []declaration{
		{name: "model-grant", effect: ir.EffectGrant, scope: scopeModel},
		{name: "model-deny", effect: ir.EffectDeny, scope: scopeModel},
		{name: "field-grant", effect: ir.EffectGrant, scope: scopeFieldA},
		{name: "field-deny", effect: ir.EffectDeny, scope: scopeFieldA},
		{name: "model-grant-a", effect: ir.EffectGrant, scope: scopeModel, condition: &conditionA},
		{name: "model-deny-a", effect: ir.EffectDeny, scope: scopeModel, condition: &conditionA},
		{name: "field-grant-a", effect: ir.EffectGrant, scope: scopeFieldA, condition: &conditionA},
		{name: "field-deny-a", effect: ir.EffectDeny, scope: scopeFieldA, condition: &conditionA},
		{name: "model-grant-b", effect: ir.EffectGrant, scope: scopeModel, condition: &conditionB},
		{name: "field-deny-b", effect: ir.EffectDeny, scope: scopeFieldA, condition: &conditionB},
	}
	rows := []testRow{
		{fieldA: false, fieldB: false},
		{fieldA: false, fieldB: true},
		{fieldA: true, fieldB: false},
		{fieldA: true, fieldB: true},
	}

	var chains int
	seenTrue := false
	seenFalse := false
	seenDiscriminating := false
	forEachChain(alphabet, 3, func(chain []declaration) {
		chains++
		policy := buildPolicy(t, chain)
		rowCondition := mustRowConstraint(t, policy, ir.ActionRead, testModel)
		fieldACondition := mustFieldCondition(t, policy, ir.ActionRead, testModel, fieldA)
		fieldCCondition := mustFieldCondition(t, policy, ir.ActionRead, testModel, fieldC)

		rowAnswers := make([]bool, len(rows))
		for index, row := range rows {
			rowWant := directFirstMatch(policy, ir.ActionRead, testModel, ir.FieldID{}, false, row)
			rowGot := evaluate(t, rowCondition, row)
			if rowGot != rowWant {
				t.Fatalf("row mismatch for chain %s row %d: got %t want %t", chainName(chain), index, rowGot, rowWant)
			}
			rowAnswers[index] = rowGot

			for _, question := range []struct {
				name      string
				field     ir.FieldID
				condition ir.Condition
			}{
				{name: "named", field: fieldA, condition: fieldACondition},
				{name: "unnamed", field: fieldC, condition: fieldCCondition},
			} {
				want := directFirstMatch(policy, ir.ActionRead, testModel, question.field, true, row)
				got := evaluate(t, question.condition, row)
				if got != want {
					t.Fatalf("field %s mismatch for chain %s row %d: got %t want %t", question.name, chainName(chain), index, got, want)
				}
			}
		}

		allTrue, allFalse := true, true
		for _, answer := range rowAnswers {
			allTrue = allTrue && answer
			allFalse = allFalse && !answer
		}
		seenTrue = seenTrue || allTrue
		seenFalse = seenFalse || allFalse
		seenDiscriminating = seenDiscriminating || !allTrue && !allFalse
	})

	if chains != 1111 {
		t.Fatalf("exhausted %d chains, want 1111", chains)
	}
	if !seenTrue || !seenFalse || !seenDiscriminating {
		t.Fatalf("oracle coverage incomplete: all-true=%t all-false=%t discriminating=%t", seenTrue, seenFalse, seenDiscriminating)
	}
}

func TestNamedPriorityAndScopeMutations(t *testing.T) {
	draft := scalarBoolCondition(t, fieldA, true)
	owned := scalarBoolCondition(t, fieldB, true)

	t.Run("M-FIELD-NO-CARRY_and_M-OPEN-GRANT-WINS", func(t *testing.T) {
		policy := buildPolicy(t, []declaration{
			{name: "grant", effect: ir.EffectGrant, scope: scopeModel},
			{name: "deny-draft", effect: ir.EffectDeny, scope: scopeFieldA, condition: &draft},
		})
		condition := mustFieldCondition(t, policy, ir.ActionRead, testModel, fieldA)
		if evaluate(t, condition, testRow{fieldA: true}) {
			t.Fatal("newer conditional field denial must protect matching rows")
		}
		if !evaluate(t, condition, testRow{fieldA: false}) {
			t.Fatal("older grant must survive outside the newer denial")
		}
	})

	t.Run("M-ROW-KEEPS-FIELD-DENY", func(t *testing.T) {
		policy := buildPolicy(t, []declaration{
			{name: "grant", effect: ir.EffectGrant, scope: scopeModel},
			{name: "deny-field", effect: ir.EffectDeny, scope: scopeFieldA},
		})
		condition := mustRowConstraint(t, policy, ir.ActionRead, testModel)
		if !evaluate(t, condition, testRow{}) {
			t.Fatal("field-scoped denial must be absent from the row chain")
		}
	})

	t.Run("M-ROW-DROPS-FIELD-GRANT", func(t *testing.T) {
		policy := buildPolicy(t, []declaration{
			{name: "grant-owned-field", effect: ir.EffectGrant, scope: scopeFieldA, condition: &owned},
		})
		condition := mustRowConstraint(t, policy, ir.ActionRead, testModel)
		if !evaluate(t, condition, testRow{fieldB: true}) || evaluate(t, condition, testRow{fieldB: false}) {
			t.Fatal("field-scoped grant must retain its condition in the row chain")
		}
	})

	t.Run("M-CHAIN-ORDER-FORWARD_and_M-DENY-ABSENT", func(t *testing.T) {
		grantThenDeny := buildPolicy(t, []declaration{
			{name: "grant", effect: ir.EffectGrant, scope: scopeModel},
			{name: "deny", effect: ir.EffectDeny, scope: scopeModel},
		})
		if evaluate(t, mustRowConstraint(t, grantThenDeny, ir.ActionRead, testModel), testRow{}) {
			t.Fatal("newer unconditional denial must win")
		}

		denyThenGrant := buildPolicy(t, []declaration{
			{name: "deny", effect: ir.EffectDeny, scope: scopeModel},
			{name: "grant", effect: ir.EffectGrant, scope: scopeModel},
		})
		if !evaluate(t, mustRowConstraint(t, denyThenGrant, ir.ActionRead, testModel), testRow{}) {
			t.Fatal("newer unconditional grant must win")
		}
	})

	t.Run("M-FIELD-PRIORITY-BLIND", func(t *testing.T) {
		policy := buildPolicy(t, []declaration{
			{name: "grant-all", effect: ir.EffectGrant, scope: scopeModel},
			{name: "deny-draft", effect: ir.EffectDeny, scope: scopeFieldA, condition: &draft},
			{name: "grant-owned", effect: ir.EffectGrant, scope: scopeFieldA, condition: &owned},
		})
		condition := mustFieldCondition(t, policy, ir.ActionRead, testModel, fieldA)
		if !evaluate(t, condition, testRow{fieldA: true, fieldB: true}) {
			t.Fatal("newest matching conditional grant must outrank older denial")
		}
		if evaluate(t, condition, testRow{fieldA: true, fieldB: false}) {
			t.Fatal("conditional denial must protect rows not claimed by newer grant")
		}
	})

	t.Run("M-EMPTY-OPEN", func(t *testing.T) {
		policy := buildPolicy(t, []declaration{
			{name: "deny-field", effect: ir.EffectDeny, scope: scopeFieldA},
		})
		condition := mustFieldCondition(t, policy, ir.ActionRead, testModel, fieldA)
		if evaluate(t, condition, testRow{}) {
			t.Fatal("a lone field-scoped denial must resolve to None")
		}
	})

	t.Run("M-ROW-DENY-NOT-CARRIED", func(t *testing.T) {
		policy := buildPolicy(t, []declaration{
			{name: "grant-owned", effect: ir.EffectGrant, scope: scopeModel, condition: &owned},
			{name: "deny-draft", effect: ir.EffectDeny, scope: scopeModel, condition: &draft},
		})
		condition := mustRowConstraint(t, policy, ir.ActionRead, testModel)
		if evaluate(t, condition, testRow{fieldA: true, fieldB: true}) {
			t.Fatal("newer row denial must narrow every older grant")
		}
		if !evaluate(t, condition, testRow{fieldA: false, fieldB: true}) {
			t.Fatal("older grant must remain reachable outside the newer denial")
		}
	})
}

func TestActionGateIsDerivedOnlyFromRowConstraint(t *testing.T) {
	empty := buildPolicy(t, nil)
	none := mustRowConstraint(t, empty, ir.ActionRead, testModel)
	allowed, err := ActionAllowed(none)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("empty row chain must fail the action gate")
	}

	conditional := scalarBoolCondition(t, fieldA, true)
	policy := buildPolicy(t, []declaration{{name: "conditional", effect: ir.EffectGrant, scope: scopeModel, condition: &conditional}})
	constraint := mustRowConstraint(t, policy, ir.ActionRead, testModel)
	allowed, err = ActionAllowed(constraint)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("a non-None row constraint must pass the action gate")
	}

	all, err := ir.NewConstant(testModel, true)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = ActionAllowed(all)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("the true row constraint must pass the action gate")
	}

	falsehood, err := ir.NewConstant(testModel, false)
	if err != nil {
		t.Fatal(err)
	}
	notConditional := mustNot(t, conditional)
	reducibleNone := mustAnd(t, falsehood, notConditional)
	allowed, err = ActionAllowed(reducibleNone)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("a row constraint normalized to None must fail the action gate")
	}
}

func TestResolvedNoneIsNormalizedBeforeTheGate(t *testing.T) {
	falsehood, err := ir.NewConstant(testModel, false)
	if err != nil {
		t.Fatal(err)
	}
	denial := scalarBoolCondition(t, fieldA, true)
	policy := buildPolicy(t, []declaration{
		{name: "never-grant", effect: ir.EffectGrant, scope: scopeModel, condition: &falsehood},
		{name: "conditional-denial", effect: ir.EffectDeny, scope: scopeModel, condition: &denial},
	})

	row := mustRowConstraint(t, policy, ir.ActionRead, testModel)
	truth, constant := row.Constant()
	if !constant || truth {
		t.Fatal("resolved impossible grant chain must normalize to the false constant")
	}
	allowed, err := ActionAllowed(row)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("normalized None row constraint must fail the action gate")
	}
}

func TestFieldWalkPreservesMutuallyExclusiveSymbolicShape(t *testing.T) {
	conditionA := scalarBoolCondition(t, fieldA, true)
	conditionB := scalarBoolCondition(t, fieldB, true)
	policy := buildPolicy(t, []declaration{
		{name: "open-grant", effect: ir.EffectGrant, scope: scopeModel},
		{name: "grant-b", effect: ir.EffectGrant, scope: scopeModel, condition: &conditionB},
		{name: "deny-a", effect: ir.EffectDeny, scope: scopeModel, condition: &conditionA},
	})

	got := mustFieldCondition(t, policy, ir.ActionRead, testModel, fieldA)
	notA := mustNot(t, conditionA)
	notB := mustNot(t, conditionB)
	guardedB := mustAnd(t, conditionB, notA)
	fallback := mustAnd(t, notA, notB)
	want := mustOr(t, guardedB, fallback)
	assertSameCondition(t, got, want)
}

func TestRowWalkPreservesDenialsOnEveryOlderGrant(t *testing.T) {
	conditionA := scalarBoolCondition(t, fieldA, true)
	conditionB := scalarBoolCondition(t, fieldB, true)
	policy := buildPolicy(t, []declaration{
		{name: "open-grant", effect: ir.EffectGrant, scope: scopeModel},
		{name: "grant-b", effect: ir.EffectGrant, scope: scopeModel, condition: &conditionB},
		{name: "deny-a", effect: ir.EffectDeny, scope: scopeModel, condition: &conditionA},
	})

	got := mustRowConstraint(t, policy, ir.ActionRead, testModel)
	notA := mustNot(t, conditionA)
	guardedB := mustAnd(t, conditionB, notA)
	want := mustOr(t, guardedB, notA)
	assertSameCondition(t, got, want)
}

func TestResolutionTraceUsesVisitedDeclarationPositions(t *testing.T) {
	condition := scalarBoolCondition(t, fieldA, true)
	policy := buildPolicy(t, []declaration{
		{name: "unreachable-old", effect: ir.EffectGrant, scope: scopeModel, condition: &condition},
		{name: "terminal", effect: ir.EffectGrant, scope: scopeModel},
		{name: "newer-conditional", effect: ir.EffectDeny, scope: scopeModel, condition: &condition},
	})
	_, rowTrace, err := rowConstraint(policy, ir.ActionRead, testModel)
	if err != nil {
		t.Fatal(err)
	}
	assertPositions(t, rowTrace, []uint32{2, 1})

	_, fieldTrace, err := fieldCondition(policy, ir.ActionRead, testModel, fieldA)
	if err != nil {
		t.Fatal(err)
	}
	assertPositions(t, fieldTrace, []uint32{2, 1})
}

func TestChainSelectionPreservesFirstSeenFieldOrder(t *testing.T) {
	rule, err := ir.NewFieldRule(
		ir.ActionRead,
		ir.EffectGrant,
		testModel,
		nil,
		fieldB,
		[]ir.FieldID{fieldA, fieldC},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ir.NewPolicy(testModel, []ir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}

	chain := chainForField(policy, ir.ActionRead, testModel, fieldA)
	if len(chain) != 1 {
		t.Fatalf("field chain has %d rules, want 1", len(chain))
	}
	fields, modelWide := chain[0].Fields()
	if modelWide {
		t.Fatal("field-scoped rule became model-wide")
	}
	want := []ir.FieldID{fieldB, fieldA, fieldC}
	if len(fields) != len(want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}
	for index := range want {
		if fields[index] != want[index] {
			t.Fatalf("fields = %v, want first-seen order %v", fields, want)
		}
	}
}

func TestResolutionRejectsInvalidQuestions(t *testing.T) {
	policy := buildPolicy(t, nil)
	if _, err := RowConstraint(policy, 0, testModel); err == nil {
		t.Fatal("expected invalid action rejection")
	}
	if _, err := RowConstraint(policy, ir.ActionRead, modelID(2)); err == nil {
		t.Fatal("expected model mismatch rejection")
	}
	if _, err := FieldCondition(policy, ir.ActionRead, testModel, ir.FieldID{}); err == nil {
		t.Fatal("expected zero field rejection")
	}
	if _, err := ActionAllowed(ir.Condition{}); err == nil {
		t.Fatal("expected zero row constraint rejection")
	}
}

func forEachChain(alphabet []declaration, maxDepth int, visit func([]declaration)) {
	visit(nil)
	level := [][]declaration{{}}
	for depth := 1; depth <= maxDepth; depth++ {
		next := make([][]declaration, 0, len(level)*len(alphabet))
		for _, prefix := range level {
			for _, item := range alphabet {
				chain := append(append([]declaration(nil), prefix...), item)
				visit(chain)
				next = append(next, chain)
			}
		}
		level = next
	}
}

func buildPolicy(t *testing.T, declarations []declaration) ir.Policy {
	t.Helper()
	rules := make([]ir.Rule, len(declarations))
	for index, declaration := range declarations {
		var (
			rule ir.Rule
			err  error
		)
		if declaration.scope == scopeModel {
			rule, err = ir.NewModelRule(ir.ActionRead, declaration.effect, testModel, declaration.condition, uint32(index))
		} else {
			rule, err = ir.NewFieldRule(ir.ActionRead, declaration.effect, testModel, declaration.condition, fieldA, nil, uint32(index))
		}
		if err != nil {
			t.Fatalf("build rule %d: %v", index, err)
		}
		rules[index] = rule
	}
	policy, err := ir.NewPolicy(testModel, rules)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	return policy
}

func directFirstMatch(policy ir.Policy, action ir.Action, model ir.ModelID, field ir.FieldID, fieldLens bool, row testRow) bool {
	rules := policy.Rules()
	for index := len(rules) - 1; index >= 0; index-- {
		rule := rules[index]
		if rule.Action() != action || rule.ModelID() != model {
			continue
		}
		fields, modelWide := rule.Fields()
		if fieldLens {
			if !modelWide && !testContainsField(fields, field) {
				continue
			}
		} else if !modelWide && rule.Effect() == ir.EffectDeny {
			continue
		}
		condition, conditional := rule.Condition()
		if conditional && !evaluate(nil, condition, row) {
			continue
		}
		return rule.Effect() == ir.EffectGrant
	}
	return false
}

func evaluate(t *testing.T, condition ir.Condition, row testRow) bool {
	if truth, ok := condition.Constant(); ok {
		return truth
	}
	if operator, children, ok := condition.Logical(); ok {
		switch operator {
		case ir.LogicalAnd:
			for _, child := range children {
				if !evaluate(t, child, row) {
					return false
				}
			}
			return true
		case ir.LogicalOr:
			for _, child := range children {
				if evaluate(t, child, row) {
					return true
				}
			}
			return false
		case ir.LogicalNot:
			return !evaluate(t, children[0], row)
		}
	}
	field, fieldOK := condition.Field()
	operator, operatorOK := condition.Operator()
	operand, operandOK := condition.Operand()
	value, valueOK := operand.One()
	want, boolOK := value.Bool()
	if fieldOK && operatorOK && operandOK && valueOK && boolOK && operator == ir.OperatorEqual {
		return row[field] == want
	}
	if t != nil {
		t.Fatalf("test evaluator received unsupported condition kind %d", condition.Kind())
	}
	panic("test evaluator received unsupported condition")
}

func scalarBoolCondition(t *testing.T, field ir.FieldID, value bool) ir.Condition {
	t.Helper()
	typ, err := ir.NewTypeRef(ir.ValueBool, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := ir.OneOperand(ir.BoolValue(value))
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewScalar(testModel, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func mustNot(t *testing.T, condition ir.Condition) ir.Condition {
	t.Helper()
	result, err := ir.NewLogical(testModel, ir.LogicalNot, []ir.Condition{condition})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustAnd(t *testing.T, conditions ...ir.Condition) ir.Condition {
	t.Helper()
	result, err := ir.NewLogical(testModel, ir.LogicalAnd, conditions)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustOr(t *testing.T, conditions ...ir.Condition) ir.Condition {
	t.Helper()
	result, err := ir.NewLogical(testModel, ir.LogicalOr, conditions)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSameCondition(t *testing.T, got, want ir.Condition) {
	t.Helper()
	var err error
	want, err = normalize.Condition(want)
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := ir.CanonicalCondition(got)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := ir.CanonicalCondition(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("condition canonical shape differs:\n got %x\nwant %x", gotBytes, wantBytes)
	}
}

func mustRowConstraint(t *testing.T, policy ir.Policy, action ir.Action, model ir.ModelID) ir.Condition {
	t.Helper()
	condition, err := RowConstraint(policy, action, model)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func mustFieldCondition(t *testing.T, policy ir.Policy, action ir.Action, model ir.ModelID, field ir.FieldID) ir.Condition {
	t.Helper()
	condition, err := FieldCondition(policy, action, model, field)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func assertPositions(t *testing.T, got, want []uint32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("trace positions %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("trace positions %v, want %v", got, want)
		}
	}
}

func testContainsField(fields []ir.FieldID, field ir.FieldID) bool {
	for _, candidate := range fields {
		if candidate == field {
			return true
		}
	}
	return false
}

func chainName(chain []declaration) string {
	if len(chain) == 0 {
		return "empty"
	}
	name := chain[0].name
	for _, item := range chain[1:] {
		name += "/" + item.name
	}
	return name
}

func modelID(value byte) ir.ModelID {
	var id ir.ModelID
	id[len(id)-1] = value
	return id
}

func fieldID(value byte) ir.FieldID {
	var id ir.FieldID
	id[len(id)-1] = value
	return id
}
