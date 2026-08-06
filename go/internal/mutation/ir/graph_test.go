package ir

import (
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestGraphAssignsStableDepthFirstOrdinals(t *testing.T) {
	rootModel, childModel, leafModel := testModel(1), testModel(2), testModel(3)
	rootTarget, leafTarget := target(t, rootModel, 1), target(t, leafModel, 3)
	rootSelection := selection(t, rootModel, policyir.ActionUpdate)
	leafSelection := selection(t, leafModel, policyir.ActionDelete)
	rootPost, childPost := constant(t, rootModel), constant(t, childModel)
	childPosition, _ := NewRelationPosition(RelationPositionInput{ParentModel: rootModel, Field: testField(10), Relation: testRelation(1), TargetModel: childModel, Kind: PositionEndpoint})
	leafPosition, _ := NewRelationPosition(RelationPositionInput{ParentModel: childModel, Field: testField(11), Relation: testRelation(2), TargetModel: leafModel, Kind: PositionRelatedTarget, Target: &leafTarget})

	graph, err := NewGraph(NodeInput{
		Operation: Update, Model: rootModel, Target: &rootTarget, Selection: &rootSelection, RowPostcondition: &rootPost, Identity: IdentityUnchanged,
		Children: []NodeInput{{
			Operation: Create, Model: childModel, Relation: testRelation(1), RelationPosition: &childPosition, RowPostcondition: &childPost, Identity: IdentityProduced,
			Children: []NodeInput{{Operation: Delete, Model: leafModel, Relation: testRelation(2), RelationPosition: &leafPosition, Selection: &leafSelection, Identity: IdentityUnchanged}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes := graph.Nodes()
	if len(nodes) != 3 || graph.MaxDepth() != 2 {
		t.Fatalf("unexpected graph size/depth: %d/%d", len(nodes), graph.MaxDepth())
	}
	for index, node := range nodes {
		if node.Ordinal() != uint32(index) {
			t.Fatalf("node %d has ordinal %d", index, node.Ordinal())
		}
	}
	if parent, ok := nodes[2].ParentOrdinal(); !ok || parent != 1 {
		t.Fatalf("leaf parent = %d/%v", parent, ok)
	}
	children := nodes[0].ChildOrdinals()
	children[0] = 99
	if graph.Nodes()[0].ChildOrdinals()[0] != 1 {
		t.Fatal("graph accessor leaked child ordinals")
	}
}

func TestGraphRequiresTruthfulUpsertBranches(t *testing.T) {
	model := testModel(1)
	valueTarget := target(t, model, 1)
	selectUpdate := selection(t, model, policyir.ActionUpdate)
	post := constant(t, model)
	create := NodeInput{Operation: Create, Model: model, Branch: UpsertCreateBranch, RowPostcondition: &post, Identity: IdentityProduced}
	update := NodeInput{Operation: Update, Model: model, Branch: UpsertUpdateBranch, Target: &valueTarget, Selection: &selectUpdate, RowPostcondition: &post, Identity: IdentityUnchanged}
	if _, err := NewGraph(NodeInput{Operation: Upsert, Model: model, Target: &valueTarget, Selection: &selectUpdate, Identity: IdentityUnchanged, Children: []NodeInput{create, update}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGraph(NodeInput{Operation: Upsert, Model: model, Target: &valueTarget, Selection: &selectUpdate, Identity: IdentityUnchanged, Children: []NodeInput{create}}); err == nil {
		t.Fatal("upsert missing update branch accepted")
	}
	bad := update
	bad.Operation = Delete
	if _, err := NewGraph(NodeInput{Operation: Upsert, Model: model, Target: &valueTarget, Selection: &selectUpdate, Identity: IdentityUnchanged, Children: []NodeInput{create, bad}}); err == nil {
		t.Fatal("upsert delete branch accepted")
	}
}

func TestDependencyRequirementsValidateAndCloneRelationPaths(t *testing.T) {
	root, targetModel := testModel(1), testModel(2)
	hop, err := NewRelationHop(root, testField(1), testRelation(1), targetModel)
	if err != nil {
		t.Fatal(err)
	}
	path := []RelationHop{hop}
	dependency, err := NewDependency(root, path, testField(2))
	if err != nil {
		t.Fatal(err)
	}
	image, err := NewImageRequirements(root, []policyir.FieldID{testField(3)}, []Dependency{dependency})
	if err != nil {
		t.Fatal(err)
	}
	path[0] = RelationHop{}
	got := image.Dependencies()
	if got[0].Path()[0].TargetModelID() != targetModel {
		t.Fatal("dependency path was not detached")
	}
	gotPath := got[0].Path()
	gotPath[0] = RelationHop{}
	if image.Dependencies()[0].Path()[0].TargetModelID() != targetModel {
		t.Fatal("dependency accessor leaked storage")
	}

	broken, _ := NewRelationHop(testModel(9), testField(1), testRelation(1), targetModel)
	if _, err := NewDependency(root, []RelationHop{broken}, testField(2)); err == nil {
		t.Fatal("discontinuous dependency path accepted")
	}
}

func TestCreateManyIsExpandedIntoActualCreateNodes(t *testing.T) {
	rootModel, childModel := testModel(1), testModel(2)
	rootTarget := target(t, rootModel, 1)
	rootSelection := selection(t, rootModel, policyir.ActionUpdate)
	rootPost, childPost := constant(t, rootModel), constant(t, childModel)
	position, _ := NewRelationPosition(RelationPositionInput{ParentModel: rootModel, Field: testField(10), Relation: testRelation(1), TargetModel: childModel, Kind: PositionEndpoint})
	item := func() NodeInput {
		return NodeInput{Operation: Create, Model: childModel, Branch: BatchItemBranch, RowPostcondition: &childPost, Identity: IdentityProduced}
	}
	graph, err := NewGraph(NodeInput{
		Operation: Update, Model: rootModel, Target: &rootTarget, Selection: &rootSelection, RowPostcondition: &rootPost, Identity: IdentityUnchanged,
		Children: []NodeInput{{Operation: CreateMany, Model: childModel, Relation: testRelation(1), RelationPosition: &position, Identity: IdentityProduced, Children: []NodeInput{item(), item()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := graph.Nodes(); len(got) != 4 || got[1].Operation() != CreateMany || got[2].Branch() != BatchItemBranch || got[3].Branch() != BatchItemBranch {
		t.Fatalf("unexpected create-many expansion: %#v", got)
	}
	bad := NodeInput{Operation: CreateMany, Model: childModel, Relation: testRelation(1), RelationPosition: &position, Identity: IdentityProduced}
	if _, err := NewGraph(NodeInput{Operation: Update, Model: rootModel, Target: &rootTarget, Selection: &rootSelection, RowPostcondition: &rootPost, Identity: IdentityUnchanged, Children: []NodeInput{bad}}); err == nil {
		t.Fatal("opaque create-many node accepted")
	}
}
