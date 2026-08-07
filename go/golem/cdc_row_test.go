package golem

import "testing"

func TestRuntimeCDCModelRowProvenanceIsExplicitAndInventoryIsOwned(t *testing.T) {
	model, first, second := ModelID{1}, FieldID{2}, FieldID{3}
	ordinary, err := RuntimeModelReadRow(model,
		RuntimePresentReadCell(second, "second", nil),
		RuntimeNullReadCell(first),
	)
	if err != nil {
		t.Fatal(err)
	}
	if fields, exact := RuntimeCDCModelRowInventory(ordinary); exact || fields != nil {
		t.Fatal("ordinary row acquired CDC exact-image provenance")
	}
	exact, err := RuntimeCDCModelRow(model,
		RuntimePresentReadCell(second, "second", nil),
		RuntimeNullReadCell(first),
	)
	if err != nil {
		t.Fatal(err)
	}
	fields, ok := RuntimeCDCModelRowInventory(exact)
	if !ok || len(fields) != 2 || fields[0] != first || fields[1] != second {
		t.Fatalf("exact inventory=%x ok=%t", fields, ok)
	}
	fields[0] = FieldID{99}
	again, ok := RuntimeCDCModelRowInventory(exact)
	if !ok || again[0] != first {
		t.Fatal("CDC inventory returned aliased storage")
	}
	cloned := cloneRuntimeModelRow(exact)
	if _, ok := RuntimeCDCModelRowInventory(cloned); !ok {
		t.Fatal("owned row clone lost exact CDC provenance")
	}
}
