package resolve

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestFactResolutionBindsReferencesAndRetainsNamedBoundaries(t *testing.T) {
	body := scalarBoolCondition(t, fieldA, true)
	leaf, _ := ir.NewFactDefinition(testModel, "owned", body)
	leafRef, _ := ir.NewFactReference(testModel, leaf.ID())
	parent, _ := ir.NewFactDefinition(testModel, "visible", leafRef)
	parentRef, _ := ir.NewFactReference(testModel, parent.ID())
	rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, &parentRef, 0)
	policy, err := ir.NewPolicyWithFacts(testModel, []ir.Rule{rule}, []ir.FactDefinition{parent, leaf}, unrestrictedFactScope(t, testModel))
	if err != nil {
		t.Fatal(err)
	}
	policy = policy.WithRowVersion(fieldC)
	before, _ := ir.CanonicalPolicy(policy)
	for _, question := range []struct {
		name string
		read func() (ir.Condition, error)
	}{
		{"row", func() (ir.Condition, error) { return RowConstraint(policy, ir.ActionRead, testModel) }},
		{"field", func() (ir.Condition, error) { return FieldCondition(policy, ir.ActionRead, testModel, fieldA) }},
		{"version", func() (ir.Condition, error) { return FieldCondition(policy, ir.ActionRead, testModel, fieldC) }},
	} {
		t.Run(question.name, func(t *testing.T) {
			resolved, err := question.read()
			if err != nil {
				t.Fatal(err)
			}
			id, nested, ok := resolved.Fact()
			if !ok || id != parent.ID() {
				t.Fatalf("resolved rule lost its named fact boundary: kind=%d", resolved.Kind())
			}
			id, value, ok := nested.Fact()
			if !ok || id != leaf.ID() {
				t.Fatal("nested definition was not resolved at the same root")
			}
			assertSameCondition(t, value, body)
		})
	}
	after, _ := ir.CanonicalPolicy(policy)
	if !bytes.Equal(before, after) {
		t.Fatal("resolution mutated the authored policy or fact table")
	}
}

func TestFactResolutionPreservesRulePriorityAndFieldScope(t *testing.T) {
	owned, _ := ir.NewFactDefinition(testModel, "owned", scalarBoolCondition(t, fieldA, true))
	suspended, _ := ir.NewFactDefinition(testModel, "suspended", scalarBoolCondition(t, fieldB, true))
	ownedRef, _ := ir.NewFactReference(testModel, owned.ID())
	suspendedRef, _ := ir.NewFactReference(testModel, suspended.ID())
	for _, open := range []bool{false, true} {
		for _, fieldDeny := range []bool{false, true} {
			t.Run(fmt.Sprintf("open=%v/field-deny=%v", open, fieldDeny), func(t *testing.T) {
				grant := &ownedRef
				if open {
					grant = nil
				}
				rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, grant, 0)
				deny, err := ir.NewModelRule(ir.ActionRead, ir.EffectDeny, testModel, &suspendedRef, 1)
				if fieldDeny {
					deny, err = ir.NewFieldRule(ir.ActionRead, ir.EffectDeny, testModel, &suspendedRef, fieldA, nil, 1)
				}
				if err != nil {
					t.Fatal(err)
				}
				policy, err := ir.NewPolicyWithFacts(testModel, []ir.Rule{rule, deny}, []ir.FactDefinition{owned, suspended}, unrestrictedFactScope(t, testModel))
				if err != nil {
					t.Fatal(err)
				}
				row := mustRowConstraint(t, policy, ir.ActionRead, testModel)
				field := mustFieldCondition(t, policy, ir.ActionRead, testModel, fieldA)
				for _, owner := range []bool{false, true} {
					for _, blocked := range []bool{false, true} {
						values := testRow{fieldA: owner, fieldB: blocked}
						wantRow := (open || owner) && (fieldDeny || !blocked)
						wantField := (open || owner) && !blocked
						if got := evaluateResolvedFact(t, row, values); got != wantRow {
							t.Fatalf("owner=%v blocked=%v row=%v want=%v", owner, blocked, got, wantRow)
						}
						if got := evaluateResolvedFact(t, field, values); got != wantField {
							t.Fatalf("owner=%v blocked=%v field=%v want=%v", owner, blocked, got, wantField)
						}
					}
				}
			})
		}
	}
}

func TestFactResolutionKeepsPhysicalAndReadableRelationUsesDistinct(t *testing.T) {
	target := modelID(2)
	child, _ := ir.NewConstant(target, true)
	physical, err := ir.NewRelation(testModel, fieldA, ir.RelationID{1}, target, ir.RelationToMany, ir.OperatorRelationSome, &child, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := ir.NewFactDefinition(testModel, "has_copy", physical)
	reference, _ := ir.NewFactReference(testModel, definition.ID())
	mixed, _ := ir.NewLogical(testModel, ir.LogicalAnd, []ir.Condition{physical, reference})
	rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, &mixed, 0)
	policy, err := ir.NewPolicyWithFacts(testModel, []ir.Rule{rule}, []ir.FactDefinition{definition}, unrestrictedFactScope(t, testModel))
	if err != nil {
		t.Fatal(err)
	}
	resolved := mustRowConstraint(t, policy, ir.ActionRead, testModel)
	logical, children, ok := resolved.Logical()
	if !ok || logical != ir.LogicalAnd || len(children) != 2 {
		t.Fatal("fact and readable relation use were incorrectly merged")
	}
	var ordinary, facts int
	for _, condition := range children {
		if condition.Kind() == ir.ConditionRelation {
			ordinary++
		}
		if id, body, ok := condition.Fact(); ok {
			facts++
			if id != definition.ID() {
				t.Fatal("fact identity changed")
			}
			assertSameCondition(t, body, physical)
		}
	}
	if ordinary != 1 || facts != 1 {
		t.Fatalf("ordinary=%d facts=%d", ordinary, facts)
	}
}

func TestFactResolutionDoesNotReuseAnotherActorBody(t *testing.T) {
	var previous ir.Fingerprint
	for index, owned := range []bool{true, false, true} {
		definition, _ := ir.NewFactDefinition(testModel, "owned", scalarBoolCondition(t, fieldA, owned))
		reference, _ := ir.NewFactReference(testModel, definition.ID())
		rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, &reference, 0)
		policy, err := ir.NewPolicyWithFacts(testModel, []ir.Rule{rule}, []ir.FactDefinition{definition}, unrestrictedFactScope(t, testModel))
		if err != nil {
			t.Fatal(err)
		}
		resolved := mustRowConstraint(t, policy, ir.ActionRead, testModel)
		if evaluateResolvedFact(t, resolved, testRow{fieldA: true}) != owned {
			t.Fatal("resolved fact retained another actor's constraint")
		}
		fingerprint, err := ir.ConditionFingerprint(resolved)
		if err != nil {
			t.Fatal(err)
		}
		if index > 0 && fingerprint == previous {
			t.Fatal("actor-bound body was omitted from resolved identity")
		}
		previous = fingerprint
	}
}

func TestFactResolutionKeepsSharedDefinitionGraphsCompact(t *testing.T) {
	const count = 128
	body := scalarBoolCondition(t, fieldA, true)
	definitions := make([]ir.FactDefinition, 0, count)
	var reference ir.Condition
	for index := 0; index < count; index++ {
		definition, err := ir.NewFactDefinition(testModel, fmt.Sprintf("shared_%03d", index), body)
		if err != nil {
			t.Fatal(err)
		}
		definitions = append(definitions, definition)
		reference, _ = ir.NewFactReference(testModel, definition.ID())
		body, _ = ir.NewLogical(testModel, ir.LogicalAnd, []ir.Condition{reference, reference})
	}
	rule, _ := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, &reference, 0)
	policy, err := ir.NewPolicyWithFacts(testModel, []ir.Rule{rule}, definitions, unrestrictedFactScope(t, testModel))
	if err != nil {
		t.Fatal(err)
	}
	resolved := mustRowConstraint(t, policy, ir.ActionRead, testModel)
	for index := count - 1; index >= 0; index-- {
		id, body, ok := resolved.Fact()
		if !ok || id != definitions[index].ID() {
			t.Fatalf("unresolved definition at depth %d: kind=%d", count-index, resolved.Kind())
		}
		encoded, err := ir.CanonicalCondition(resolved)
		if err != nil || len(encoded) > 256 {
			t.Fatalf("expanded shared definition: bytes=%d err=%v", len(encoded), err)
		}
		resolved = body
	}
	assertSameCondition(t, resolved, scalarBoolCondition(t, fieldA, true))
}

// This independent rule-resolution oracle unwraps named boundaries, while
// production execution remains gated until SQL, writes and observation agree.
func evaluateResolvedFact(t *testing.T, condition ir.Condition, row testRow) bool {
	t.Helper()
	if _, body, ok := condition.Fact(); ok {
		return evaluateResolvedFact(t, body, row)
	}
	if logical, children, ok := condition.Logical(); ok {
		switch logical {
		case ir.LogicalNot:
			return !evaluateResolvedFact(t, children[0], row)
		case ir.LogicalAnd:
			for _, child := range children {
				if !evaluateResolvedFact(t, child, row) {
					return false
				}
			}
			return true
		case ir.LogicalOr:
			for _, child := range children {
				if evaluateResolvedFact(t, child, row) {
					return true
				}
			}
			return false
		}
	}
	return evaluate(t, condition, row)
}
