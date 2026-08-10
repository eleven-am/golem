package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestOrderNestedFactsMissingAppliedFactIsInvariantAndRollsBack(t *testing.T) {
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	fixture := newGraphMutationFixture(t, schema, golem.ModelID{})
	author := golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 202}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "fact-invariant-author"),
	)
	post := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
		golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 201}),
		golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "fact-invariant-post"),
		golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, author),
	)
	frozen, err := golem.RuntimeFreezeCreateInput(post)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mutationir.NewImageRequirements(policyir.ModelID(schema.Post), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = SystemTransaction(ctx, fixture.app.System(), func(outer *SystemTx[graphMutationPrincipal, graphMutationActor]) error {
		graph, graphErr := prepareNestedGraph(fixture.app, nil, mutationir.System, mutationir.Create, &frozen, nil, result)
		if graphErr != nil {
			return graphErr
		}
		boundary := &systemNestedBoundary[graphMutationPrincipal, graphMutationActor]{
			app: fixture.app, source: outer.system.executor, graph: graph, stance: mutationir.System,
		}
		receipt, executeErr := mutationnested.Execute(ctx, graph, uint32(fixture.app.mutationLimits.touchedRows), boundary)
		if executeErr != nil {
			return executeErr
		}
		state, stateErr := outer.system.executor.mutationState()
		if stateErr != nil {
			return stateErr
		}
		state.mu.Lock()
		if len(state.facts) != 2 {
			state.mu.Unlock()
			t.Fatalf("buffered facts=%d want=2", len(state.facts))
		}
		// Focused failpoint: simulate a bug that lost one graph-owned fact after
		// applying both rows but before the outer transaction flush.
		state.facts = state.facts[:1]
		state.mu.Unlock()

		ordering := &systemNestedTransaction[graphMutationPrincipal, graphMutationActor]{
			app: fixture.app, binding: outer.system.executor,
			graphFactOrder: &beforeParentFactCheckpoint{start: 0, ordinal: 0},
		}
		return ordering.orderNestedFacts(receipt.Applied())
	})
	if err == nil || !strings.Contains(err.Error(), "P4_RUNTIME_NESTED_FACT") || !strings.Contains(err.Error(), "has no buffered fact") {
		t.Fatalf("missing graph fact error=%v", err)
	}
	assertGraphMutationRowsAndFacts(t, fixture, 0, 0, 0, 0)
}

func TestRepeatedIdenticalFactCandidatesAreEquivalentOrRefused(t *testing.T) {
	schema := schematest.NewSubscribedGraph(t)
	model := policyir.ModelID(schema.Post)
	identityFields := []policyir.FieldID{policyir.FieldID(schema.PostID)}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactUpdated, identityFields, identityFields, nil)
	if err != nil {
		t.Fatal(err)
	}
	causation := mutationfact.CausationID{1}
	beforeFirst := factOrderPostRow(t, schema, 61, "first-before")
	afterFirst := factOrderPostRow(t, schema, 61, "first-after")
	beforeSecond := factOrderPostRow(t, schema, 61, "second-before")
	afterSecond := factOrderPostRow(t, schema, 61, "second-after")
	first, err := mutationfact.New(schema.Registry, mutationfact.EventID{1}, requirement, causation, 1, &beforeFirst, &afterFirst)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mutationfact.New(schema.Registry, mutationfact.EventID{2}, requirement, causation, 2, &beforeSecond, &afterSecond)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := mutationdecode.ExtractOrderedIdentity(schema.Registry, afterFirst, identityFields)
	if err != nil {
		t.Fatal(err)
	}
	index, matched, err := nextNestedFactIndex([]mutationfact.Envelope{first, second}, make([]bool, 2), model, mutationir.FactUpdated, expected)
	if err != nil || !matched || index != 0 {
		t.Fatalf("equivalent repeated candidates index=%d matched=%t err=%v", index, matched, err)
	}
	if !nestedFactOrderingEquivalent(first, second) {
		t.Fatal("V1 update facts with the same exact identity retained hidden non-identity row semantics")
	}

	// Matching only by the after identity would also admit these two envelopes,
	// but their before identities differ. The checked equivalence boundary must
	// refuse that ambiguity rather than assigning either event to a graph node.
	beforeDifferent := factOrderPostRow(t, schema, 62, "different-before")
	afterShared := factOrderPostRow(t, schema, 63, "shared-after")
	left, err := mutationfact.New(schema.Registry, mutationfact.EventID{3}, requirement, causation, 3, &beforeFirst, &afterShared)
	if err != nil {
		t.Fatal(err)
	}
	right, err := mutationfact.New(schema.Registry, mutationfact.EventID{4}, requirement, causation, 4, &beforeDifferent, &afterShared)
	if err != nil {
		t.Fatal(err)
	}
	sharedIdentity, err := mutationdecode.ExtractOrderedIdentity(schema.Registry, afterShared, identityFields)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := nextNestedFactIndex([]mutationfact.Envelope{left, right}, make([]bool, 2), model, mutationir.FactUpdated, sharedIdentity); err == nil || !strings.Contains(err.Error(), "ambiguous non-equivalent") {
		t.Fatalf("non-equivalent repeated candidates error=%v", err)
	}
}

func factOrderPostRow(t testing.TB, schema schematest.GraphFixture, id byte, title string) mutationdecode.Row {
	t.Helper()
	text, err := policyir.StringValue(title)
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewRow(schema.Registry, policyir.ModelID(schema.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(schema.PostID), policyir.UUIDValue([16]byte{15: id})),
		mutationdecode.Value(policyir.FieldID(schema.AuthorID), policyir.UUIDValue([16]byte{15: 1})),
		mutationdecode.Value(policyir.FieldID(schema.PostTitle), text),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}
