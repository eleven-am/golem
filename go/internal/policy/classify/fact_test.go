package classify

import (
	"slices"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestFactScopeRemainsAFieldDependencyWithUnconditionalRules(t *testing.T) {
	model, field, owner := modelID(1), fieldID(1), fieldID(2)
	truth, _ := ir.NewConstant(model, true)
	definition, _ := ir.NewFactDefinition(model, "unused", truth)
	scope, err := ir.NewFactScope(scalar(t, model, owner, 7))
	if err != nil {
		t.Fatal(err)
	}
	rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, model, nil, 0)
	policy, err := ir.NewPolicyWithFacts(model, []ir.Rule{rule}, []ir.FactDefinition{definition}, scope)
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(identities(model, field, owner), policy, model, []ir.FieldID{field}, UseProjection, ir.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	classified, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := classified.Field(field)
	if !ok || result.Access() != AccessConditional || !slices.Contains(result.Requires(), owner) {
		t.Fatalf("classification lost its policy-domain dependency: %+v", result)
	}
}

func TestRequestPreservesPolicyFactDefinitionsAndRowVersion(t *testing.T) {
	model, version, owner := modelID(1), fieldID(1), fieldID(2)
	definition, _ := ir.NewFactDefinition(model, "owned", scalar(t, model, owner, 7))
	reference, _ := ir.NewFactReference(model, definition.ID())
	rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, model, &reference, 0)
	policy, err := ir.NewPolicyWithFacts(model, []ir.Rule{rule}, []ir.FactDefinition{definition}, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	policy = policy.WithRowVersion(version)
	request, err := NewRequest(identities(model, version, owner), policy, model, []ir.FieldID{version}, UseProjection, ir.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	if retained, ok := request.policy.Fact(definition.ID()); !ok || retained.ID() != definition.ID() {
		t.Fatal("classification request discarded its policy's fact table")
	}
	if retained, ok := request.policy.RowVersion(); !ok || retained != version {
		t.Fatal("classification request discarded the owned row version")
	}
	for _, constraint := range []ir.Condition{request.ReadConstraint(), request.SelectingConstraint()} {
		if id, _, ok := constraint.Fact(); !ok || id != definition.ID() {
			t.Fatal("classification request did not resolve the policy's fact reference")
		}
	}
	classified, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	field, ok := classified.Field(version)
	if !ok || field.Access() != AccessConditional || !field.DischargedByConstraint() {
		t.Fatal("fact-backed row version classification lost its read-row guarantee")
	}
	if required := field.Requires(); len(required) != 1 || required[0] != owner {
		t.Fatalf("fact dependencies=%x, want owner", required)
	}
}
