package observation

import (
	"context"
	"errors"
	"testing"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func TestHistoricalCandidatesKeepIntermediateGuardAlternativesTogether(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	input := graph.Input()
	second := historicalRow(t, f.Registry, f.Comment,
		mutationdecode.Value(ir.FieldID(f.CommentID), ir.UUIDValue([16]byte{15: 32})),
		mutationdecode.Value(ir.FieldID(f.CommentPostID), ir.UUIDValue([16]byte{15: 11})),
		mutationdecode.Value(ir.FieldID(f.CommentBody), must(ir.StringValue("ready"))))
	input.Rows = append(input.Rows, history.PhaseRow{Phase: history.Before, Row: second})
	attached := false
	for index := range input.Extents {
		extent := &input.Extents[index]
		if extent.Phase != history.Before || extent.Field != ir.FieldID(f.PostComments) {
			continue
		}
		// Graph.New canonicalizes row order. Its exported references are not
		// the original fixture's insertion ordinals; locate Post11 by identity.
		parent := input.Rows[int(extent.Parent)-1].Row
		cell, present := parent.Cell(ir.FieldID(f.PostID))
		if !present {
			t.Fatal("PostComments extent has no Post primary key")
		}
		value, _ := cell.PolicyValue()
		id, _ := value.UUID()
		if id != ([16]byte{15: 11}) {
			continue
		}
		extent.Children = append(extent.Children, history.RowRef(len(input.Rows)))
		attached = true
	}
	if !attached {
		t.Fatal("missing complete before-phase Post11 Comments extent")
	}
	graph = must(history.New(f.Registry, ir.ProviderSQLite, input))
	// Both recorded chains have the same Post cut and exact User owner:
	// Post11 -> Comment31/32 -> Post11 -> User1. The two local conditions
	// belong to one Comment witness, never two independent current rows.
	owner := historicalOwnerCondition(t, fixture, false)
	postOwner := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	commentPost := historicalRelation(t, f.Registry, ir.ModelID(f.Comment), f.CommentPost, f.Commenting, ir.ModelID(f.Post), ir.RelationToOne, postOwner)
	commentPost = must(logical(commentPost.ModelID(), ir.LogicalAnd, []ir.Condition{
		historicalScalar(t, f.Registry, f.Comment, f.CommentBody, must(ir.StringValue("ready"))),
		historicalScalar(t, f.Registry, f.Comment, f.CommentPostID, ir.UUIDValue([16]byte{15: 11})),
		commentPost,
	}))
	postComments := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostComments, f.Commenting, ir.ModelID(f.Comment), ir.RelationToMany, commentPost)
	outer := historicalRelation(t, f.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(f.Post), ir.RelationToOne, postComments)
	plan := must(New(outer, nil))
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	change := HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity}
	candidates := must(plan.HistoricalCandidates(context.Background(), f.Registry, graph, change))
	if len(candidates) != 2 {
		t.Fatalf("two same-cut witness chains collapsed: candidates=%d", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.CutOffset() != 1 || len(candidate.Guards()) != 1 {
			t.Fatalf("wrong cut or intermediate guards: offset=%d guards=%d", candidate.CutOffset(), len(candidate.Guards()))
		}
		guards := candidate.Guards()
		guards[0] = ir.Condition{}
		if candidate.Guards()[0].ModelID() != ir.ModelID(f.Comment) {
			t.Fatal("guard accessor exposed candidate storage")
		}
	}
	record := func(id, post byte, body string) evaluate.Record {
		return must(evaluate.NewRecord(ir.ModelID(f.Comment),
			must(evaluate.ValueField(ir.FieldID(f.CommentID), ir.UUIDValue([16]byte{15: id}))),
			must(evaluate.ValueField(ir.FieldID(f.CommentPostID), ir.UUIDValue([16]byte{15: post}))),
			must(evaluate.ValueField(ir.FieldID(f.CommentBody), must(ir.StringValue(body)))),
		))
	}
	matchingCandidates := func(records ...evaluate.Record) int {
		matches := 0
		for _, candidate := range candidates {
			// These candidates each have one identity-bound conjunctive
			// question. Runtime additionally ANDs it with that candidate's prefix.
			for _, current := range records {
				if must(evaluate.Condition(candidate.Guards()[0], current, ir.PortableProviders())) {
					matches++
					break
				}
			}
		}
		return matches
	}
	if got := matchingCandidates(record(31, 12, "ready"), record(32, 11, "private")); got != 0 {
		t.Fatalf("complementary current successes combined across witnesses: %d", got)
	}
	if got := matchingCandidates(record(31, 11, "ready"), record(32, 11, "private")); got != 1 {
		t.Fatalf("one complete current witness must suffice: %d", got)
	}
	if got := matchingCandidates(record(99, 11, "ready")); got != 0 {
		t.Fatalf("an unrecorded current identity borrowed the guard: %d", got)
	}
	// The old broad suffix could let Comment31's historical guard certify
	// the chain through Comment32. Pinning each occurrence must exclude it.
	input.Rows[len(input.Rows)-1].Row = historicalRow(t, f.Registry, f.Comment,
		mutationdecode.Value(ir.FieldID(f.CommentID), ir.UUIDValue([16]byte{15: 32})),
		mutationdecode.Value(ir.FieldID(f.CommentPostID), ir.UUIDValue([16]byte{15: 11})),
		mutationdecode.Value(ir.FieldID(f.CommentBody), must(ir.StringValue("private"))))
	graph = must(history.New(f.Registry, ir.ProviderSQLite, input))
	remaining := must(plan.HistoricalCandidates(context.Background(), f.Registry, graph, change))
	if len(remaining) != 1 || !must(evaluate.Condition(remaining[0].Guards()[0], record(31, 11, "ready"), ir.PortableProviders())) || must(evaluate.Condition(remaining[0].Guards()[0], record(32, 11, "ready"), ir.PortableProviders())) {
		t.Fatal("historical guard truth was borrowed from another ordinary chain")
	}
}

func TestHistoricalIntermediateGuardRefusesHiddenFactOwner(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	owner := historicalOwnerCondition(t, fixture, false)
	plan := must(New(owner, nil))
	matcher := historicalMatcher{ctx: context.Background(), registry: f.Registry, resolver: policysql.SchemaResolver(f.Registry), providers: ir.PortableProviders()}
	// Even a fact-free scalar question beneath an ordinary relation cannot
	// quietly enter a policy whose declared fact scope was projected away.
	guard := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne,
		historicalScalar(t, f.Registry, f.User, f.UserName, must(ir.StringValue("owner"))))
	err := matcher.currentGuardBoundary(plan, plan.paths[0], guard)
	var failure *HistoricalCoverageError
	if !errors.As(err, &failure) {
		t.Fatalf("hidden fact-owning boundary accepted: %v", err)
	}
}

func TestHistoricalCandidatesRefuseHiddenFactOwnerInCurrentOrdinaryGuards(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	change := HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity}
	owner := historicalOwnerCondition(t, fixture, false)
	selected := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	// This sibling contains no Fact node, but User is known to own the selected
	// policy fact. Rejoining its current Name would cross the authority boundary
	// independently of the exact historical owner and its separate scope fence.
	sibling := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne,
		historicalScalar(t, f.Registry, f.User, f.UserName, must(ir.StringValue("owner"))))
	if ir.HasFacts(sibling) {
		t.Fatal("regression requires a fact-free sibling guard")
	}
	watch := func(child ir.Condition) ir.Condition {
		return historicalRelation(t, f.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(f.Post), ir.RelationToOne, child)
	}
	guardedPost := must(logical(selected.ModelID(), ir.LogicalAnd, []ir.Condition{selected, sibling}))
	for _, test := range []struct {
		name     string
		question ir.Condition
	}{
		{"cut", watch(guardedPost)},
		{"root-cut", guardedPost},
		{"prefix-hop", must(logical(ir.ModelID(fixture.watch), ir.LogicalAnd, []ir.Condition{watch(selected), watch(sibling)}))},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := must(New(test.question, nil))
			if !plan.factModels[ir.ModelID(f.User)] {
				t.Fatal("selected policy lost its fact-owning model metadata")
			}
			candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, change)
			var failure *HistoricalCoverageError
			if !errors.As(err, &failure) || len(candidates) != 0 || failure.Detail != "current intermediate guard enters a fact-owning policy" {
				t.Fatalf("hidden owner was not refused at the current guard boundary: candidates=%d err=%v", len(candidates), err)
			}
		})
	}
	// A local scalar ordinary guard stays supported, and the bare terminal
	// owner identity may still be composed with its separately evaluated scope.
	for _, question := range []ir.Condition{
		owner,
		watch(selected),
		watch(must(logical(selected.ModelID(), ir.LogicalAnd, []ir.Condition{selected,
			historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("video")))}))),
	} {
		plan := must(New(question, nil))
		candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, change)
		if err != nil || len(candidates) == 0 {
			t.Fatalf("ordinary scalar or stable owner route was refused: candidates=%d err=%v", len(candidates), err)
		}
	}
}
