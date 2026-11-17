package observation

import (
	"context"
	"errors"
	"testing"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func TestHistoricalIndependentQuestionsCarryMatchingPrefixAuthority(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	owner := historicalOwnerCondition(t, fixture, false)
	postOwner := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	watch := func(child ir.Condition) ir.Condition {
		return historicalRelation(t, f.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(f.Post), ir.RelationToOne, child)
	}
	profile := historicalScalar(t, f.Registry, fixture.watch, fixture.profile, ir.UUIDValue([16]byte{15: 1}))
	postGuard := historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("video")))
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
				scope := must(ir.NewFactScope(historicalScalar(t, f.Registry, f.Post, f.AuthorID, ir.UUIDValue([16]byte{15: actor}))))
				local := must(ir.NewFact(ir.ModelID(f.Post), must(ir.FactIdentity(ir.ModelID(f.Post), "post_domain")), must(ir.NewConstant(ir.ModelID(f.Post), true)), scope))
				postPolicy := must(logical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{local, postGuard, postOwner}))
				row := must(logical(ir.ModelID(fixture.watch), ir.LogicalAnd, []ir.Condition{profile, watch(postPolicy)}))
				// Like CallerReach's expanded filter, the question carries the
				// target policy as well as the caller's selected Post identity.
				postID := historicalScalar(t, f.Registry, f.Post, f.PostID, ir.UUIDValue([16]byte{15: 11}))
				question := watch(must(logical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{postPolicy, postID})))
				plan := must(NewHistorical(row, &question))
				if kind == "mask" {
					plan = must(NewHistorical(row, nil, question))
				}
				candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
				if err != nil {
					t.Fatal(err)
				}
				if !allowed {
					if len(candidates) != 0 {
						t.Fatal("matching prefix borrowed a denied captured Post scope")
					}
					return
				}
				if len(candidates) == 0 {
					t.Fatal("matching uncaptured prefix lost its captured authority")
				}
				for _, candidate := range candidates {
					if candidate.CutOffset() != 1 {
						t.Fatalf("cut=%d want1; Watch must remain uncaptured", candidate.CutOffset())
					}
					additional := candidate.AdditionalScopes()
					if len(additional) != 1 || additional[0].Identity().ModelID() != ir.ModelID(f.Post) || must(ir.FactScopeFingerprint(additional[0].Scope())) != must(ir.FactScopeFingerprint(scope)) {
						t.Fatal("carried authority lost its exact one Post scope")
					}
					cell, _ := candidate.CutIdentity().Cell(ir.FieldID(f.PostID))
					postKey, _ := cell.PolicyValue()
					for _, current := range []struct {
						actor byte
						title string
						want  bool
					}{{1, "video", true}, {2, "video", false}, {1, "revoked", false}} {
						// B has reparented this Post to User2. There is no Author
						// relation in the record: the old User1 edge must stay cut.
						postRecord := must(evaluate.NewRecord(ir.ModelID(f.Post), must(evaluate.ValueField(ir.FieldID(f.PostID), postKey)), must(evaluate.ValueField(ir.FieldID(f.PostTitle), must(ir.StringValue(current.title)))), must(evaluate.ValueField(ir.FieldID(f.AuthorID), ir.UUIDValue([16]byte{15: 2})))))
						record := must(evaluate.NewRecord(ir.ModelID(fixture.watch), must(evaluate.ValueField(ir.FieldID(fixture.profile), ir.UUIDValue([16]byte{15: current.actor}))), must(evaluate.ValueField(ir.FieldID(fixture.video), postKey)), must(evaluate.ToOneField(ir.FieldID(fixture.relationField), ir.ModelID(f.Post), postRecord))))
						matches, err := evaluate.Condition(candidate.Prefix(), record, ir.PortableProviders())
						if err != nil || matches != current.want {
							t.Errorf("actor=%d title=%q matched=%v want=%v err=%v", current.actor, current.title, matches, current.want, err)
						}
					}
				}
			})
		}
	}
}

func TestHistoricalIndependentQuestionsRefuseUnalignedPrefixAuthority(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	owner := historicalOwnerCondition(t, fixture, false)
	post := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	selected := historicalRelation(t, f.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(f.Post), ir.RelationToOne, post)
	profile := historicalScalar(t, f.Registry, fixture.watch, fixture.profile, ir.UUIDValue([16]byte{15: 1}))
	sourceScope := must(ir.NewFactScope(profile))
	sourceFact := must(ir.NewFact(ir.ModelID(fixture.watch), must(ir.FactIdentity(ir.ModelID(fixture.watch), "source_domain")), must(ir.NewConstant(ir.ModelID(fixture.watch), true)), sourceScope))
	requirements := must(operator.ValidateShape(ir.OperatorRelationIsNot, operator.Shape{Node: ir.ConditionRelation, Cardinality: ir.RelationToOne, HasChild: true, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()}))
	mismatched := must(ir.NewRelation(ir.ModelID(fixture.watch), ir.FieldID(fixture.relationField), ir.RelationID(fixture.relation), ir.ModelID(f.Post), ir.RelationToOne, ir.OperatorRelationIsNot, &post, requirements))
	for _, test := range []struct {
		name string
		row  ir.Condition
	}{
		{"source-own-scope", must(logical(ir.ModelID(fixture.watch), ir.LogicalAnd, []ir.Condition{sourceFact, selected}))},
		{"opposite-prefix-operator", mismatched},
		{"conditional-prefix", must(logical(ir.ModelID(fixture.watch), ir.LogicalOr, []ir.Condition{profile, selected}))},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, mask := range []bool{false, true} {
				plan := must(NewHistorical(test.row, &selected))
				if mask {
					plan = must(NewHistorical(test.row, nil, selected))
				}
				candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
				var failure *HistoricalCoverageError
				if !errors.As(err, &failure) || candidates != nil {
					t.Fatalf("mask=%v unrecorded source authority accepted: candidates=%d err=%v", mask, len(candidates), err)
				}
			}
		})
	}
}
