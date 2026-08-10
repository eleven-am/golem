package ir

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestRelationPositionRepresentsSelectorFreeToOneAndEmptySet(t *testing.T) {
	parent, child, field, relation := positionID(1), positionID(2), positionID(3), positionID(4)
	one, _ := NewExpansionRequirement(ExpandCurrentToOne, 1)
	current, err := NewRelationPosition(RelationPositionInput{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionCurrentToOne, Expansion: &one})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := current.Target(); ok {
		t.Fatal("selector-free current-to-one became an explicit target")
	}
	difference, _ := NewExpansionRequirement(ExpandSetDifference, 9)
	empty, err := NewRelationPosition(RelationPositionInput{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionSetDifference, Expansion: &difference})
	if err != nil || len(empty.DesiredTargets()) != 0 {
		t.Fatalf("empty set difference=%#v err=%v", empty, err)
	}
	graph, err := NewGraph(NodeInput{Operation: Create, Model: parent, Identity: IdentityProduced, Children: []NodeInput{{Operation: Update, Model: child, Relation: relation, RelationPosition: &current, Identity: IdentityUnchanged}}})
	if err != nil || len(graph.Nodes()) != 2 {
		t.Fatalf("selector-free nested update graph: %v", err)
	}
}

func TestRelationPositionRejectsForgedEndpointsAndIllegalShapes(t *testing.T) {
	parent, child, field, relation := positionID(1), positionID(2), positionID(3), positionID(4)
	expansion, _ := NewExpansionRequirement(ExpandRelatedPredicate, 2)
	truth, _ := policyir.NewConstant(child, true)
	selector := positionTarget(t, child, 8)
	tests := []RelationPositionInput{
		{ParentModel: parent, Field: policyir.FieldID{}, Relation: relation, TargetModel: child, Kind: PositionEndpoint},
		{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionCurrentToOne},
		{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionRelatedTarget, Predicate: &truth},
		{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionRelatedPredicate, Target: &selector, Predicate: &truth, Expansion: &expansion},
	}
	for index, input := range tests {
		if _, err := NewRelationPosition(input); err == nil {
			t.Fatalf("illegal position %d accepted", index)
		}
	}
	endpoint, _ := NewRelationPosition(RelationPositionInput{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionEndpoint})
	if _, err := NewGraph(NodeInput{Operation: Create, Model: parent, Identity: IdentityProduced, Children: []NodeInput{{Operation: Delete, Model: child, Relation: relation, RelationPosition: &endpoint, Identity: IdentityUnchanged}}}); err == nil {
		t.Fatal("delete accepted an endpoint-only position")
	}
}

func TestRelationPositionCanonicalizesDesiredTargetsDeterministically(t *testing.T) {
	parent, child, field, relation := positionID(1), positionID(2), positionID(3), positionID(4)
	a, b := positionTarget(t, child, 8), positionTarget(t, child, 9)
	expansion, _ := NewExpansionRequirement(ExpandSetDifference, 5)
	left, _ := NewRelationPosition(RelationPositionInput{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionSetDifference, Desired: []Target{b, a}, Expansion: &expansion})
	right, _ := NewRelationPosition(RelationPositionInput{ParentModel: parent, Field: field, Relation: relation, TargetModel: child, Kind: PositionSetDifference, Desired: []Target{a, b}, Expansion: &expansion})
	leftPlan := positionPlan(t, parent, child, relation, left)
	rightPlan := positionPlan(t, parent, child, relation, right)
	leftBytes, _ := CanonicalPlan(leftPlan)
	rightBytes, _ := CanonicalPlan(rightPlan)
	if !bytes.Equal(leftBytes, rightBytes) {
		t.Fatal("set target permutation changed canonical plan")
	}
	fingerprintA, _ := PlanFingerprint(leftPlan)
	fingerprintB, _ := PlanFingerprint(rightPlan)
	if fingerprintA != fingerprintB {
		t.Fatal("set target permutation changed fingerprint")
	}
}

func positionPlan(t *testing.T, parent, child policyir.ModelID, relation policyir.RelationID, position RelationPosition) Plan {
	t.Helper()
	graph, err := NewGraph(NodeInput{Operation: Create, Model: parent, Identity: IdentityProduced, Children: []NodeInput{{Operation: SetRelation, Model: child, Relation: relation, RelationPosition: &position, Identity: IdentityUnchanged}}})
	if err != nil {
		t.Fatal(err)
	}
	image, _ := NewImageRequirements(parent, nil, nil)
	providers, _ := policyir.NewProviderSet(policyir.ProviderSQLite, policyir.ProviderPostgreSQL)
	requirement, _ := NewProviderRequirement(providers, CapabilityTransaction)
	bounds, _ := NewStatementBounds(99, 9)
	result, err := NewPlan(PlanInput{Stance: System, Graph: graph, Result: image, Providers: []ProviderRequirement{requirement}, Retry: NoRetry, Bounds: bounds})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func positionTarget(t *testing.T, model policyir.ModelID, last byte) Target {
	t.Helper()
	key := golem.KeyID(positionID(last + 10))
	field, secondField := policyir.FieldID(positionID(last+20)), policyir.FieldID(positionID(last+30))
	var uuid [16]byte
	uuid[15] = last
	value, _ := NewSelectorValue(field, policyir.UUIDValue(uuid))
	text, _ := policyir.StringValue(string([]byte{'a' + last%20}))
	second, _ := NewSelectorValue(secondField, text)
	target, err := NewTarget(model, key, []SelectorValue{value, second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func positionID(last byte) [16]byte { var result [16]byte; result[15] = last; return result }
