package resolve

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// These low-level execution tests deliberately authorize the whole root domain.
// Actor isolation is covered separately with explicit restricted scopes.
func unrestrictedFactScope(t *testing.T, model ir.ModelID) ir.FactScope {
	t.Helper()
	truth, err := ir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ir.NewFactScope(truth)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestBoundLocalScopeKeepsTemplateIdentityWhenDefinitionsAreUnused(t *testing.T) {
	// Both actors have the same executable scope truth. Recovery still needs
	// to detect that the template inputs changed, including in an open policy.
	var previousRow, previousField ir.Fingerprint
	for _, argument := range []byte{7, 8} {
		truth, err := ir.NewConstant(testModel, true)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := ir.NewBoundFactScope(truth, ir.ScopeBinding{
			TemplateID: [16]byte{1}, Template: ir.Fingerprint{2}, Arguments: ir.Fingerprint{argument},
		})
		if err != nil {
			t.Fatal(err)
		}
		definition, err := ir.NewFactDefinition(testModel, "unused", truth)
		if err != nil {
			t.Fatal(err)
		}
		rule, err := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		policy, err := ir.NewPolicyWithFacts(testModel, []ir.Rule{rule}, []ir.FactDefinition{definition}, scope)
		if err != nil {
			t.Fatal(err)
		}
		row, err := RowConstraint(policy, ir.ActionRead, testModel)
		if err != nil {
			t.Fatal(err)
		}
		field, err := FieldCondition(policy, ir.ActionRead, testModel, fieldA)
		if err != nil {
			t.Fatal(err)
		}
		for _, gate := range []ir.Condition{row, field} {
			retained, present := gate.FactScope()
			binding, bound := retained.Binding()
			if !gate.IsFactScope() || !present || !bound || binding.Arguments != (ir.Fingerprint{argument}) {
				t.Fatal("normalized open policy lost its bound scope provenance")
			}
		}
		rowID, err := ir.ConditionFingerprint(row)
		if err != nil {
			t.Fatal(err)
		}
		fieldID, err := ir.ConditionFingerprint(field)
		if err != nil {
			t.Fatal(err)
		}
		if previousRow != (ir.Fingerprint{}) && (previousRow == rowID || previousField == fieldID) {
			t.Fatal("different scope inputs have indistinguishable effective policy identities")
		}
		previousRow, previousField = rowID, fieldID
	}
}

func TestFactScopeSharesFinalExpansionBudgetWithDataFacts(t *testing.T) {
	const count = ir.MaxFactExpansionNodes / 2
	wide := func(offset int) ir.Condition {
		children := make([]ir.Condition, count)
		for index := range children {
			var field ir.FieldID
			binary.BigEndian.PutUint32(field[12:], uint32(index+offset+1))
			children[index] = scalarBoolCondition(t, field, true)
		}
		condition, err := ir.NewLogical(testModel, ir.LogicalAnd, children)
		if err != nil {
			t.Fatal(err)
		}
		return condition
	}
	scope, err := ir.NewFactScope(wide(0))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := ir.NewFactDefinition(testModel, "wide", wide(count))
	if err != nil {
		t.Fatal(err)
	}
	reference, _ := ir.NewFactReference(testModel, definition.ID())
	rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, &reference, 0)
	policy, err := ir.NewPolicyWithFacts(testModel, []ir.Rule{rule}, []ir.FactDefinition{definition}, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []func() (ir.Condition, error){
		func() (ir.Condition, error) { return RowConstraint(policy, ir.ActionRead, testModel) },
		func() (ir.Condition, error) { return FieldCondition(policy, ir.ActionRead, testModel, fieldA) },
	} {
		_, err := question()
		var failure *ir.FactLimitError
		if !errors.As(err, &failure) || failure.Limit != ir.FactExpansionNodes {
			t.Fatalf("scope and data facts received independent full execution budgets: %v", err)
		}
	}
}

func TestFactScopeSurroundsNegativeRulesDenialsAndOpenBaseline(t *testing.T) {
	scope, err := ir.NewFactScope(scalarBoolCondition(t, fieldA, true))
	if err != nil {
		t.Fatal(err)
	}
	body := scalarBoolCondition(t, fieldB, true)
	definition, err := ir.NewFactDefinition(testModel, "ready", body)
	if err != nil {
		t.Fatal(err)
	}
	reference, _ := ir.NewFactReference(testModel, definition.ID())
	negative, _ := ir.NewLogical(testModel, ir.LogicalNot, []ir.Condition{reference})
	for _, action := range []ir.Action{ir.ActionRead, ir.ActionCreate, ir.ActionUpdate, ir.ActionDelete} {
		for _, fallback := range []bool{false, true} {
			var rules []ir.Rule
			if fallback {
				grant, _ := ir.NewModelRule(action, ir.EffectGrant, testModel, nil, 0)
				deny, _ := ir.NewModelRule(action, ir.EffectDeny, testModel, &reference, 1)
				rules = []ir.Rule{grant, deny}
			} else {
				grant, _ := ir.NewModelRule(action, ir.EffectGrant, testModel, &negative, 0)
				rules = []ir.Rule{grant}
			}
			policy, err := ir.NewPolicyWithFacts(testModel, rules, []ir.FactDefinition{definition}, scope)
			if err != nil {
				t.Fatal(err)
			}
			for _, owned := range []bool{false, true} {
				for _, ready := range []bool{false, true} {
					values := testRow{fieldA: owned, fieldB: ready}
					want := owned && !ready
					row := mustRowConstraint(t, policy, action, testModel)
					field := mustFieldCondition(t, policy, action, testModel, fieldB)
					if evaluateResolvedFact(t, row, values) != want || evaluateResolvedFact(t, field, values) != want {
						t.Fatalf("scope escaped action=%d fallback=%v owned=%v ready=%v", action, fallback, owned, ready)
					}
				}
			}
			// Raw expression resolution preserves Boolean meaning; the domain
			// belongs outside the complete ordered policy expression.
			raw, err := Condition(policy, negative)
			if err != nil || !evaluateResolvedFact(t, raw, testRow{fieldA: false, fieldB: false}) {
				t.Fatalf("raw reference resolution injected a scope under negation: %v", err)
			}
		}
	}
}
