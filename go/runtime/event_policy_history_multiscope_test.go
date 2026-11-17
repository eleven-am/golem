package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

// Both fact-owning models have real registered declarations. The unchanged
// sibling172 is captured through User's closure, not an affected Post root.
func eventHistoryMultiScopeFixture(t *testing.T) (policyFactRuntimeFixture, *eventPolicyHistory) {
	t.Helper()
	fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		userNameScope: true, postLocalFact: true,
		schemaTransform: func(base schematest.GraphFixture) schematest.GraphFixture {
			return policyFactRuntimeCaptureSchema(t, base, policyFactRuntimeOptions{userNameScope: true, postLocalFact: true})
		},
	})
	fixture.mutate(t, golem.EventCreated, 171, 1, "ready")
	fixture.mutate(t, golem.EventCreated, 172, 1, "other")
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 171), mutationHistoryRuntimeUpdate(fixture, "changed"))
		return err
	})
	userKey, _ := eventHistoryMultiScopeUserRoot(t, fixture, evidence)
	siblingKey, _ := eventHistoryMultiScopeProof(t, fixture, eventHistoryScopeCaller(t, fixture, 1), 172)
	for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
		if row, known := evidence.value.Graph().FindRoot(phase, userKey); !known || row == nil {
			t.Fatal("fixture must retain the surviving User1 affected root in both phases")
		}
		if _, known := evidence.value.Graph().FindRoot(phase, siblingKey); known {
			t.Fatal("unchanged sibling172 incorrectly became an affected Post root")
		}
		if row, found := evidence.value.Graph().FindRow(phase, siblingKey); !found || mutationHistoryGraphRowID(t, row, fixture.schema.PostID) != 172 {
			t.Fatal("unchanged sibling172 must remain captured in both phases")
		}
	}
	return fixture, evidence
}

func eventHistoryMultiScopeUserRoot(t *testing.T, fixture policyFactRuntimeFixture, evidence *eventPolicyHistory) (string, mutationdecode.Row) {
	t.Helper()
	for index, root := range evidence.value.Graph().Roots() {
		if root.Identity.ModelID() == policyir.ModelID(fixture.schema.User) && mutationHistoryGraphRowID(t, root.Identity, fixture.schema.UserID) == 1 {
			return evidence.value.Graph().RootKeys()[index], root.Identity
		}
	}
	t.Fatal("User1 root is missing")
	return "", mutationdecode.Row{}
}

func eventHistoryMultiScopeProof(t *testing.T, fixture policyFactRuntimeFixture, caller *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], id byte) (string, eventHistoryScopeProof) {
	t.Helper()
	identity, err := mutationdecode.NewRow(fixture.app.registry, policyir.ModelID(fixture.schema.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue((golem.UUID{15: id}).Bytes())),
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := policyhistory.RowKey(fixture.app.registry, fixture.app.provider, identity)
	if err != nil {
		t.Fatal(err)
	}
	policy, present := caller.policies.Policy(identity.ModelID())
	if !present {
		t.Fatal("Post fact policy is missing")
	}
	return key, eventHistoryScopeProof{identity: identity, scope: policy.FactScope(), captured: true}
}

func eventHistoryMultiScopeReparent(t *testing.T, fixture policyFactRuntimeFixture, id, owner byte) {
	t.Helper()
	_, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, id), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: owner})))
	if err != nil {
		t.Fatal(err)
	}
}

func TestEventHistoryScopeCapturedSiblingUsesExactHistoricalAndCurrentIdentity(t *testing.T) {
	fixture, evidence := eventHistoryMultiScopeFixture(t)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	key, proof := eventHistoryMultiScopeProof(t, fixture, caller, 172)
	if _, known := evidence.value.Graph().FindRoot(policyhistory.Before, key); known {
		t.Fatal("sibling incorrectly became an affected graph root")
	}
	check := func(wantCurrent bool) {
		t.Helper()
		verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(fixture.ctx, caller, evidence, key, &proof, &eventHistoryScopeEvaluation{})
		if err != nil || verdict != (eventHistoryScopeVerdict{before: true, after: true}) || probe == nil || probe.answered {
			t.Fatalf("captured scope preparation: verdict=%+v probe=%+v err=%v", verdict, probe, err)
		}
		if err := fixture.app.runDeleteProbes(fixture.ctx, caller, []*deleteProbe{probe}); err != nil || !probe.answered || probe.exists != wantCurrent {
			t.Fatalf("captured current scope: probe=%+v want=%v err=%v", probe, wantCurrent, err)
		}
	}
	// CanRead is false for both current Posts, while the independent actor
	// scope still permits historical invalidation of the exact sibling.
	if count, err := CallerCount(fixture.ctx, caller, fixture.posts); err != nil || count != 0 {
		t.Fatalf("current Post fact count=%d want=0 err=%v", count, err)
	}
	check(true)
	eventHistoryMultiScopeReparent(t, fixture, 172, 2)
	check(false) // Post171 still belongs to actor1 and cannot lend its scope.
	other := eventHistoryScopeCaller(t, fixture, 2)
	_, otherProof := eventHistoryMultiScopeProof(t, fixture, other, 172)
	verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(fixture.ctx, other, evidence, key, &otherProof, &eventHistoryScopeEvaluation{})
	if err != nil || verdict != (eventHistoryScopeVerdict{}) {
		t.Fatalf("new actor borrowed historical sibling authority: %+v err=%v", verdict, err)
	}
	if err := fixture.app.runDeleteProbes(fixture.ctx, other, []*deleteProbe{probe}); err != nil || !probe.exists {
		t.Fatalf("actor2 must have current but no historical authority: %+v err=%v", probe, err)
	}
	eventHistoryMultiScopeReparent(t, fixture, 172, 1)
	check(true) // No actor truth was cached with the immutable notice.
}

func TestEventHistoryScopeIncompleteCapturedPhaseIsNotKnownAbsence(t *testing.T) {
	fixture, evidence := eventHistoryMultiScopeFixture(t)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	key, proof := eventHistoryMultiScopeProof(t, fixture, caller, 172)
	verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(fixture.ctx, caller, evidence, key, &proof, &eventHistoryScopeEvaluation{})
	if err != nil || verdict != (eventHistoryScopeVerdict{before: true, after: true}) || probe == nil {
		t.Fatalf("unmodified real sibling evidence must grant both phases: verdict=%+v probe=%+v err=%v", verdict, probe, err)
	}
	for _, omitted := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
		name := "missing-before"
		if omitted == policyhistory.After {
			name = "missing-after"
		}
		t.Run(name, func(t *testing.T) {
			// This is structurally valid incomplete PRIVATE test evidence derived
			// from a real producer graph, not a native or accepted causal history.
			// It is never persisted, loaded as a notice, or given to a publisher.
			input := evidence.value.Graph().Input()
			remap := make(map[policyhistory.RowRef]policyhistory.RowRef, len(input.Rows))
			var removed policyhistory.RowRef
			rows := make([]policyhistory.PhaseRow, 0, len(input.Rows)-1)
			for index, row := range input.Rows {
				old := policyhistory.RowRef(index + 1)
				rowKey, err := policyhistory.RowKey(evidence.registry, fixture.app.provider, row.Row)
				if err != nil {
					t.Fatal(err)
				}
				if row.Phase == omitted && rowKey == key {
					if removed != 0 {
						t.Fatal("sibling phase appeared more than once")
					}
					removed = old
					continue
				}
				rows = append(rows, row)
				remap[old] = policyhistory.RowRef(len(rows))
			}
			if removed == 0 {
				t.Fatal("real sibling phase was not captured")
			}
			reference := func(old policyhistory.RowRef) policyhistory.RowRef {
				if old == 0 {
					return 0
				}
				value, present := remap[old]
				if !present {
					t.Fatalf("surviving graph reference %d has no retained row", old)
				}
				return value
			}
			input.Rows = rows
			for index := range input.Roots {
				input.Roots[index].Before.Row = reference(input.Roots[index].Before.Row)
				input.Roots[index].After.Row = reference(input.Roots[index].After.Row)
			}
			var extents []policyhistory.Extent
			for _, extent := range input.Extents {
				if extent.Parent == removed {
					continue
				}
				extent.Parent = reference(extent.Parent)
				children := make([]policyhistory.RowRef, 0, len(extent.Children))
				for _, child := range extent.Children {
					if child != removed {
						children = append(children, reference(child))
					}
				}
				extent.Children = children
				extents = append(extents, extent)
			}
			input.Extents = extents
			graph, err := policyhistory.New(evidence.registry, fixture.app.provider, input)
			if err != nil {
				t.Fatalf("incomplete-phase fixture must remain structurally valid: %v", err)
			}
			for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
				if _, known := graph.FindRoot(phase, key); known {
					t.Fatal("captured sibling acquired a root absence certificate")
				}
				if _, present := graph.FindRow(phase, key); present != (phase != omitted) {
					t.Fatal("fixture must retain exactly the other captured sibling phase")
				}
			}
			value, err := mutationfact.NewPolicyHistory(evidence.value.CausationID(), evidence.value.FactsDigest(), graph)
			if err != nil {
				t.Fatal(err)
			}
			private := &eventPolicyHistory{registry: evidence.registry, value: value}
			ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.Post, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
			verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(ctx, caller, private, key, &proof, &eventHistoryScopeEvaluation{})
			observeexec.Finish(span, observe.OutcomeRefused, observe.ReasonNone)
			var refusal *events.Error
			if verdict != (eventHistoryScopeVerdict{}) || probe != nil || span.StatementCount() != 0 || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation || refusal.Reason() != "" || refusal.Detail() != "" {
				t.Fatalf("missing captured phase became authority or known absence: verdict=%+v probe=%+v statements=%d err=%v", verdict, probe, span.StatementCount(), err)
			}
		})
	}
}

func TestEventHistoryScopeCapturedProofRejectsKeyIdentityAndFullScopeMismatch(t *testing.T) {
	fixture, evidence := eventHistoryMultiScopeFixture(t)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	key, proof := eventHistoryMultiScopeProof(t, fixture, caller, 172)
	other := eventHistoryScopeCaller(t, fixture, 2)
	wrongKey, _ := eventHistoryMultiScopeProof(t, fixture, caller, 171)
	for _, test := range []struct {
		name   string
		change func(*eventHistoryScopeProof, *string, **Caller[policyFactRuntimePrincipal, policyFactRuntimeActor])
	}{
		{"different-key", func(_ *eventHistoryScopeProof, key *string, _ **Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) {
			*key = wrongKey
		}},
		{"foreign-actor-scope", func(_ *eventHistoryScopeProof, _ *string, candidate **Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) {
			*candidate = other
		}},
		{"extra-nonidentity-cell", func(proof *eventHistoryScopeProof, _ *string, _ **Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) {
			text, _ := policyir.StringValue("private-scope-marker")
			row, err := mutationdecode.NewRow(fixture.app.registry, proof.identity.ModelID(), append(proof.identity.Cells(), mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), text)))
			if err != nil {
				t.Fatal(err)
			}
			proof.identity = row
		}},
		{"different-model", func(proof *eventHistoryScopeProof, _ *string, _ **Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) {
			_, proof.identity = eventHistoryMultiScopeUserRoot(t, fixture, evidence)
		}},
		{"same-condition-different-binding", func(proof *eventHistoryScopeProof, _ *string, _ **Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) {
			bound, err := policyir.NewBoundFactScope(proof.scope.Condition(), policyir.ScopeBinding{TemplateID: [16]byte{1}, Template: policyir.Fingerprint{2}, Arguments: policyir.Fingerprint{3}})
			if err != nil {
				t.Fatal(err)
			}
			proof.scope = bound
		}},
		{"root-only-cannot-borrow-captured-row", func(proof *eventHistoryScopeProof, _ *string, _ **Caller[policyFactRuntimePrincipal, policyFactRuntimeActor]) {
			proof.captured = false
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, candidateKey, candidateCaller := proof, key, caller
			test.change(&candidate, &candidateKey, &candidateCaller)
			verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(fixture.ctx, candidateCaller, evidence, candidateKey, &candidate, &eventHistoryScopeEvaluation{})
			var refusal *events.Error
			if verdict != (eventHistoryScopeVerdict{}) || probe != nil || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation || refusal.Reason() != "" || refusal.Detail() != "" {
				t.Fatalf("invalid scope proof returned authority: verdict=%+v probe=%+v err=%v", verdict, probe, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	if _, probe, err := fixture.app.prepareEventHistoryScopeProof(ctx, caller, evidence, key, &proof, &eventHistoryScopeEvaluation{}); !errors.Is(err, context.Canceled) || probe != nil {
		t.Fatalf("scope proof cancellation: probe=%+v err=%v", probe, err)
	}
}

func TestEventHistoryScopeRegisteredOwnerPreservesAbsenceAndRejectsUnknownIdentity(t *testing.T) {
	for _, action := range []golem.EventAction{golem.EventCreated, golem.EventDeleted} {
		t.Run(string(action), func(t *testing.T) {
			fixture, _ := eventHistoryMultiScopeFixture(t)
			id := byte(173)
			if action == golem.EventDeleted {
				fixture.mutate(t, golem.EventCreated, id, 1, "other")
			}
			evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
				if action == golem.EventDeleted {
					_, err := SystemTxDelete(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, id))
					return err
				}
				return mutationHistorySealCreate(fixture, transaction, id)
			})
			caller := eventHistoryScopeCaller(t, fixture, 1)
			key, proof := eventHistoryMultiScopeProof(t, fixture, caller, id)
			// A directly changed registered owner is an affected root. Its
			// opposite phase is explicitly absent, rather than an unknown
			// phase of an incidental captured row like sibling172.
			before, beforeKnown := evidence.value.Graph().FindRoot(policyhistory.Before, key)
			after, afterKnown := evidence.value.Graph().FindRoot(policyhistory.After, key)
			created := action == golem.EventCreated
			if !beforeKnown || !afterKnown || (before == nil) != created || (after != nil) != created {
				t.Fatal("registered owner creation/deletion lost its explicit root absence")
			}
			verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(fixture.ctx, caller, evidence, key, &proof, &eventHistoryScopeEvaluation{})
			want := eventHistoryScopeVerdict{before: !created, after: created}
			if err != nil || verdict != want || probe == nil {
				t.Fatalf("registered owner scope: verdict=%+v want=%+v probe=%+v err=%v", verdict, want, probe, err)
			}
			if err := fixture.app.runDeleteProbes(fixture.ctx, caller, []*deleteProbe{probe}); err != nil || !probe.answered || probe.exists != created {
				t.Fatalf("registered owner current scope: probe=%+v want=%t err=%v", probe, created, err)
			}
			unknownKey, unknown := eventHistoryMultiScopeProof(t, fixture, caller, 174)
			for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
				if _, known := evidence.value.Graph().FindRoot(phase, unknownKey); known {
					t.Fatal("unrecorded owner identity acquired an absence certificate")
				}
				if _, known := evidence.value.Graph().FindRow(phase, unknownKey); known {
					t.Fatal("unknown control unexpectedly has a captured row")
				}
			}
			verdict, probe, err = fixture.app.prepareEventHistoryScopeProof(fixture.ctx, caller, evidence, unknownKey, &unknown, &eventHistoryScopeEvaluation{})
			var refusal *events.Error
			if verdict != (eventHistoryScopeVerdict{}) || probe != nil || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation {
				t.Fatalf("unknown owner became a certified absence: %+v probe=%+v err=%v", verdict, probe, err)
			}
		})
	}
}

func TestEventHistoryScopeAdditionalProofPreservesExplicitRootAbsence(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		id := golem.GeneratedEqualField[policyFactRuntimeUser, golem.UUID](fixture.schema.UserID)
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		_, err := SystemTxCreate(fixture.ctx, transaction, fixture.users, golem.GeneratedCreateInput[policyFactRuntimeUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, id, golem.UUID{15: 3}), golem.GeneratedCreateFieldValue(fixture.schema.User, name, "source")))
		return err
	})
	caller := eventHistoryScopeCaller(t, fixture, 3)
	policy, _ := caller.policies.Policy(policyir.ModelID(fixture.schema.User))
	proof := eventHistoryScopeProof{identity: evidence.value.Graph().Roots()[0].Identity, scope: policy.FactScope(), captured: true}
	verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(fixture.ctx, caller, evidence, evidence.value.Graph().RootKeys()[0], &proof, &eventHistoryScopeEvaluation{})
	if err != nil || verdict != (eventHistoryScopeVerdict{after: true}) {
		t.Fatalf("explicit absence lost: %+v err=%v", verdict, err)
	}
	if err := fixture.app.runDeleteProbes(fixture.ctx, caller, []*deleteProbe{probe}); err != nil || !probe.exists {
		t.Fatalf("created owner current scope: %+v err=%v", probe, err)
	}
}

func eventHistoryMultiScopeQuestions(t *testing.T, fixture policyFactRuntimeFixture, caller *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], evidence *eventPolicyHistory, posts ...byte) (policyir.Condition, []eventHistoryScopeVerdict, []*deleteProbe) {
	t.Helper()
	key, owner := eventHistoryMultiScopeUserRoot(t, fixture, evidence)
	identity, err := mutationdecode.PrimaryIdentity(fixture.app.registry, owner)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := eventIdentityCondition(fixture.app, owner.ModelID(), identity)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := &eventHistoryScopeEvaluation{}
	verdict, probe, err := fixture.app.prepareEventHistoryScopeWithEvaluation(fixture.ctx, caller, evidence, key, evaluation)
	if err != nil {
		t.Fatal(err)
	}
	verdicts, probes := []eventHistoryScopeVerdict{verdict}, []*deleteProbe{probe}
	for _, id := range posts {
		key, proof := eventHistoryMultiScopeProof(t, fixture, caller, id)
		verdict, probe, err := fixture.app.prepareEventHistoryScopeProof(fixture.ctx, caller, evidence, key, &proof, evaluation)
		if err != nil {
			t.Fatal(err)
		}
		verdicts, probes = append(verdicts, verdict), append(probes, probe)
	}
	return prefix, verdicts, probes
}

func TestEventHistoryCurrentGuardScopesCannotBorrowAnotherCandidateAnswers(t *testing.T) {
	fixture, evidence := eventHistoryMultiScopeFixture(t)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	prefix, verdicts, probes := eventHistoryMultiScopeQuestions(t, fixture, caller, evidence, 172, 171)
	falsehood, err := policyir.NewConstant(policyir.ModelID(fixture.schema.User), false)
	if err != nil {
		t.Fatal(err)
	}
	alternatives := []eventHistoryRouteAlternative{
		{prefix: prefix, scopes: []*deleteProbe{probes[0], probes[1]}},
		{prefix: falsehood, scopes: []*deleteProbe{probes[0], probes[2]}},
	}
	authorities := []eventHistoryRouteAuthority{
		{phase: policyhistory.Before, scopes: []eventHistoryScopeVerdict{verdicts[0], verdicts[1]}},
		{phase: policyhistory.Before, scopes: []eventHistoryScopeVerdict{verdicts[0], verdicts[2]}},
	}
	eventHistoryMultiScopeReparent(t, fixture, 172, 2)
	check := func(want bool) {
		t.Helper()
		ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.User, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
		defer observeexec.Finish(span, observe.OutcomeSuccess, observe.ReasonNone)
		matches, err := fixture.app.matchEventHistoryRoutes(ctx, caller, fixture.schema.User, alternatives)
		if err != nil || len(matches) != 2 || span.StatementCount() != 1 {
			t.Fatalf("scope clauses did not share one SQL snapshot: matches=%+v statements=%d err=%v", matches, span.StatementCount(), err)
		}
		if !matches[0].prefix || matches[1].prefix || len(matches[0].scopes) != 2 || len(matches[1].scopes) != 2 || !matches[0].scopes[0] || matches[0].scopes[1] != want || !matches[1].scopes[0] || !matches[1].scopes[1] {
			t.Fatalf("candidate-local scope positions changed: %+v", matches)
		}
		for index, match := range matches {
			activity, cleanup, err := authorities[index].outcome(match)
			if err != nil || cleanup || activity != (index == 0 && want) {
				t.Fatalf("candidate %d borrowed another answer: activity=%v cleanup=%v err=%v", index, activity, cleanup, err)
			}
		}
	}
	check(false) // True prefix A cannot borrow true Post171 scope from B.
	eventHistoryMultiScopeReparent(t, fixture, 172, 1)
	check(true) // Reuse prepared probes, but never their prior answer flags.
	eventHistoryMultiScopeReparent(t, fixture, 172, 2)
	check(false)
}

func TestEventHistoryCurrentGuardScopeLossDoesNotBorrowLaterRevocation(t *testing.T) {
	fixture, _ := eventHistoryMultiScopeFixture(t)
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.users, selector, golem.GeneratedUpdateInput[policyFactRuntimeUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, name, "revoked")))
		return err
	})
	caller := eventHistoryScopeCaller(t, fixture, 1)
	prefix, verdicts, probes := eventHistoryMultiScopeQuestions(t, fixture, caller, evidence, 172)
	if verdicts[0] != (eventHistoryScopeVerdict{before: true}) || verdicts[1] != (eventHistoryScopeVerdict{before: true, after: true}) {
		t.Fatalf("loss fixture phases=%+v", verdicts)
	}
	authority := eventHistoryRouteAuthority{phase: policyhistory.Before, scopes: verdicts}
	alternatives := []eventHistoryRouteAlternative{{prefix: prefix, scopes: probes}}
	check := func(wantCleanup bool) {
		t.Helper()
		matches, err := fixture.app.matchEventHistoryRoutes(fixture.ctx, caller, fixture.schema.User, alternatives)
		if err != nil || len(matches) != 1 {
			t.Fatalf("scope loss current query: %+v err=%v", matches, err)
		}
		activity, cleanup, err := authority.outcome(matches[0])
		if err != nil || activity || cleanup != wantCleanup {
			t.Fatalf("scope loss activity=%v cleanup=%v want cleanup=%v err=%v", activity, cleanup, wantCleanup, err)
		}
	}
	check(true)
	eventHistoryMultiScopeReparent(t, fixture, 172, 2) // B revoked a different, unchanged-by-A fence.
	check(false)
	eventHistoryMultiScopeReparent(t, fixture, 172, 1)
	check(true)

	// A separate real scope gain is eligible only in its AFTER phase.
	gain := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.users, selector, golem.GeneratedUpdateInput[policyFactRuntimeUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, name, "source")))
		return err
	})
	prefix, verdicts, probes = eventHistoryMultiScopeQuestions(t, fixture, caller, gain, 172)
	matches, err := fixture.app.matchEventHistoryRoutes(fixture.ctx, caller, fixture.schema.User, []eventHistoryRouteAlternative{{prefix: prefix, scopes: probes}})
	if err != nil || len(matches) != 1 {
		t.Fatalf("gain query: %+v err=%v", matches, err)
	}
	for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
		activity, cleanup, err := (eventHistoryRouteAuthority{phase: phase, scopes: verdicts}).outcome(matches[0])
		if err != nil || cleanup || activity != (phase == policyhistory.After) {
			t.Fatalf("gain phase=%d activity=%v cleanup=%v err=%v", phase, activity, cleanup, err)
		}
	}
}

func TestEventHistoryCurrentGuardScopesCannotCombineDifferentHistoricalPhases(t *testing.T) {
	fixture, _ := eventHistoryMultiScopeFixture(t)
	eventHistoryMultiScopeReparent(t, fixture, 172, 2)
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		if _, err := SystemTxUpdate(fixture.ctx, transaction, fixture.users, selector, golem.GeneratedUpdateInput[policyFactRuntimeUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, name, "revoked"))); err != nil {
			return err
		}
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 172), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1})))
		return err
	})
	eventHistoryScopeSetName(t, fixture, "source") // B makes both current fences true.
	caller := eventHistoryScopeCaller(t, fixture, 1)
	prefix, verdicts, probes := eventHistoryMultiScopeQuestions(t, fixture, caller, evidence, 172)
	if verdicts[0] != (eventHistoryScopeVerdict{before: true}) || verdicts[1] != (eventHistoryScopeVerdict{after: true}) {
		t.Fatalf("cross-phase fixture=%+v", verdicts)
	}
	matches, err := fixture.app.matchEventHistoryRoutes(fixture.ctx, caller, fixture.schema.User, []eventHistoryRouteAlternative{{prefix: prefix, scopes: probes}})
	if err != nil || len(matches) != 1 || len(matches[0].scopes) != 2 || !matches[0].scopes[0] || !matches[0].scopes[1] {
		t.Fatalf("current fences should both grant: %+v err=%v", matches, err)
	}
	for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
		activity, cleanup, err := (eventHistoryRouteAuthority{phase: phase, scopes: verdicts}).outcome(matches[0])
		if err != nil || activity || cleanup {
			t.Fatalf("cross-phase authority borrowed: phase=%d activity=%v cleanup=%v err=%v", phase, activity, cleanup, err)
		}
	}
}
