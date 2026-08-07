package main

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/mutationverify"
)

func TestDefaultSelectionCannotHideUncoveredMutations(t *testing.T) {
	catalog := []mutationverify.Mutation{
		{Label: "COVERED", Patches: []mutationverify.Patch{{Path: "x", Before: "a", After: "b"}}, Tests: []mutationverify.Test{{Package: ".", Name: "TestX"}}},
		{Label: "REMAINING", Remaining: "not encoded"},
	}
	selected, err := selectMutations(catalog, "", false)
	if err != nil || len(selected) != 2 || selected[1].Label != "REMAINING" {
		t.Fatalf("default selection hid uncovered mutation: %#v err=%v", selected, err)
	}
	development, err := selectMutations(catalog, "", true)
	if err != nil || len(development) != 1 || development[0].Label != "COVERED" {
		t.Fatalf("allow-uncovered selection=%#v err=%v", development, err)
	}
}
