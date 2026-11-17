package ir

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestFactPolicyRequiresExplicitObservationAuthority(t *testing.T) {
	model := testModel(1)
	truth, _ := NewConstant(model, true)
	definition, err := NewFactDefinition(model, "ready", truth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPolicyWithFacts(model, nil, []FactDefinition{definition}, FactScope{}); err == nil {
		t.Fatal("fact policy admitted an implicit observation audience")
	}
}

func TestResolvedFactCanonicalIdentityDoesNotExpandSharedBodies(t *testing.T) {
	model := testModel(1)
	body, _ := NewConstant(model, true)
	for index := 0; index < 8; index++ {
		id, _ := FactIdentity(model, fmt.Sprintf("shared_%d", index))
		fact, err := NewFact(model, id, body, unrestrictedFactScope(t, model))
		if err != nil {
			t.Fatal(err)
		}
		body, err = NewLogical(model, LogicalAnd, []Condition{fact, fact})
		if err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := CanonicalCondition(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 512 {
		t.Fatalf("shared fact bodies expanded into %d canonical bytes", len(encoded))
	}
}

func TestResolvedFactRefusesExcessiveExpandedWork(t *testing.T) {
	model := testModel(1)
	body, _ := NewConstant(model, true)
	for index := 0; index < 16; index++ {
		id, _ := FactIdentity(model, fmt.Sprintf("expansion_%d", index))
		fact, err := NewFact(model, id, body, unrestrictedFactScope(t, model))
		if err != nil {
			var limit *FactLimitError
			if !errors.As(err, &limit) || limit.Limit != FactExpansionNodes || limit.Maximum != MaxFactExpansionNodes {
				t.Fatalf("unexpected expansion refusal: %v", err)
			}
			return
		}
		body, _ = NewLogical(model, LogicalOr, []Condition{fact, fact})
	}
	t.Fatal("compact fact DAG admitted more than 65,000 expanded nodes")
}

func TestResolvedFactRefusesExcessiveFactNesting(t *testing.T) {
	model := testModel(1)
	body, _ := NewConstant(model, true)
	for index := 0; index < 257; index++ {
		id, _ := FactIdentity(model, fmt.Sprintf("nesting_%d", index))
		fact, err := NewFact(model, id, body, unrestrictedFactScope(t, model))
		if err != nil {
			var limit *FactLimitError
			if index != MaxFactDepth || !errors.As(err, &limit) || limit.Limit != FactDepth || limit.Maximum != MaxFactDepth {
				t.Fatalf("unexpected nesting refusal at %d: %v", index, err)
			}
			return
		}
		body = fact
	}
	t.Fatal("fact nesting exceeded the policy binding/proof depth ceiling")
}

func TestFactExpansionExactBoundaryAndCombinedConstraints(t *testing.T) {
	model := testModel(1)
	id, _ := FactIdentity(model, "boundary")
	truth, _ := NewConstant(model, true)
	children := make([]Condition, MaxFactExpansionNodes-2)
	for index := range children {
		children[index] = truth
	}
	body, _ := NewLogical(model, LogicalAnd, children)
	fact, err := NewFact(model, id, body, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckFactExpansion(fact); err != nil {
		t.Fatal(err)
	}
	body, _ = NewLogical(model, LogicalAnd, append(children, truth))
	if _, err := NewFact(model, id, body, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("one node beyond the exact fact expansion ceiling was admitted")
	}
	combined, _ := NewLogical(model, LogicalAnd, []Condition{fact, fact})
	var limit *FactLimitError
	if err := CheckFactExpansion(combined); !errors.As(err, &limit) || limit.Limit != FactExpansionNodes {
		t.Fatalf("combined facts bypassed the constraint expansion bound: %v", err)
	}
}

func TestPolicyRejectsFactReferencesWithoutDefinitions(t *testing.T) {
	model := testModel(1)
	id, err := FactIdentity(model, "in_libraries")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := NewFactReference(model, id)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := NewModelRule(ActionRead, EffectGrant, model, &reference, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPolicy(model, []Rule{rule}); err == nil {
		t.Fatal("policy admitted a fact reference with no definition")
	}
}

func TestPolicyRejectsRecursiveFactDefinitions(t *testing.T) {
	model := testModel(1)
	first, _ := FactIdentity(model, "first")
	second, _ := FactIdentity(model, "second")
	firstRef, _ := NewFactReference(model, first)
	secondRef, _ := NewFactReference(model, second)
	firstDef, _ := NewFactDefinition(model, "first", secondRef)
	secondDef, _ := NewFactDefinition(model, "second", firstRef)
	if _, err := NewPolicyWithFacts(model, nil, []FactDefinition{firstDef, secondDef}, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("policy admitted a recursive fact graph")
	}
}

func TestFactIdentityAndPolicyOwnership(t *testing.T) {
	model := testModel(1)
	id, err := FactIdentity(model, "in_libraries")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := FactIdentity(model, "in_libraries")
	otherRoot, _ := FactIdentity(testModel(2), "in_libraries")
	otherName, _ := FactIdentity(model, "in_other_libraries")
	if id == (FactID{}) || id != again || id == otherRoot || id == otherName {
		t.Fatal("fact identity did not bind its exact name and model root")
	}
	for _, name := range []string{"", " padded", "padded ", "bad\x00name", string([]byte{0xff})} {
		if _, err := FactIdentity(model, name); err == nil {
			t.Fatalf("invalid fact name accepted: %q", name)
		}
	}
	if _, err := FactIdentity(ModelID{}, "fact"); err == nil {
		t.Fatal("zero fact root accepted")
	}
	body, _ := NewConstant(model, true)
	definition, err := NewFactDefinition(model, "in_libraries", body)
	if err != nil {
		t.Fatal(err)
	}
	reference, _ := NewFactReference(model, id)
	rule, _ := NewModelRule(ActionRead, EffectGrant, model, &reference, 0)
	input := []FactDefinition{definition}
	policy, err := NewPolicyWithFacts(model, []Rule{rule}, input, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	input[0] = FactDefinition{}
	copy := policy.Facts()
	copy[0] = FactDefinition{}
	owned, present := policy.Fact(id)
	if !present || owned.ID() != id || owned.Name() != "in_libraries" || owned.ModelID() != model || owned.Body().ModelID() != model {
		t.Fatal("fact table aliased caller-owned storage")
	}
	foreignBody, _ := NewConstant(testModel(2), true)
	if _, err := NewFactDefinition(model, "foreign", foreignBody); err == nil {
		t.Fatal("foreign-root fact body accepted")
	}
	foreign, _ := NewFactDefinition(testModel(2), "foreign", foreignBody)
	if _, err := NewPolicyWithFacts(model, nil, []FactDefinition{foreign}, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("foreign-root definition admitted into policy")
	}
	if _, err := NewPolicyWithFacts(model, nil, []FactDefinition{definition, definition}, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("duplicate fact name accepted")
	}
}

func TestFactGraphChecksUnusedDefinitionsAndForwardReferences(t *testing.T) {
	model := testModel(1)
	leafID, _ := FactIdentity(model, "leaf")
	leafRef, _ := NewFactReference(model, leafID)
	parent, _ := NewFactDefinition(model, "parent", leafRef)
	truth, _ := NewConstant(model, true)
	leaf, _ := NewFactDefinition(model, "leaf", truth)
	first, err := NewPolicyWithFacts(model, nil, []FactDefinition{parent, leaf}, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPolicyWithFacts(model, nil, []FactDefinition{leaf, parent}, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	left, err := CanonicalPolicy(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalPolicy(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatal("fact declaration order changed policy identity")
	}
	if _, err := NewPolicyWithFacts(model, nil, []FactDefinition{parent}, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("undefined reference in unused fact accepted")
	}
	self, _ := NewFactDefinition(model, "leaf", leafRef)
	if _, err := NewPolicyWithFacts(model, nil, []FactDefinition{self}, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("self-recursive unused fact accepted")
	}
}

func TestFactBoundaryAndActorBoundBodiesRemainInCanonicalIdentity(t *testing.T) {
	model := testModel(1)
	truth, _ := NewConstant(model, true)
	falsehood, _ := NewConstant(model, false)
	first, _ := NewFactDefinition(model, "membership", truth)
	second, _ := NewFactDefinition(model, "membership", falsehood)
	if first.ID() != second.ID() {
		t.Fatal("actor-bound body changed the declared fact identity")
	}
	policyOne, _ := NewPolicyWithFacts(model, nil, []FactDefinition{first}, unrestrictedFactScope(t, model))
	policyTwo, _ := NewPolicyWithFacts(model, nil, []FactDefinition{second}, unrestrictedFactScope(t, model))
	encodedOne, err := CanonicalPolicy(policyOne)
	if err != nil {
		t.Fatal(err)
	}
	encodedTwo, err := CanonicalPolicy(policyTwo)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encodedOne, encodedTwo) {
		t.Fatal("different actor-bound fact bodies shared a policy identity")
	}
	resolved, err := NewFact(model, first.ID(), truth, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	resolvedBytes, err := CanonicalCondition(resolved)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryBytes, _ := CanonicalCondition(truth)
	if bytes.Equal(resolvedBytes, ordinaryBytes) {
		t.Fatal("fact provenance vanished from canonical condition identity")
	}
	reference, _ := NewFactReference(model, first.ID())
	if _, err := NewFact(model, first.ID(), reference, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("resolved fact retained unresolved references")
	}
	rule, _ := NewModelRule(ActionRead, EffectGrant, model, &resolved, 0)
	if _, err := NewPolicyWithFacts(model, []Rule{rule}, []FactDefinition{first}, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("resolved fact bypassed authored-reference validation")
	}
	foreignRef, _ := NewFactReference(testModel(2), first.ID())
	if _, err := NewFact(model, first.ID(), foreignRef, unrestrictedFactScope(t, model)); err == nil {
		t.Fatal("foreign-root reference accepted in resolved fact")
	}
}

func TestFactEncodingPreservesOrdinaryPoliciesAndDoesNotExpandSharedDefinitions(t *testing.T) {
	model := testModel(1)
	truth, _ := NewConstant(model, true)
	rule, _ := NewModelRule(ActionRead, EffectGrant, model, &truth, 0)
	ordinary, err := NewPolicy(model, []Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := CanonicalPolicy(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	// Build the established ordinary policy encoding independently. Fact-free
	// policies must keep their prior bytes and therefore their fingerprints.
	var established canonicalEncoder
	established.raw([]byte("golem:policy:v1\x00"))
	established.id16(model)
	established.count(1)
	established.rule(rule)
	if !bytes.Equal(encoded, established.buffer) {
		t.Fatal("ordinary policy canonical format changed")
	}
	facts := make([]FactDefinition, 1024)
	facts[0], _ = NewFactDefinition(model, "fact_0", truth)
	for i := 1; i < len(facts); i++ {
		ref, _ := NewFactReference(model, facts[i-1].ID())
		shared, err := NewLogical(model, LogicalAnd, []Condition{ref, ref})
		if err != nil {
			t.Fatal(err)
		}
		facts[i], err = NewFactDefinition(model, fmt.Sprintf("fact_%d", i), shared)
		if err != nil {
			t.Fatal(err)
		}
	}
	policy, err := NewPolicyWithFacts(model, nil, facts, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = CanonicalPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > len(facts)*256 {
		t.Fatalf("shared fact bodies were expanded: bytes=%d definitions=%d", len(encoded), len(facts))
	}
}
