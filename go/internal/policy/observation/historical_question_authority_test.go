package observation

import (
	"context"
	"testing"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// CallerReach returns row authority separately from expanded filter/mask
// questions. A target policy inside either question cannot replace the source's
// own authority, even when its exact historical owner has a true scope.
func TestHistoricalIndependentQuestionsRetainSourceAuthority(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	owner := historicalOwnerCondition(t, fixture, false)
	ownerID := historicalScalar(t, f.Registry, f.User, f.UserID, ir.UUIDValue([16]byte{15: 1}))
	filteredOwner := must(logical(ir.ModelID(f.User), ir.LogicalAnd, []ir.Condition{owner, ownerID}))
	question := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, filteredOwner)
	ordinary := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	localGuard := historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("video")))
	for _, kind := range []string{"filter", "mask"} {
		for _, allowed := range []bool{false, true} {
			name := kind + "/denied"
			if allowed {
				name = kind + "/allowed"
			}
			t.Run(name, func(t *testing.T) {
				actor := byte(2)
				if allowed {
					actor = 1
				}
				localScope := historicalScalar(t, f.Registry, f.Post, f.AuthorID, ir.UUIDValue([16]byte{15: actor}))
				scope := must(ir.NewFactScope(localScope))
				// This scalar-only source fact is false in the graph. It supplies
				// no physical Comment path; its data truth is opaque relevance,
				// while its local scope remains exact actor authority.
				body := historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("never-ready")))
				fact := must(ir.NewFact(ir.ModelID(f.Post), must(ir.FactIdentity(ir.ModelID(f.Post), "source_domain")), body, scope))
				row := must(logical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{fact, localGuard, ordinary}))
				var plan Plan
				if kind == "filter" {
					plan = must(NewHistorical(row, &question))
				} else {
					plan = must(NewHistorical(row, nil, question))
				}
				candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
				if err != nil {
					t.Fatal(err)
				}
				if !allowed {
					if len(candidates) != 0 {
						t.Fatalf("%s question escaped the denied source scope: %d candidates", kind, len(candidates))
					}
					return
				}
				if len(candidates) == 0 {
					t.Fatal("false source data truth suppressed authorized dependency recovery")
				}
				for _, candidate := range candidates {
					cutKey := must(history.RowKey(f.Registry, ir.ProviderSQLite, candidate.CutIdentity()))
					found := false
					for _, additional := range candidate.AdditionalScopes() {
						if additional.Identity().ModelID() == ir.ModelID(f.Post) && additional.Key() == cutKey {
							found = must(ir.FactScopeFingerprint(additional.Scope())) == must(ir.FactScopeFingerprint(scope))
						}
					}
					if !found {
						t.Errorf("%s candidate omitted exact source scope", kind)
					}
					cell, _ := candidate.CutIdentity().Cell(ir.FieldID(f.PostID))
					id, _ := cell.PolicyValue()
					for _, title := range []string{"video", "revoked"} {
						// No Author relation is supplied: the historical edge must
						// stay cut, but the authored mutable Title guard must remain.
						record := must(evaluate.NewRecord(ir.ModelID(f.Post), must(evaluate.ValueField(ir.FieldID(f.PostID), id)), must(evaluate.ValueField(ir.FieldID(f.PostTitle), must(ir.StringValue(title))))))
						matches, err := evaluate.Condition(candidate.Prefix(), record, ir.PortableProviders())
						if err != nil || matches != (title == "video") {
							t.Errorf("%s current title=%q matches=%v err=%v: source guard lost or old edge rejoined", kind, title, matches, err)
						}
					}
				}
			})
		}
	}
}

func TestHistoricalIndependentQuestionsPreserveHomogeneousSourceFactAlgebra(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	owner := historicalOwnerCondition(t, fixture, false)
	question := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	for _, operation := range []ir.LogicalOperator{ir.LogicalNot, ir.LogicalOr} {
		for _, allowed := range []bool{false, true} {
			actor := byte(2)
			if allowed {
				actor = 1
			}
			scope := must(ir.NewFactScope(historicalScalar(t, f.Registry, f.Post, f.AuthorID, ir.UUIDValue([16]byte{15: actor}))))
			first := must(ir.NewFact(ir.ModelID(f.Post), must(ir.FactIdentity(ir.ModelID(f.Post), "first")), historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("first-false"))), scope))
			children := []ir.Condition{first}
			if operation == ir.LogicalOr {
				children = append(children, must(ir.NewFact(ir.ModelID(f.Post), must(ir.FactIdentity(ir.ModelID(f.Post), "second")), historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("second-false"))), scope)))
			}
			data := must(ir.NewLogical(ir.ModelID(f.Post), operation, children))
			// A common complete local policy scope is conjoined outside the
			// data algebra, as in a resolved rule using Not(fact) or fact.Or.
			row := must(logical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{scope.Condition(), data}))
			for _, mask := range []bool{false, true} {
				plan := must(NewHistorical(row, &question))
				if mask {
					plan = must(NewHistorical(row, nil, question))
				}
				candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
				if err != nil {
					t.Fatalf("operation=%v allowed=%v mask=%v: %v", operation, allowed, mask, err)
				}
				if !allowed {
					if len(candidates) != 0 {
						t.Fatalf("operation=%v mask=%v borrowed local scope", operation, mask)
					}
					continue
				}
				if len(candidates) != 2 {
					t.Fatalf("operation=%v mask=%v candidates=%d want both captured siblings", operation, mask, len(candidates))
				}
				for _, candidate := range candidates {
					additional := candidate.AdditionalScopes()
					if len(additional) != 1 || must(ir.FactScopeFingerprint(additional[0].Scope())) != must(ir.FactScopeFingerprint(scope)) {
						t.Fatal("source data Boolean algebra lost or multiplied its one scope")
					}
				}
			}
		}
	}
}

func TestHistoricalIndependentQuestionsKeepUncapturedSourceGuard(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	owner := historicalOwnerCondition(t, fixture, false)
	post := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	question := historicalRelation(t, f.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(f.Post), ir.RelationToOne, post)
	row := historicalScalar(t, f.Registry, fixture.watch, fixture.profile, ir.UUIDValue([16]byte{15: 1}))
	for _, mask := range []bool{false, true} {
		plan := must(NewHistorical(row, &question))
		if mask {
			plan = must(NewHistorical(row, nil, question))
		}
		candidates := must(plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity}))
		if len(candidates) != 2 {
			t.Fatalf("mask=%v candidates=%d want2", mask, len(candidates))
		}
		for _, candidate := range candidates {
			if candidate.CutOffset() != 1 {
				t.Fatal("fixture did not leave the Watch source before the captured cut")
			}
			cell, _ := candidate.CutIdentity().Cell(ir.FieldID(f.PostID))
			video, _ := cell.PolicyValue()
			for _, actor := range []byte{1, 2} {
				record := must(evaluate.NewRecord(ir.ModelID(fixture.watch), must(evaluate.ValueField(ir.FieldID(fixture.video), video)), must(evaluate.ValueField(ir.FieldID(fixture.profile), ir.UUIDValue([16]byte{15: actor})))))
				matches, err := evaluate.Condition(candidate.Prefix(), record, ir.PortableProviders())
				if err != nil || matches != (actor == 1) {
					t.Errorf("mask=%v actor=%d matches=%v err=%v: uncaptured source guard lost", mask, actor, matches, err)
				}
			}
		}
	}
}
