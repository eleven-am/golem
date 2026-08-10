package main

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/mutationverify"
)

func TestP7MutationDefaultSelectionIncludesEveryCatalogEntry(t *testing.T) {
	catalog := []mutationverify.Mutation{{Label: "A"}, {Label: "B"}}
	selected, err := selectMutations(catalog, "")
	if err != nil || len(selected) != len(catalog) {
		t.Fatalf("selection=%#v err=%v", selected, err)
	}
}

func TestP7MutationSelectionRejectsUnknownLabels(t *testing.T) {
	if _, err := selectMutations([]mutationverify.Mutation{{Label: "A"}}, "MISSING"); err == nil {
		t.Fatal("unknown mutation was accepted")
	}
}
