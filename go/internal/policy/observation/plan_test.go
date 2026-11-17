package observation

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func TestRelatedLocalFactObservesItsScopeIndependentlyOfDataTruth(t *testing.T) {
	root, target := ir.ModelID{1}, ir.ModelID{2}
	local := boolean(t, target, ir.FieldID{2})
	id := must(ir.FactIdentity(target, "local_membership"))
	scope := must(ir.NewFactScope(boolean(t, target, ir.FieldID{5})))
	fact := must(ir.NewFact(target, id, local, scope))
	context := boolean(t, target, ir.FieldID{3})
	child := must(logical(target, ir.LogicalAnd, []ir.Condition{context, fact}))
	relation := testRelation(t, root, target, ir.FieldID{4}, ir.RelationID{1}, ir.OperatorRelationSome, &child)
	plan := must(New(relation, nil))
	paths := plan.Paths()
	if len(paths) != 1 || paths[0].FactID() != id || paths[0].TargetModel() != target {
		t.Fatal("local fact on a related model lost its observation path")
	}
	if ir.HasFacts(paths[0].Terminal()) {
		t.Fatal("related local witness retained fact provenance")
	}
	if must(ir.ConditionFingerprint(paths[0].Terminal())) != must(ir.ConditionFingerprint(scope.Condition())) {
		t.Fatal("owning-row observation inferred its audience from data truth")
	}
	for _, watched := range []ir.FieldID{{2}, {5}} {
		if !slices.Contains(paths[0].Dependencies(), watched) {
			t.Fatalf("owning-row observer lost a scope or fact dependency: %x", watched)
		}
	}
	for _, operation := range []ir.OperatorID{ir.OperatorRelationNone, ir.OperatorRelationEvery} {
		negative := testRelation(t, root, target, ir.FieldID{4}, ir.RelationID{1}, operation, &child)
		negativePlan := must(New(negative, nil))
		if must(ir.ConditionFingerprint(negativePlan.Paths()[0].Terminal())) != must(ir.ConditionFingerprint(scope.Condition())) {
			t.Fatalf("negative quantifier %d inverted observation authority", operation)
		}
	}
	paths[0] = Path{}
	if plan.Paths()[0].FactID() != id {
		t.Fatal("path accessor exposed mutable storage")
	}
	hops := plan.Paths()[0].Hops()
	hops[0] = Hop{}
	if plan.Paths()[0].Hops()[0].Relation().ModelID() != root {
		t.Fatal("path hop accessor exposed mutable storage")
	}
}

func TestPhysicalSelfRelationIsAnObservedTarget(t *testing.T) {
	root := ir.ModelID{1}
	child := boolean(t, root, ir.FieldID{2})
	body := testRelation(t, root, root, ir.FieldID{3}, ir.RelationID{1}, ir.OperatorRelationSome, &child)
	id := must(ir.FactIdentity(root, "related_member"))
	plan := must(New(must(ir.NewFact(root, id, body, unrestrictedFactScope(t, root))), nil))
	if paths := plan.Paths(); len(paths) != 1 || paths[0].TargetModel() != root {
		t.Fatal("same-model dependency was confused with the changed source row")
	}
}

func TestRelatedAuthorityNeedsEvidenceEvenForLocalDataFacts(t *testing.T) {
	root, target := ir.ModelID{1}, ir.ModelID{2}
	truth := must(ir.NewConstant(root, true))
	child := must(ir.NewConstant(target, true))
	related := testRelation(t, root, target, ir.FieldID{3}, ir.RelationID{1}, ir.OperatorRelationSome, &child)
	scope := must(ir.NewFactScope(related))
	id := must(ir.FactIdentity(root, "local"))
	data := must(ir.NewFact(root, id, truth, scope))
	var failure *ir.FactScopeEvidenceError
	if _, err := New(data, nil); !errors.As(err, &failure) {
		t.Fatalf("an unobservable related domain was accepted because its data fact was local: %v", err)
	}
}

func TestNestedSelfFactDoesNotRebindTheOwningActorScope(t *testing.T) {
	root := ir.ModelID{1}
	scope := must(ir.NewFactScope(boolean(t, root, ir.FieldID{5})))
	active := boolean(t, root, ir.FieldID{2})
	children := testRelation(t, root, root, ir.FieldID{3}, ir.RelationID{1}, ir.OperatorRelationSome, &active)
	inner := must(ir.NewFact(root, must(ir.FactIdentity(root, "ready")), children, scope))
	friend := testRelation(t, root, root, ir.FieldID{4}, ir.RelationID{2}, ir.OperatorRelationSome, &inner)
	outer := must(ir.NewFact(root, must(ir.FactIdentity(root, "friend_ready")), friend, scope))
	plan := must(New(outer, nil))
	var deep []Hop
	for _, path := range plan.Paths() {
		if len(path.Hops()) == 2 {
			deep = path.Hops()
		}
	}
	if len(deep) != 2 {
		t.Fatal("nested physical path was omitted")
	}
	if must(ir.ConditionFingerprint(deep[0].Guard())) != must(ir.ConditionFingerprint(scope.Condition())) {
		t.Fatal("source actor scope was lost")
	}
	if truth, constant := deep[1].Guard().Constant(); !constant || !truth {
		t.Fatal("actor scope was rebound to the related user and suppresses source recovery")
	}
}

func TestObservationRefusesUnresolvedMasksAndBoundsPathExpansion(t *testing.T) {
	root, target := ir.ModelID{1}, ir.ModelID{2}
	truth := must(ir.NewConstant(root, true))
	id := must(ir.FactIdentity(root, "unresolved"))
	reference := must(ir.NewFactReference(root, id))
	if _, err := New(truth, nil, reference); err == nil {
		t.Fatal("unresolved reference in a selected mask was silently omitted")
	}
	child := must(ir.NewConstant(target, true))
	var facts []ir.Condition
	for index := 0; index <= MaxPaths; index++ {
		id := must(ir.FactIdentity(root, fmt.Sprintf("path_%d", index)))
		body := testRelation(t, root, target, ir.FieldID{3}, ir.RelationID{1}, ir.OperatorRelationSome, &child)
		facts = append(facts, must(ir.NewFact(root, id, body, unrestrictedFactScope(t, root))))
	}
	if plan := must(New(must(logical(root, ir.LogicalAnd, facts[:MaxPaths])), nil)); len(plan.Paths()) != MaxPaths {
		t.Fatal("exact observation path boundary was rejected")
	}
	var limit *PathLimitError
	if _, err := New(must(logical(root, ir.LogicalAnd, facts)), nil); !errors.As(err, &limit) {
		t.Fatalf("unbounded observation paths admitted: %v", err)
	}
}

func testRelation(t *testing.T, root, target ir.ModelID, field ir.FieldID, relation ir.RelationID, operation ir.OperatorID, child *ir.Condition) ir.Condition {
	t.Helper()
	requirements := must(operator.ValidateShape(operation, operator.Shape{Node: ir.ConditionRelation, Cardinality: ir.RelationToMany, HasChild: child != nil, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()}))
	return must(ir.NewRelation(root, field, relation, target, ir.RelationToMany, operation, child, requirements))
}
