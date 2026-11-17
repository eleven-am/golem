package ir

import "testing"

// These low-level execution tests deliberately authorize the whole root domain.
// Actor isolation is covered separately with explicit restricted scopes.
func unrestrictedFactScope(t *testing.T, model ModelID) FactScope {
	t.Helper()
	truth, err := NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := NewFactScope(truth)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestFactScopeRejectsNegativeOrFactAuthorityAndBindsContractIdentity(t *testing.T) {
	model, target := testModel(1), testModel(2)
	truth, _ := NewConstant(model, true)
	definition, _ := NewFactDefinition(model, "ready", truth)
	reference, _ := NewFactReference(model, definition.ID())
	child, _ := NewConstant(target, true)
	related, _ := NewRelation(model, testField(1), testRelation(1), target, RelationToOne, OperatorRelationIs, &child, nil)
	negative, _ := NewLogical(model, LogicalNot, []Condition{related})
	for _, unsupported := range []Condition{{}, reference, negative} {
		if _, err := NewFactScope(unsupported); err == nil {
			t.Fatal("unverifiable observation authority was accepted")
		}
	}
	foreign, _ := NewFactScope(child)
	if _, err := NewPolicyWithFacts(model, nil, []FactDefinition{definition}, foreign); err == nil {
		t.Fatal("another model's scope authorized this policy")
	}
	var identities []Fingerprint
	var resolvedIdentities []Fingerprint
	for _, actor := range []int64{7, 8} {
		value, _ := SignedValue(ValueInt64, actor)
		condition, err := NewScalar(model, testField(2), mustType(t, ValueInt64, false), OperatorEqual, ComparisonSensitive, mustOperand(t, value), nil)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := NewFactScope(condition)
		if err != nil {
			t.Fatal(err)
		}
		policy, err := NewPolicyWithFacts(model, nil, []FactDefinition{definition}, scope)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := CanonicalPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, hashCanonical(policyFingerprintDomain, encoded))
		fact, err := NewFact(model, definition.ID(), truth, scope)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := ConditionFingerprint(fact)
		if err != nil {
			t.Fatal(err)
		}
		resolvedIdentities = append(resolvedIdentities, identity)
		if _, err := NewFact(model, definition.ID(), fact, unrestrictedFactScope(t, model)); err == nil {
			t.Fatal("nested definition borrowed a different actor's domain")
		}
	}
	if identities[0] == identities[1] || resolvedIdentities[0] == resolvedIdentities[1] {
		t.Fatal("actor scope values were omitted from authored or resolved identity")
	}
}

func TestFactScopeAcceptsPositiveCorrelatedAuthority(t *testing.T) {
	root, target := testModel(1), testModel(2)
	child, _ := NewConstant(target, true)
	for _, path := range []struct {
		cardinality RelationCardinality
		operation   OperatorID
	}{
		{RelationToOne, OperatorRelationIs},
		{RelationToMany, OperatorRelationSome},
	} {
		relation, err := NewRelation(root, testField(1), testRelation(1), target, path.cardinality, path.operation, &child, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewFactScope(relation); err != nil {
			t.Fatalf("positive physical authority path was rejected: %v", err)
		}
	}
}

func TestFactScopeWitnessAllowsOnlyBoundRelatedAuthority(t *testing.T) {
	model, target := testModel(1), testModel(2)
	child, err := NewConstant(target, true)
	if err != nil {
		t.Fatal(err)
	}
	relation, err := NewRelation(model, testField(1), testRelation(1), target, RelationToMany, OperatorRelationSome, &child, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := NewFactScope(relation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFactScopeWitness(plain); err == nil {
		t.Fatal("unbound related scope became an observation witness")
	}
	binding := ScopeBinding{TemplateID: [16]byte{1}, Template: Fingerprint{2}, Arguments: Fingerprint{3}}
	bound, err := NewBoundFactScope(relation, binding)
	if err != nil {
		t.Fatal(err)
	}
	witness, err := NewFactScopeWitness(bound)
	if err != nil {
		t.Fatal(err)
	}
	_, body, present := witness.Fact()
	if !present {
		t.Fatal("scope witness lost physical provenance")
	}
	if value, constant := body.Constant(); !constant || !value {
		t.Fatal("scope witness imported a data predicate")
	}
	scope, present := witness.FactScope()
	if !present || !scope.UsesRelations() {
		t.Fatal("scope witness erased related authority")
	}
	if got, ok := scope.Binding(); !ok || got != binding {
		t.Fatal("scope witness erased bound template provenance")
	}
	if got, err := ConditionFingerprint(scope.Condition()); err != nil {
		t.Fatal(err)
	} else if want, _ := ConditionFingerprint(relation); got != want {
		t.Fatal("scope witness rewrote authority")
	}
}
