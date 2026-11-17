package normalize

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestFactNormalizationPreservesDefinitionsReferencesAndBoundary(t *testing.T) {
	model := ir.ModelID{1}
	truth, _ := ir.NewConstant(model, true)
	not, _ := ir.NewLogical(model, ir.LogicalNot, []ir.Condition{truth})
	doubleNot, _ := ir.NewLogical(model, ir.LogicalNot, []ir.Condition{not})
	definition, err := ir.NewFactDefinition(model, "visible", doubleNot)
	if err != nil {
		t.Fatal(err)
	}
	reference, _ := ir.NewFactReference(model, definition.ID())
	rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, model, &reference, 0)
	policy, err := ir.NewPolicyWithFacts(model, []ir.Rule{rule}, []ir.FactDefinition{definition}, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	policy = policy.WithRowVersion(ir.FieldID{2})
	normalized, err := Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	fact, present := normalized.Fact(definition.ID())
	if !present {
		t.Fatal("normalization dropped fact definitions")
	}
	if value, constant := fact.Body().Constant(); !constant || !value {
		t.Fatal("fact body was not normalized")
	}
	condition, _ := normalized.RuleAt(0).Condition()
	if id, isReference := condition.FactReference(); !isReference || id != definition.ID() {
		t.Fatal("normalization expanded or dropped an authored fact reference")
	}
	if version, owned := normalized.RowVersion(); !owned || version != (ir.FieldID{2}) {
		t.Fatal("normalization dropped row-version ownership")
	}
	resolved, err := ir.NewFact(model, definition.ID(), doubleNot, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	condition, err = Condition(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if id, body, ok := condition.Fact(); !ok || id != definition.ID() {
		t.Fatal("normalization dropped resolved fact provenance")
	} else if value, constant := body.Constant(); !constant || !value {
		t.Fatal("resolved fact body was not normalized")
	}
	before, err := ir.CanonicalPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ir.CanonicalPolicy(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("test fixture did not exercise a normalization change")
	}
	original, _ := policy.Fact(definition.ID())
	if _, constant := original.Body().Constant(); constant {
		t.Fatal("normalization mutated the original fact body")
	}
}
