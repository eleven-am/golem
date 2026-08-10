package classify

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestFieldsClassifiesAlwaysConditionalAndNever(t *testing.T) {
	model := modelID(1)
	visible, scoped, absent := fieldID(1), fieldID(2), fieldID(3)
	mine := scalar(t, model, fieldID(4), 7)
	registry := identities(model, visible, scoped, absent, fieldID(4))
	policy := newPolicy(t, model,
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant},
		ruleSpec{action: ir.ActionRead, effect: ir.EffectDeny, fields: []ir.FieldID{absent}},
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &mine, fields: []ir.FieldID{scoped}},
	)

	request, err := NewRequest(registry, policy, model, []ir.FieldID{visible, scoped, absent}, UseProjection, ir.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Classifications()
	if len(got) != 3 || got[0].FieldID() != visible || got[1].FieldID() != scoped || got[2].FieldID() != absent {
		t.Fatalf("classification order = %#v", got)
	}
	if got[0].Access() != AccessAlways || got[0].DischargedByConstraint() {
		t.Fatalf("always classification = access %d discharged %v", got[0].Access(), got[0].DischargedByConstraint())
	}
	if got[1].Access() != AccessConditional || !got[1].DischargedByConstraint() {
		t.Fatalf("conditional classification = access %d discharged %v", got[1].Access(), got[1].DischargedByConstraint())
	}
	if requires := got[1].Requires(); len(requires) != 1 || requires[0] != fieldID(4) {
		t.Fatalf("conditional requires = %x", requires)
	}
	condition, ok := got[1].FieldCondition()
	if !ok {
		t.Fatal("conditional classification omitted its mask condition")
	}
	if err := condition.Validate(); err != nil || condition.ModelID() != model {
		t.Fatalf("invalid resolved field condition: model=%x err=%v", condition.ModelID(), err)
	}
	if got[2].Access() != AccessNever || got[2].DischargedByConstraint() {
		t.Fatalf("never classification = access %d discharged %v", got[2].Access(), got[2].DischargedByConstraint())
	}
	for _, result := range []Classification{got[0], got[2]} {
		if result.Dependencies().ModelID() != model || len(result.Dependencies().Entries()) != 0 {
			t.Fatal("unconditional classification did not retain an empty model-rooted dependency tree")
		}
		if _, ok := result.FieldCondition(); ok {
			t.Fatal("unconditional classification exposed a conditional mask")
		}
	}
}

func TestFieldsPinsSiblingFieldGrantDischargeRegression(t *testing.T) {
	model := modelID(1)
	body, title := fieldID(1), fieldID(2)
	authorID, status := fieldID(3), fieldID(4)
	mine := scalar(t, model, authorID, 11)
	published := scalar(t, model, status, 1)
	policy := newPolicy(t, model,
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &mine},
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &published, fields: []ir.FieldID{title}},
	)
	request, err := NewRequest(identities(model, body, title, authorID, status), policy, model, []ir.FieldID{body}, UseFilter, ir.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	classification := plan.Classifications()[0]
	if classification.Access() != AccessConditional {
		t.Fatalf("body access = %d", classification.Access())
	}
	if classification.DischargedByConstraint() {
		t.Fatal("sibling title grant widened row reach but incorrectly discharged body")
	}
	if requires := classification.Requires(); len(requires) != 1 || requires[0] != authorID {
		t.Fatalf("body requires = %x", requires)
	}
}

func TestFieldsUsesActualWriteSelectingConstraint(t *testing.T) {
	model := modelID(1)
	title, owner := fieldID(1), fieldID(2)
	mine := scalar(t, model, owner, 9)

	tests := []struct {
		name       string
		updateRule ruleSpec
		want       bool
	}{
		{
			name:       "unconstrained update exceeds read reach",
			updateRule: ruleSpec{action: ir.ActionUpdate, effect: ir.EffectGrant},
			want:       false,
		},
		{
			name:       "matching update stays inside read reach",
			updateRule: ruleSpec{action: ir.ActionUpdate, effect: ir.EffectGrant, condition: &mine},
			want:       true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := newPolicy(t, model,
				ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &mine},
				test.updateRule,
			)
			request, err := NewRequest(identities(model, title, owner), policy, model, []ir.FieldID{title}, UseFilter, ir.ActionUpdate)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Fields(request)
			if err != nil {
				t.Fatal(err)
			}
			classification := plan.Classifications()[0]
			if classification.Access() != AccessConditional || classification.DischargedByConstraint() != test.want {
				t.Fatalf("classification = access %d discharged %v, want conditional/%v", classification.Access(), classification.DischargedByConstraint(), test.want)
			}
		})
	}
}

func TestFieldsDischargesWriteReachThatIsOneReadBranch(t *testing.T) {
	model := modelID(1)
	title, owner, status := fieldID(1), fieldID(2), fieldID(3)
	mine := scalar(t, model, owner, 9)
	published := scalar(t, model, status, 1)
	policy := newPolicy(t, model,
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &published},
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &mine},
		ruleSpec{action: ir.ActionUpdate, effect: ir.EffectGrant, condition: &mine},
	)
	request, err := NewRequest(identities(model, title, owner, status), policy, model, []ir.FieldID{title}, UseFilter, ir.ActionUpdate)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	classification := plan.Classifications()[0]
	if classification.Access() != AccessConditional || !classification.DischargedByConstraint() {
		t.Fatalf("one-branch write reach = access %d discharged %v", classification.Access(), classification.DischargedByConstraint())
	}
}

func TestFieldsWriteOnlyPolicyNeverMakesFieldsReadable(t *testing.T) {
	model := modelID(1)
	id, payload := fieldID(1), fieldID(2)
	policy := newPolicy(t, model, ruleSpec{action: ir.ActionUpdate, effect: ir.EffectGrant})
	request, err := NewRequest(identities(model, id, payload), policy, model, []ir.FieldID{id, payload}, UseSelector, ir.ActionUpdate)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, classification := range plan.Classifications() {
		if classification.Access() != AccessNever {
			t.Fatalf("write-only field %x access = %d", classification.FieldID(), classification.Access())
		}
	}
}

func TestFieldsRetainsConservativeUnreachableTailDependencies(t *testing.T) {
	model := modelID(1)
	secret, firstDependency, tailDependency := fieldID(1), fieldID(2), fieldID(3)
	oldScope := scalar(t, model, tailDependency, 1)
	newScope := scalar(t, model, firstDependency, 1)
	policy := newPolicy(t, model,
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &oldScope},
		ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant},
		ruleSpec{action: ir.ActionRead, effect: ir.EffectDeny, condition: &newScope},
	)
	request, err := NewRequest(identities(model, secret, firstDependency, tailDependency), policy, model, []ir.FieldID{secret}, UseProjection, ir.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	classification := plan.Classifications()[0]
	if classification.Access() != AccessConditional {
		t.Fatalf("access = %d", classification.Access())
	}
	requires := classification.Requires()
	if len(requires) != 2 || requires[0] != firstDependency || requires[1] != tailDependency {
		t.Fatalf("requires = %x, want first-seen newest scope then unreachable tail", requires)
	}
	entries := classification.Dependencies().Entries()
	if len(entries) != 2 || entries[0].FieldID() != firstDependency || entries[1].FieldID() != tailDependency {
		t.Fatalf("dependencies = %#v", entries)
	}
}

func TestClassificationAgreesWithIndependentFirstMatchOracle(t *testing.T) {
	model := modelID(1)
	requested, sibling := fieldID(1), fieldID(2)
	aField, bField := fieldID(3), fieldID(4)
	a := scalar(t, model, aField, 1)
	b := scalar(t, model, bField, 1)

	tests := []struct {
		name  string
		rules []ruleSpec
	}{
		{name: "empty"},
		{name: "unconditional grant", rules: []ruleSpec{{action: ir.ActionRead, effect: ir.EffectGrant}}},
		{name: "unconditional denial", rules: []ruleSpec{{action: ir.ActionRead, effect: ir.EffectDeny}}},
		{name: "conditional grant", rules: []ruleSpec{{action: ir.ActionRead, effect: ir.EffectGrant, condition: &a}}},
		{name: "conditional denial without grant", rules: []ruleSpec{{action: ir.ActionRead, effect: ir.EffectDeny, condition: &a}}},
		{name: "conditional grant over denial", rules: []ruleSpec{
			{action: ir.ActionRead, effect: ir.EffectDeny},
			{action: ir.ActionRead, effect: ir.EffectGrant, condition: &a},
		}},
		{name: "conditional denial over grant", rules: []ruleSpec{
			{action: ir.ActionRead, effect: ir.EffectGrant},
			{action: ir.ActionRead, effect: ir.EffectDeny, condition: &a},
		}},
		{name: "two conditional grants", rules: []ruleSpec{
			{action: ir.ActionRead, effect: ir.EffectGrant, condition: &a},
			{action: ir.ActionRead, effect: ir.EffectGrant, condition: &b},
		}},
		{name: "mixed conditional chain", rules: []ruleSpec{
			{action: ir.ActionRead, effect: ir.EffectDeny},
			{action: ir.ActionRead, effect: ir.EffectGrant, condition: &a},
			{action: ir.ActionRead, effect: ir.EffectDeny, condition: &b},
		}},
		{name: "sibling field rule is inapplicable", rules: []ruleSpec{
			{action: ir.ActionRead, effect: ir.EffectGrant, condition: &a},
			{action: ir.ActionRead, effect: ir.EffectGrant, condition: &b, fields: []ir.FieldID{sibling}},
		}},
	}

	registry := identities(model, requested, sibling, aField, bField)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := newPolicy(t, model, test.rules...)
			want := oracleAccess(policy, requested, aField, bField)
			request, err := NewRequest(registry, policy, model, []ir.FieldID{requested}, UseFilter, ir.ActionRead)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Fields(request)
			if err != nil {
				t.Fatal(err)
			}
			if got := plan.Classifications()[0].Access(); got != want {
				t.Fatalf("access = %d, direct first-match oracle = %d", got, want)
			}
		})
	}
}

func TestNewRequestValidatesAndCopyIsolatesTypedBoundary(t *testing.T) {
	model, other := modelID(1), modelID(2)
	first, second, foreign := fieldID(1), fieldID(2), fieldID(3)
	registry := fakeRegistry{
		models: map[golem.ModelID]struct{}{golem.ModelID(model): {}, golem.ModelID(other): {}},
		fields: map[golem.ModelID]map[golem.FieldID]struct{}{
			golem.ModelID(model): {golem.FieldID(first): {}, golem.FieldID(second): {}},
			golem.ModelID(other): {golem.FieldID(foreign): {}},
		},
	}
	policy := newPolicy(t, model, ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant})
	input := []ir.FieldID{first, second, first}
	request, err := NewRequest(registry, policy, model, input, UseOrder, ir.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = foreign
	fields := request.Fields()
	if len(fields) != 2 || fields[0] != first || fields[1] != second {
		t.Fatalf("validated fields = %x", fields)
	}
	fields[0] = foreign
	if request.Fields()[0] != first {
		t.Fatal("request field accessor leaked storage")
	}
	plan, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	classifications := plan.Classifications()
	classifications[0].requires = []ir.FieldID{foreign}
	if got := plan.Classifications()[0].Requires(); len(got) != 0 {
		t.Fatal("classification accessor leaked storage")
	}

	badCases := []struct {
		name   string
		fields []ir.FieldID
		use    UseKind
		action ir.Action
	}{
		{"empty fields", nil, UseFilter, ir.ActionRead},
		{"zero field", []ir.FieldID{{}}, UseFilter, ir.ActionRead},
		{"wrong owner", []ir.FieldID{foreign}, UseFilter, ir.ActionRead},
		{"unknown use", []ir.FieldID{first}, 0, ir.ActionRead},
		{"unknown action", []ir.FieldID{first}, UseFilter, 0},
	}
	for _, test := range badCases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRequest(registry, policy, model, test.fields, test.use, test.action); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
	if _, err := NewRequest((*nilRegistry)(nil), policy, model, []ir.FieldID{first}, UseFilter, ir.ActionRead); err == nil {
		t.Fatal("typed nil registry was accepted")
	}
	if _, err := Fields(Request{}); err == nil {
		t.Fatal("zero request was accepted")
	}
}

type ruleSpec struct {
	action    ir.Action
	effect    ir.Effect
	condition *ir.Condition
	fields    []ir.FieldID
}

func newPolicy(t *testing.T, model ir.ModelID, specs ...ruleSpec) ir.Policy {
	t.Helper()
	rules := make([]ir.Rule, len(specs))
	for index, spec := range specs {
		var (
			rule ir.Rule
			err  error
		)
		if spec.fields == nil {
			rule, err = ir.NewModelRule(spec.action, spec.effect, model, spec.condition, uint32(index))
		} else {
			rule, err = ir.NewFieldRule(spec.action, spec.effect, model, spec.condition, spec.fields[0], spec.fields[1:], uint32(index))
		}
		if err != nil {
			t.Fatal(err)
		}
		rules[index] = rule
	}
	policy, err := ir.NewPolicy(model, rules)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func scalar(t *testing.T, model ir.ModelID, field ir.FieldID, value int64) ir.Condition {
	t.Helper()
	typ, err := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := ir.SignedValue(ir.ValueInt64, value)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := ir.OneOperand(signed)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewScalar(model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

type fakeRegistry struct {
	models map[golem.ModelID]struct{}
	fields map[golem.ModelID]map[golem.FieldID]struct{}
}

func (registry fakeRegistry) HasModel(model golem.ModelID) bool {
	_, ok := registry.models[model]
	return ok
}

func (registry fakeRegistry) HasField(model golem.ModelID, field golem.FieldID) bool {
	_, ok := registry.fields[model][field]
	return ok
}

type nilRegistry struct{}

func (*nilRegistry) HasModel(golem.ModelID) bool                { return false }
func (*nilRegistry) HasField(golem.ModelID, golem.FieldID) bool { return false }

func identities(model ir.ModelID, fields ...ir.FieldID) fakeRegistry {
	registry := fakeRegistry{
		models: map[golem.ModelID]struct{}{golem.ModelID(model): {}},
		fields: map[golem.ModelID]map[golem.FieldID]struct{}{golem.ModelID(model): {}},
	}
	for _, field := range fields {
		registry.fields[golem.ModelID(model)][golem.FieldID(field)] = struct{}{}
	}
	return registry
}

func oracleAccess(policy ir.Policy, requested, aField, bField ir.FieldID) Access {
	outcomes := make([]bool, 0, 4)
	for _, a := range []bool{false, true} {
		for _, b := range []bool{false, true} {
			outcomes = append(outcomes, oracleGrant(policy, requested, map[ir.FieldID]bool{aField: a, bField: b}))
		}
	}
	allGrant, anyGrant := true, false
	for _, granted := range outcomes {
		allGrant = allGrant && granted
		anyGrant = anyGrant || granted
	}
	if allGrant {
		return AccessAlways
	}
	if !anyGrant {
		return AccessNever
	}
	return AccessConditional
}

func oracleGrant(policy ir.Policy, requested ir.FieldID, matches map[ir.FieldID]bool) bool {
	rules := policy.Rules()
	for index := len(rules) - 1; index >= 0; index-- {
		rule := rules[index]
		if rule.Action() != ir.ActionRead {
			continue
		}
		fields, modelWide := rule.Fields()
		if !modelWide && !contains(fields, requested) {
			continue
		}
		condition, conditional := rule.Condition()
		if conditional {
			field, ok := condition.Field()
			if !ok || !matches[field] {
				continue
			}
		}
		return rule.Effect() == ir.EffectGrant
	}
	return false
}

func modelID(value byte) ir.ModelID { return ir.ModelID{value} }
func fieldID(value byte) ir.FieldID { return ir.FieldID{value} }
