package runtime

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func eventHistoryRoutePlan(t *testing.T, fixture policyFactRuntimeFixture, caller *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) observation.Plan {
	t.Helper()
	row, filter, err := readplan.CallerReach(caller.policies, policyir.ModelID(fixture.schema.User), nil, fixture.app.readLimits.plan.MaxRelationDepth)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := observation.New(row, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Paths()) == 0 {
		t.Fatal("real User policy produced no fact observation paths")
	}
	return plan
}

func eventHistoryRouteNotice(t *testing.T, fixture policyFactRuntimeFixture, evidence *eventPolicyHistory) *eventNoticeShared {
	t.Helper()
	causal := golem.UUID(evidence.value.CausationID()).String()
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) == 0 {
		t.Fatal("sealed source causation has no stored notice")
	}
	return &eventNoticeShared{envelope: eventPolicyHistoryWire(t, fixture, rows[len(rows)-1])}
}

func eventHistoryRouteReadyPost(t *testing.T, fixture policyFactRuntimeFixture) *eventPolicyHistory {
	t.Helper()
	return eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		_, err := SystemTxCreate(fixture.ctx, transaction, fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 161}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "ready")))
		return err
	})
}

func TestHistoricalFactDependencyUsesCurrentScopeWithoutLiveFactBodyOrActorCache(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryRouteReadyPost(t, fixture)
	shared := eventHistoryRouteNotice(t, fixture, evidence)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	plan := eventHistoryRoutePlan(t, fixture, caller)
	check := func(want bool) {
		t.Helper()
		changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.User, shared, plan)
		if err != nil || changed != want {
			t.Fatalf("historical dependency changed=%v want=%v err=%v", changed, want, err)
		}
	}
	if count, err := CallerCount(fixture.ctx, caller, fixture.users); err != nil || count != 1 {
		t.Fatalf("initial authorized User count=%d want 1 err=%v", count, err)
	}
	check(true)
	// A certified true-to-true scope in A cannot attribute hidden activity
	// after B revokes current scope, even with the same cached raw graph.
	eventHistoryScopeSetName(t, fixture, "revoked")
	check(false)
	eventHistoryScopeSetName(t, fixture, "source")
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 161), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "not-ready"))); err != nil {
		t.Fatal(err)
	}
	if count, err := CallerCount(fixture.ctx, caller, fixture.users); err != nil || count != 0 {
		t.Fatalf("live data fact must now deny CanRead: count=%d err=%v", count, err)
	}
	// Historical A remains relevant inside the explicit current owner domain;
	// reapplying today's full CanRead would incorrectly hide its invalidation.
	check(true)
	other := eventHistoryScopeCaller(t, fixture, 2)
	otherPlan := eventHistoryRoutePlan(t, fixture, other)
	changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, other, fixture.schema.User, shared, otherPlan)
	if err != nil || changed {
		t.Fatalf("another actor borrowed User 1's graph authority: changed=%v err=%v", changed, err)
	}
	check(true)
}

func TestHistoricalFactDependencyScopeLossReturnsUnattributedCleanup(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	if count, err := CallerCount(fixture.ctx, caller, fixture.users); err != nil || count != 0 {
		t.Fatalf("scope-loss fixture must have no relevant ready Post: count=%d err=%v", count, err)
	}
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.users, selector, golem.GeneratedUpdateInput[policyFactRuntimeUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, name, "revoked")))
		return err
	})
	shared := eventHistoryRouteNotice(t, fixture, evidence)
	changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.User, shared, eventHistoryRoutePlan(t, fixture, caller))
	var failure *events.Error
	if changed || !errors.As(err, &failure) || failure.Code() != events.CodeSubscriptionResync || failure.Reason() != "" || failure.Detail() != "" || errors.Unwrap(failure) != nil {
		t.Fatalf("certified scope loss must return generic cleanup without activity: changed=%v err=%v", changed, err)
	}
	other := eventHistoryScopeCaller(t, fixture, 2)
	changed, err = fixture.app.historicalFactDependencyChanged(fixture.ctx, other, fixture.schema.User, shared, eventHistoryRoutePlan(t, fixture, other))
	if changed || err != nil {
		t.Fatalf("scope loss leaked cleanup to another actor: changed=%v err=%v", changed, err)
	}
}

func TestHistoricalFactDependencyRefusesMissingOrMalformedEvidence(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "missing anchor"
		if malformed {
			name = "malformed graph"
		}
		t.Run(name, func(t *testing.T) {
			fixture := eventHistoryScopeFixture(t, false)
			evidence := eventHistoryRouteReadyPost(t, fixture)
			shared := eventHistoryRouteNotice(t, fixture, evidence)
			causal := golem.UUID(evidence.value.CausationID()).String()
			if malformed {
				eventPolicyHistoryExec(t, fixture, `UPDATE _golem_outbox SET delete_snapshot=? WHERE causation_id=? AND transaction_ordinal=1`, []byte("private-graph-value-corrupt"), causal)
			} else {
				eventPolicyHistoryExec(t, fixture, `DELETE FROM _golem_outbox WHERE causation_id=? AND transaction_ordinal=1`, causal)
			}
			caller := eventHistoryScopeCaller(t, fixture, 1)
			changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.User, shared, eventHistoryRoutePlan(t, fixture, caller))
			var failure *events.Error
			if changed || !errors.As(err, &failure) || failure.Code() != events.CodeSubscriptionRevalidation || failure.Reason() != "" || failure.Detail() != "" || errors.Unwrap(failure) != nil {
				t.Fatalf("unavailable evidence escaped generic revalidation: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestHistoricalFactDependencyDirectOwnerDeletionCleansUpWithoutCurrentRow(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		_, err := SystemTxDelete(fixture.ctx, transaction, fixture.users, selector)
		return err
	})
	graph := evidence.value.Graph()
	keys := graph.RootKeys()
	if len(keys) != 1 {
		t.Fatalf("deleted owner roots=%d want1", len(keys))
	}
	if before, known := graph.FindRoot(policyhistory.Before, keys[0]); !known || before == nil {
		t.Fatal("deleted owner before image is missing")
	}
	if after, known := graph.FindRoot(policyhistory.After, keys[0]); !known || after != nil {
		t.Fatal("deleted owner must be known absent after")
	}
	verdict, scope, err := fixture.app.prepareEventHistoryScope(fixture.ctx, caller, evidence, keys[0])
	if err != nil || !verdict.before || verdict.after {
		t.Fatalf("deleted owner recorded scope: %+v %v", verdict, err)
	}
	shared := eventHistoryRouteNotice(t, fixture, evidence)
	plan := eventHistoryRoutePlan(t, fixture, caller)
	identity, present := shared.envelope.Fact().BeforeIdentity()
	if !present {
		t.Fatal("deleted fact has no before identity")
	}
	candidates, err := plan.HistoricalCandidates(fixture.ctx, evidence.registry, graph, observation.HistoricalChange{Model: policyir.ModelID(fixture.schema.User), Before: &identity})
	if err != nil || len(candidates) == 0 {
		t.Fatalf("deleted owner candidates=%d err=%v", len(candidates), err)
	}
	for _, candidate := range candidates {
		prefix := &deleteProbe{target: fixture.schema.User, condition: candidate.Prefix()}
		if err := fixture.app.runDeleteProbes(fixture.ctx, caller, []*deleteProbe{scope, prefix}); err != nil {
			t.Fatal(err)
		}
		if !scope.answered || !prefix.answered || scope.exists || prefix.exists {
			t.Fatal("deleted owner unexpectedly satisfied a current fence")
		}
	}
	changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.User, shared, plan)
	var failure *events.Error
	if changed || !errors.As(err, &failure) || failure.Code() != events.CodeSubscriptionResync || failure.Reason() != "" || failure.Detail() != "" || errors.Unwrap(failure) != nil {
		t.Fatalf("deleted direct owner did not receive unattributed cleanup: changed=%v err=%v", changed, err)
	}
	other := eventHistoryScopeCaller(t, fixture, 2)
	if changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, other, fixture.schema.User, shared, eventHistoryRoutePlan(t, fixture, other)); err != nil || changed {
		t.Fatalf("owner deletion disclosed activity to actor2: changed=%v err=%v", changed, err)
	}
}

func TestHistoricalFactDependencyCapturedCutSurvivesReparentButRequiresCurrentSource(t *testing.T) {
	fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		postMutationGrants: true, commentOwnerRead: true, userNameScope: true,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
	})
	fixture.mutate(t, golem.EventCreated, 171, 1, "ready")
	fixture.mutate(t, golem.EventCreated, 172, 1, "not-ready")
	fixture.mutate(t, golem.EventCreated, 173, 2, "ready")
	comments := mutationHistoryGraphComments(fixture.schema)
	if _, err := SystemCreate(fixture.ctx, fixture.app.System(), comments, mutationHistoryGraphCommentInput(fixture.schema, 1, 172, "source W")); err != nil {
		t.Fatal(err)
	}
	caller := eventHistoryScopeCaller(t, fixture, 1)
	if count, err := CallerCount(fixture.ctx, caller, comments); err != nil || count != 1 {
		t.Fatalf("initial W visibility: count=%d err=%v", count, err)
	}
	// A changes V1. The physical owner extent must retain sibling V2 as a
	// possible ordinary cut even though V2 does not satisfy the data fact.
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 171), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "not-ready")))
		return err
	})
	graph := evidence.value.Graph()
	keys := graph.RootKeys()
	if len(keys) != 1 {
		t.Fatalf("A owner roots=%d want1", len(keys))
	}
	for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
		rows, known := graph.Children(phase, keys[0], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
		seen := map[byte]bool{}
		for _, row := range rows {
			cell, _ := row.Cell(policyir.FieldID(fixture.schema.PostID))
			value, _ := cell.PolicyValue()
			id, _ := value.UUID()
			seen[id[15]] = true
		}
		if !known || len(rows) != 2 || !seen[171] || !seen[172] {
			t.Fatalf("A omitted complete sibling cut evidence in phase%d", phase)
		}
	}
	// B changes V2's live owner after A, before A is routed. The old owner
	// relationship is still A's immutable evidence; only W→V2 stays current.
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 172), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
		t.Fatal(err)
	}
	if count, err := CallerCount(fixture.ctx, caller, comments); err != nil || count != 0 {
		t.Fatalf("live full CanRead must now deny W: count=%d err=%v", count, err)
	}
	shared := eventHistoryRouteNotice(t, fixture, evidence)
	commentPlan := func(caller *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) observation.Plan {
		row, filter, err := readplan.CallerReach(caller.policies, policyir.ModelID(fixture.schema.Comment), nil, fixture.app.readLimits.plan.MaxRelationDepth)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := observation.New(row, filter)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	plan := commentPlan(caller)
	if changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.Comment, shared, plan); err != nil || !changed {
		t.Fatalf("A lost its authorized captured sibling route after B: changed=%v err=%v", changed, err)
	}
	other := eventHistoryScopeCaller(t, fixture, 2)
	if count, err := CallerCount(fixture.ctx, other, fixture.users); err != nil || count != 1 {
		t.Fatalf("actor2 must have an otherwise readable current owner: count=%d err=%v", count, err)
	}
	if changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, other, fixture.schema.Comment, shared, commentPlan(other)); err != nil || changed {
		t.Fatalf("A's cut escaped the source actor guard: changed=%v err=%v", changed, err)
	}
	// Removing W from the current ordinary prefix must stop this route even
	// though A's immutable owner scope and sibling cut still match actor1.
	selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeComment](fixture.schema.Comment, fixture.schema.CommentKey, golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: 1}))
	post := golem.GeneratedEqualField[policyFactRuntimeComment, golem.UUID](fixture.schema.CommentPostID)
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), comments, selector, golem.GeneratedUpdateInput[policyFactRuntimeComment](fixture.schema.Comment, golem.GeneratedSetFieldValue(fixture.schema.Comment, post, golem.UUID{15: 173}))); err != nil {
		t.Fatal(err)
	}
	if changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.Comment, shared, plan); err != nil || changed {
		t.Fatalf("A ignored loss of its current ordinary prefix: changed=%v err=%v", changed, err)
	}
}

func TestHistoricalFactDependencyRequiresCurrentCapturedCutReadGuard(t *testing.T) {
	fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		postMutationGrants: true, postCurrentGuard: true, commentOwnerRead: true, userNameScope: true,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
	})
	// W reaches User 1 through V2, while the independent sibling V1 supplies
	// the owner's ready fact. Only V2's local read guard will later be revoked.
	fixture.mutate(t, golem.EventCreated, 181, 1, "ready")
	fixture.mutate(t, golem.EventCreated, 182, 1, "video")
	comments := mutationHistoryGraphComments(fixture.schema)
	if _, err := SystemCreate(fixture.ctx, fixture.app.System(), comments, mutationHistoryGraphCommentInput(fixture.schema, 1, 182, "source W")); err != nil {
		t.Fatal(err)
	}
	caller := eventHistoryScopeCaller(t, fixture, 1)
	if count, err := CallerCount(fixture.ctx, caller, comments); err != nil || count != 1 {
		t.Fatalf("authorized W fixture count=%d want 1 err=%v", count, err)
	}
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 181), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "not-ready")))
		return err
	})
	shared := eventHistoryRouteNotice(t, fixture, evidence)
	row, filter, err := readplan.CallerReach(caller.policies, policyir.ModelID(fixture.schema.Comment), nil, fixture.app.readLimits.plan.MaxRelationDepth)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := observation.New(row, filter)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.Comment, shared, plan); err != nil || !changed {
		t.Fatalf("authorized captured cut did not route A before B: changed=%v err=%v", changed, err)
	}
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 182), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "revoked"))); err != nil {
		t.Fatal(err)
	}
	// Keep the same caller, plan, notice, and cached immutable graph. Current
	// owner authority and W's actor/FK are unchanged; the cut guard alone fails.
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
	var current struct {
		PostID   string `db:"post_id"`
		AuthorID string `db:"author_id"`
		Title    string `db:"title"`
	}
	if err := fixture.app.database.GetContext(fixture.ctx, &current, `SELECT comments.post_id,posts.author_id,posts.title FROM comments JOIN posts ON posts.id=comments.post_id WHERE comments.id=?`, golem.UUID{15: 1}.String()); err != nil {
		t.Fatal(err)
	}
	if current.PostID != (golem.UUID{15: 182}).String() || current.AuthorID != (golem.UUID{15: 1}).String() || current.Title != "revoked" {
		t.Fatal("B changed more than the current captured-cut read guard")
	}
	if count, err := CallerCount(fixture.ctx, caller, fixture.posts, golem.Where(fixture.postID.Eq(golem.UUID{15: 182}))); err != nil || count != 0 {
		t.Fatalf("cut Post must now fail its own CanRead: count=%d err=%v", count, err)
	}
	changed, err := fixture.app.historicalFactDependencyChanged(fixture.ctx, caller, fixture.schema.Comment, shared, plan)
	if changed || err != nil {
		t.Fatalf("delayed A escaped the revoked current cut guard: changed=%v err=%v", changed, err)
	}
}
