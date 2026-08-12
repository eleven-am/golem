package main

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

func TestP8MutationCommandSelectionIsClosedAndCatalogOrdered(t *testing.T) {
	catalog := []p8mutation.Mutation{{Label: "FIRST"}, {Label: "SECOND"}}
	selected, err := selectMutations(catalog, " SECOND , FIRST ")
	if err != nil || len(selected) != 2 || selected[0].Label != "FIRST" || selected[1].Label != "SECOND" {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
	if _, err := selectMutations(catalog, "ABSENT"); err == nil || err.Error() != "P8_MUTATION_UNKNOWN_LABEL" {
		t.Fatalf("unknown-label error=%v", err)
	}
}
