package dependency

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestCollectKeepsLocalRequirementsFlatAndMergesRelationTrees(t *testing.T) {
	root, user, organization := modelID(1), modelID(2), modelID(3)
	title, author := fieldID(10), fieldID(11)
	name, phone, org := fieldID(20), fieldID(21), fieldID(22)
	suspended := fieldID(30)

	first := and(t, root,
		scalar(t, root, title),
		relation(t, root, author, user, and(t, user,
			scalar(t, user, phone),
			relation(t, user, org, organization, scalar(t, organization, suspended)),
		)),
	)
	second := relation(t, root, author, user, scalar(t, user, name))

	plan, err := Collect(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ModelID() != root {
		t.Fatalf("model = %x", plan.ModelID())
	}
	requires := plan.Requires()
	if len(requires) != 2 || requires[0] != title || requires[1] != author {
		t.Fatalf("requires = %x", requires)
	}
	entries := plan.Dependencies().Entries()
	if len(entries) != 2 || entries[0].FieldID() != title || entries[0].Kind() != Scalar || entries[1].FieldID() != author || entries[1].Kind() != Relation {
		t.Fatalf("root dependencies = %#v", entries)
	}
	target, ok := entries[1].TargetModel()
	if !ok || target != user {
		t.Fatalf("relation target = %x/%v", target, ok)
	}
	children := entries[1].Children().Entries()
	if len(children) != 3 || children[0].FieldID() != phone || children[1].FieldID() != org || children[2].FieldID() != name {
		t.Fatalf("merged child order = %#v", children)
	}
	grandchildren := children[1].Children().Entries()
	if len(grandchildren) != 1 || grandchildren[0].FieldID() != suspended {
		t.Fatalf("grandchildren = %#v", grandchildren)
	}
}

func TestCollectDeduplicatesFirstSeenAndCopyIsolatesAccessors(t *testing.T) {
	model, field := modelID(1), fieldID(2)
	condition := and(t, model, scalar(t, model, field), scalar(t, model, field))
	plan, err := Collect(condition)
	if err != nil {
		t.Fatal(err)
	}
	requires := plan.Requires()
	entries := plan.Dependencies().Entries()
	if len(requires) != 1 || len(entries) != 1 {
		t.Fatalf("requires/dependencies = %x/%#v", requires, entries)
	}
	requires[0] = fieldID(99)
	entries[0] = Entry{}
	if plan.Requires()[0] != field || plan.Dependencies().Entries()[0].FieldID() != field {
		t.Fatal("dependency accessors leaked mutable storage")
	}
}

func TestCollectRejectsEmptyAndCrossModelInputs(t *testing.T) {
	if _, err := Collect(); err == nil {
		t.Fatal("empty condition inventory accepted")
	}
	if _, err := Collect(scalar(t, modelID(1), fieldID(1)), scalar(t, modelID(2), fieldID(2))); err == nil {
		t.Fatal("cross-model condition inventory accepted")
	}
}

func scalar(t *testing.T, model ir.ModelID, field ir.FieldID) ir.Condition {
	t.Helper()
	typ, err := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := ir.SignedValue(ir.ValueInt64, 1)
	operand, _ := ir.OneOperand(value)
	condition, err := ir.NewScalar(model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func relation(t *testing.T, model ir.ModelID, field ir.FieldID, target ir.ModelID, child ir.Condition) ir.Condition {
	t.Helper()
	relation, err := ir.NewRelation(model, field, relationID(int(field[15])+100), target, ir.RelationToOne, ir.OperatorRelationIs, &child, nil)
	if err != nil {
		t.Fatal(err)
	}
	return relation
}

func and(t *testing.T, model ir.ModelID, conditions ...ir.Condition) ir.Condition {
	t.Helper()
	condition, err := ir.NewLogical(model, ir.LogicalAnd, conditions)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func modelID(value int) (result ir.ModelID)       { result[15] = byte(value); return }
func fieldID(value int) (result ir.FieldID)       { result[15] = byte(value); return }
func relationID(value int) (result ir.RelationID) { result[15] = byte(value); return }
