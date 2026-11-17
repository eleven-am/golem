package dependency

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestReadableDependenciesDoNotBorrowPhysicalFactAuthority(t *testing.T) {
	root, target := modelID(1), modelID(2)
	visible, hidden := fieldID(1), fieldID(2)
	physical := relation(t, root, hidden, target, scalar(t, target, fieldID(3)))
	id, _ := ir.FactIdentity(root, "membership")
	fact, err := ir.NewFact(root, id, physical, unrestrictedFactScope(t, root))
	if err != nil {
		t.Fatal(err)
	}
	condition := and(t, root, scalar(t, root, visible), fact)
	readable, err := CollectReadable(condition)
	if err != nil {
		t.Fatal(err)
	}
	if entries := readable.Dependencies().Entries(); len(entries) != 1 || entries[0].FieldID() != visible {
		t.Fatal("physical fact entered a target-readable event dependency tree")
	}
	all, err := Collect(condition)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Dependencies().Entries()) != 2 {
		t.Fatal("physical hydration or mutation dependencies were discarded")
	}
	outer := relation(t, root, visible, target, mustTargetFact(t, target, fieldID(4)))
	readable, err = CollectReadable(outer)
	if err != nil {
		t.Fatal(err)
	}
	if entries := readable.Dependencies().Entries(); len(entries) != 1 || len(entries[0].Children().Entries()) != 0 {
		t.Fatal("readable traversal lost its boundary or borrowed a nested fact body")
	}
}

func mustTargetFact(t *testing.T, model ir.ModelID, field ir.FieldID) ir.Condition {
	t.Helper()
	id, _ := ir.FactIdentity(model, "local")
	condition, err := ir.NewFact(model, id, scalar(t, model, field), unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	return condition
}
