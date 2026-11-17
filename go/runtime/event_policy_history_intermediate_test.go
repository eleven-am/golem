package runtime

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

// Watch intentionally has no subscription. This acceptance covers real caller
// policy expansion, sealed stored V4 evidence and the routed consumer's current
// SQL fence; it does not claim Watch transport/factory delivery coverage.
func TestEventPolicyHistoryIntermediateGuardUsesRecordedIdentityAndCurrentTruth(t *testing.T) {
	extra := schematest.NewPolicyCaptureOrdinaryGuardGraph(t)
	fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		ordinaryGuardGraph: &extra, postMutationGrants: true, userNameScope: true,
	})
	fixture.mutate(t, golem.EventCreated, 191, 1, "video")
	comments := mutationHistoryGraphComments(fixture.schema)
	for _, item := range []struct {
		id   byte
		body string
	}{{192, "ready"}, {193, "bridge"}} {
		if _, err := SystemCreate(fixture.ctx, fixture.app.System(), comments, mutationHistoryGraphCommentInput(fixture.schema, item.id, 191, item.body)); err != nil {
			t.Fatal(err)
		}
	}
	watches := policyFactRuntimeWatchDescriptor(extra)
	if _, err := SystemCreate(fixture.ctx, fixture.app.System(), watches, golem.GeneratedCreateInput[policyFactRuntimeWatch](extra.Watch,
		golem.GeneratedCreateFieldValue(extra.Watch, golem.GeneratedEqualField[policyFactRuntimeWatch, golem.UUID](extra.ID), golem.UUID{15: 194}),
		golem.GeneratedCreateFieldValue(extra.Watch, golem.GeneratedEqualField[policyFactRuntimeWatch, golem.UUID](extra.Profile), golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(extra.Watch, golem.GeneratedEqualField[policyFactRuntimeWatch, golem.UUID](extra.Video), golem.UUID{15: 191}),
	)); err != nil {
		t.Fatal(err)
	}
	caller := eventHistoryScopeCaller(t, fixture, 1)
	other := eventHistoryScopeCaller(t, fixture, 2)
	// The same expanded request must still be rejected under the default
	// five-hop limit. This is a supported public depth configuration, not a
	// cycle-protection bypass for the relation-free Post policy reentry.
	_, _, depthErr := readplan.CallerReach(caller.policies, policyir.ModelID(extra.Watch), nil, readplan.DefaultLimits().MaxRelationDepth)
	var limit *readplan.Error
	if !errors.As(depthErr, &limit) || limit.Code != readplan.CodeLimit || limit.Detail != "policy fact relation depth limit exceeded" {
		t.Fatalf("default depth did not reject the six-hop request for the expected reason: %v", depthErr)
	}
	if fixture.app.readLimits.plan.MaxRelationDepth != 6 {
		t.Fatal("public fixture read-depth configuration was not applied")
	}
	if count, err := CallerCount(fixture.ctx, caller, watches); err != nil || count != 1 {
		t.Fatalf("initial Watch visibility=%d want1 err=%v", count, err)
	}
	if count, err := CallerCount(fixture.ctx, caller, comments); err != nil || count != 1 {
		t.Fatalf("only the bridge should be ordinarily readable: count=%d err=%v", count, err)
	}
	body := golem.GeneratedTextField[policyFactRuntimeComment, string](fixture.schema.CommentBody)
	selector := func(id byte) golem.UniqueSelectorValue[policyFactRuntimeComment] {
		return golem.GeneratedUniqueSelectorValue[policyFactRuntimeComment](fixture.schema.Comment, fixture.schema.CommentKey, golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: id}))
	}
	// A changes only the physical Copy. The bridge's CanRead remains true in
	// both recorded phases, independently of User1's now-false data fact.
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		_, err := SystemTxUpdate(fixture.ctx, transaction, comments, selector(192), golem.GeneratedUpdateInput[policyFactRuntimeComment](fixture.schema.Comment,
			golem.GeneratedSetFieldValue(fixture.schema.Comment, body, "not-ready")))
		return err
	})
	shared := eventHistoryRouteNotice(t, fixture, evidence)
	if shared.envelope.ModelID() != fixture.schema.Comment || shared.envelope.Action() != golem.EventUpdated {
		t.Fatal("A must be the actual stored Copy update")
	}
	if _, historical := shared.envelope.Fact().PolicyHistory(); !historical {
		t.Fatal("A did not retain its sealed V4 history reference")
	}
	planFor := func(current *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) observation.Plan {
		t.Helper()
		row, filter, err := readplan.CallerReach(current.policies, policyir.ModelID(extra.Watch), nil, fixture.app.readLimits.plan.MaxRelationDepth)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := observation.New(row, filter)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	plan := planFor(caller)
	before, known := shared.envelope.Fact().BeforeIdentity()
	if !known {
		t.Fatal("Copy update lost its primary identity")
	}
	candidates, err := plan.HistoricalCandidates(fixture.ctx, evidence.registry, evidence.value.Graph(), observation.HistoricalChange{Model: policyir.ModelID(fixture.schema.Comment), Before: &before})
	if err != nil || len(candidates) == 0 {
		t.Fatalf("real ordinary path produced no historical candidates: %v", err)
	}
	for _, candidate := range candidates {
		if candidate.CutOffset() != 1 || len(candidate.Guards()) == 0 {
			t.Fatalf("acceptance did not reach a non-cut intermediate: offset=%d guards=%d", candidate.CutOffset(), len(candidate.Guards()))
		}
	}
	check := func(want bool) {
		t.Helper()
		changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, extra.Watch, shared, plan)
		if err != nil || changed != want {
			t.Fatalf("historical Watch route changed=%v want=%v err=%v", changed, want, err)
		}
		changed, err = fixture.app.historicalFactDependencyChanged(fixture.ctx, other, extra.Watch, shared, planFor(other))
		if err != nil || changed {
			t.Fatalf("recorded User1 activity escaped to actor2: changed=%v err=%v", changed, err)
		}
	}
	check(true)
	// B changes the recorded Post's current owner, leaving both source FK and
	// bridge guard intact. A must continue using the recorded User1 chain.
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 191), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
		t.Fatal(err)
	}
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
	if count, err := CallerCount(fixture.ctx, caller, watches); err != nil || count != 0 {
		t.Fatalf("current full CanRead must now deny Watch: count=%d err=%v", count, err)
	}
	check(true)
	// Revoke only the intermediate bridge guard. Neither the Watch profile/FK
	// nor User1's explicit scope changes; the historical Copy stays unreadable.
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), comments, selector(193), golem.GeneratedUpdateInput[policyFactRuntimeComment](fixture.schema.Comment,
		golem.GeneratedSetFieldValue(fixture.schema.Comment, body, "revoked"))); err != nil {
		t.Fatal(err)
	}
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
	for _, candidate := range candidates {
		prefix := &deleteProbe{target: extra.Watch, condition: candidate.Prefix()}
		if err := fixture.app.runDeleteProbes(fixture.ctx, caller, []*deleteProbe{prefix}); err != nil {
			t.Fatal(err)
		}
		if !prefix.answered || !prefix.exists {
			t.Fatal("guard revocation also changed the source prefix")
		}
	}
	check(false)
}
