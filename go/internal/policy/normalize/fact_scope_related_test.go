package normalize

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestRelatedFactScopeNormalizationPreservesAuthorityAndActorIdentity(t *testing.T) {
	root, target, leaf := modelID(1), modelID(2), modelID(3)
	var previousPolicy []byte
	var previousGate, previousFact ir.Fingerprint
	for _, actor := range []int64{7, 8} {
		actorCondition := scalar(t, leaf, fieldID(30), actor, nil)
		leafCondition := logical(t, leaf, ir.LogicalOr, constant(t, leaf, false), actorCondition, actorCondition)
		inner, err := ir.NewRelation(target, fieldID(20), relationID(20), leaf, ir.RelationToOne, ir.OperatorRelationIs, &leafCondition, nil)
		if err != nil {
			t.Fatal(err)
		}
		child := logical(t, target, ir.LogicalAnd, constant(t, target, true), inner, inner)
		outer, err := ir.NewRelation(root, fieldID(10), relationID(10), target, ir.RelationToMany, ir.OperatorRelationSome, &child, nil)
		if err != nil {
			t.Fatal(err)
		}
		scopeCondition := logical(t, root, ir.LogicalAnd, constant(t, root, true), outer, outer)
		scope, err := ir.NewFactScope(scopeCondition)
		if err != nil {
			t.Fatal(err)
		}
		original := canonical(t, scopeCondition)
		definition, err := ir.NewFactDefinition(root, "unused", constant(t, root, true))
		if err != nil {
			t.Fatal(err)
		}
		rule, err := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, root, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		policy, err := ir.NewPolicyWithFacts(root, []ir.Rule{rule}, []ir.FactDefinition{definition}, scope)
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := Policy(policy)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Policy(normalized)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := ir.CanonicalPolicy(normalized)
		if err != nil {
			t.Fatal(err)
		}
		encodedAgain, err := ir.CanonicalPolicy(again)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, encodedAgain) || !normalized.FactScope().UsesRelations() {
			t.Fatal("policy normalization is not idempotent or lost related authority")
		}
		if bytes.Equal(original, canonical(t, normalized.FactScope().Condition())) {
			t.Fatal("fixture did not exercise related scope normalization")
		}
		if !bytes.Equal(original, canonical(t, policy.FactScope().Condition())) {
			t.Fatal("normalization mutated the original policy scope")
		}

		// Normalize the actual physical authority wrapper, including its body,
		// rather than testing only the standalone scope metadata.
		gate, err := ir.NewFactScopeCondition(scope)
		if err != nil {
			t.Fatal(err)
		}
		gate = normalize(t, gate)
		if !gate.IsFactScope() || !bytes.Equal(canonical(t, gate), canonical(t, normalize(t, gate))) {
			t.Fatal("normalization lost the physical authority boundary or its stable identity")
		}
		_, body, _ := gate.Fact()
		if !bytes.Equal(canonical(t, body), canonical(t, normalized.FactScope().Condition())) {
			t.Fatal("physical gate and normalized policy disagree on authority")
		}
		_, _, _, cardinality, nested, present := body.Relation()
		operation, _ := body.Operator()
		if !present || cardinality != ir.RelationToMany || operation != ir.OperatorRelationSome || nested == nil {
			t.Fatal("normalization lost the outer positive Some witness")
		}
		_, _, _, cardinality, terminal, present := nested.Relation()
		operation, _ = nested.Operator()
		if !present || cardinality != ir.RelationToOne || operation != ir.OperatorRelationIs || terminal == nil || !bytes.Equal(canonical(t, *terminal), canonical(t, actorCondition)) {
			t.Fatal("normalization changed the correlated Is witness or actor value")
		}
		gateScope, present := gate.FactScope()
		if !present || !gateScope.UsesRelations() || !bytes.Equal(canonical(t, gateScope.Condition()), canonical(t, body)) {
			t.Fatal("authority wrapper lost its normalized scope provenance")
		}
		fact, err := ir.NewFact(root, definition.ID(), definition.Body(), normalized.FactScope())
		if err != nil {
			t.Fatal(err)
		}
		gateIdentity, err := ir.ConditionFingerprint(gate)
		if err != nil {
			t.Fatal(err)
		}
		factIdentity, err := ir.ConditionFingerprint(fact)
		if err != nil {
			t.Fatal(err)
		}
		if previousPolicy != nil && (bytes.Equal(previousPolicy, encoded) || previousGate == gateIdentity || previousFact == factIdentity) {
			t.Fatal("normalization omitted actor authority from policy, gate or fact identity")
		}
		previousPolicy, previousGate, previousFact = encoded, gateIdentity, factIdentity
	}
}
