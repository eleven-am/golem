package golem

import (
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/pagecontract"
)

func pageFixtureResolver()      {}
func otherPageFixtureResolver() {}

func TestPageQueryRegistrationCannotGrantWithin(t *testing.T) {
	bindings, err := GeneratedApplicationBindings[struct{}](SchemaDigest{1})
	if err != nil {
		t.Fatal(err)
	}
	pages, err := GeneratedApplicationPageQueries(bindings, GeneratedPageQueryOperation("page", pageFixtureResolver))
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := pages.PageQuery(pageFixtureResolver); !ok || id != "page" {
		t.Fatalf("registered page is unavailable: %s %t", id, ok)
	}
	if _, ok := pages.Operation(pageFixtureResolver); ok {
		t.Fatal("page acquired custom mutation identity")
	}
	if _, ok := pages.TxOperation(pageFixtureResolver); ok {
		t.Fatal("page acquired transaction operation identity")
	}
	if _, ok := pages.PageQuery(otherPageFixtureResolver); ok {
		t.Fatal("unregistered resolver acquired page identity")
	}
	ids := pages.PageQueryInventory()
	ids[0] = "changed"
	if pages.PageQueryInventory()[0] != "page" {
		t.Fatal("page inventory leaked mutable metadata")
	}
	if _, err := GeneratedApplicationOperations(pages, GeneratedCustomMutationOperation("mutation", pageFixtureResolver)); err == nil {
		t.Fatal("page was subsequently registered as a mutation")
	}
	mutations, err := GeneratedApplicationOperations(bindings, GeneratedCustomMutationOperation("mutation", pageFixtureResolver))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GeneratedApplicationPageQueries(mutations, GeneratedPageQueryOperation("page", pageFixtureResolver)); err == nil {
		t.Fatal("mutation was subsequently registered as a page")
	}
}

func TestPageRegistrationRequiresUniqueStampedResolvers(t *testing.T) {
	bindings, err := GeneratedApplicationBindings[struct{}](SchemaDigest{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, queries := range [][]GeneratedPageQuery{
		{GeneratedPageQueryOperation("", pageFixtureResolver)},
		{GeneratedPageQueryOperation[func()]("page", nil)},
		{GeneratedPageQueryOperation("same", pageFixtureResolver), GeneratedPageQueryOperation("same", otherPageFixtureResolver)},
		{GeneratedPageQueryOperation("first", pageFixtureResolver), GeneratedPageQueryOperation("second", pageFixtureResolver)},
	} {
		if _, err := GeneratedApplicationPageQueries(bindings, queries...); err == nil {
			t.Fatal("invalid page registration admitted")
		}
	}
	if _, err := GeneratedApplicationPageQueries(ApplicationBindings[struct{}]{}, GeneratedPageQueryOperation("page", pageFixtureResolver)); err == nil {
		t.Fatal("unstamped page registration admitted")
	}
}

func TestPageRequestAndResultsOwnTraversalSlices(t *testing.T) {
	position := []byte("snapshot/25")
	expires := time.Unix(1800000000, 0)
	state := RuntimePageStateValue(25, 100, 25, position, expires)
	request := RuntimePageRequestValue("arguments", state)
	position[0] = 'x'
	got := request.State()
	got[0] = 'y'
	if string(request.State()) != "snapshot/25" || request.Take() != 25 || request.CandidateLimit() != 100 || request.Consumed() != 25 || !request.Expires().Equal(expires) || request.Arguments() != "arguments" {
		t.Fatal("request does not own its admitted traversal state")
	}
	items, next := []string{"one"}, []byte("snapshot/50")
	batch := PageItems(items, 25, next)
	items[0], next[0] = "changed", 'z'
	first, consumed, checkpoint, more := RuntimePageBatchParts(batch)
	if first[0] != "one" || string(checkpoint) != "snapshot/50" || consumed != 25 || !more {
		t.Fatal("batch aliases supplied traversal state")
	}
	first[0], checkpoint[0] = "other", 'a'
	second, _, otherCheckpoint, _ := RuntimePageBatchParts(batch)
	if second[0] != "one" || string(otherCheckpoint) != "snapshot/50" {
		t.Fatal("batch leaked its stored traversal state")
	}
	_, _, _, terminal := RuntimePageBatchParts(PageItems[string](nil, 0, nil))
	_, _, _, invalidEmpty := RuntimePageBatchParts(PageItems[string](nil, 0, []byte{}))
	if terminal || !invalidEmpty {
		t.Fatal("nil terminal and empty nonterminal checkpoints were conflated")
	}
	rawItems := []any{"one"}
	page := RuntimePageResultValue(rawItems, func() (string, error) { return "opaque-token", nil })
	rawItems[0] = "changed before reading"
	values := page.Items()
	values[0] = "changed"
	if page.Items()[0] != "one" {
		t.Fatal("runtime page result leaked its items")
	}
	if next, err := page.Finalize(); err != nil || next != "opaque-token" {
		t.Fatal("runtime page result lost its finalizer")
	}
	if _, err := (RuntimePageResult{}).Finalize(); err == nil {
		t.Fatal("unowned result finalized")
	}
}

func TestOversizedPageBatchKeepsBoundedInvalidWitnesses(t *testing.T) {
	items := make([]int32, pagecontract.MaxItems+128)
	checkpoint := make([]byte, pagecontract.MaxCheckpointBytes+128)
	items[0], checkpoint[0] = 7, 9
	batch := PageItems(items, 1, checkpoint)
	items[0], checkpoint[0] = 1, 2
	owned, _, state, more := RuntimePageBatchParts(batch)
	if len(owned) <= pagecontract.MaxItems || len(owned) > pagecontract.MaxItems+1 || len(state) <= pagecontract.MaxCheckpointBytes || len(state) > pagecontract.MaxCheckpointBytes+1 || !more {
		t.Fatal("oversized batch was fully copied or converted into a valid-looking page")
	}
	if owned[0] != 7 || state[0] != 9 {
		t.Fatal("invalid batch retained mutable caller storage")
	}
}
