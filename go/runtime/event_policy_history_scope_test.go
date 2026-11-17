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
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

func eventHistoryScopeFixture(t *testing.T, noFacts bool) policyFactRuntimeFixture {
	t.Helper()
	return newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		userNameScope: true, rootNoFacts: noFacts,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
	})
}

func eventHistoryScopeCapture(t *testing.T, fixture policyFactRuntimeFixture, change func(*SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error) *eventPolicyHistory {
	t.Helper()
	var causal string
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if err := change(transaction); err != nil {
			return err
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		causal = state.facts[0].CausationID
		return state.sealHistory(fixture.ctx, graph, fixture.app.eventSchemas)
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) == 0 {
		t.Fatal("scope fixture committed no facts")
	}
	shared := &eventNoticeShared{envelope: eventPolicyHistoryWire(t, fixture, rows[len(rows)-1])}
	evidence, err := fixture.app.policyHistoryForNotice(fixture.ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func eventHistoryScopePosts(t *testing.T, fixture policyFactRuntimeFixture) *eventPolicyHistory {
	t.Helper()
	return eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		for _, id := range []byte{141, 142} {
			if err := mutationHistorySealCreate(fixture, transaction, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func eventHistoryScopeCaller(t *testing.T, fixture policyFactRuntimeFixture, id byte) *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor] {
	t.Helper()
	principal := fixture.principal
	principal.ID = golem.UUID{15: id}
	caller, err := fixture.app.ForPrincipal(fixture.ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func eventHistoryScopeSetName(t *testing.T, fixture policyFactRuntimeFixture, name string) {
	t.Helper()
	selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	field := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.users, selector, golem.GeneratedUpdateInput[policyFactRuntimeUser](fixture.schema.User,
		golem.GeneratedSetFieldValue(fixture.schema.User, field, name))); err != nil {
		t.Fatal(err)
	}
}

func assertEventHistoryScope(t *testing.T, fixture policyFactRuntimeFixture, caller *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], evidence *eventPolicyHistory, want eventHistoryScopeVerdict) {
	t.Helper()
	keys := evidence.value.Graph().RootKeys()
	if len(keys) != 1 {
		t.Fatalf("scope roots=%d want 1", len(keys))
	}
	got, err := fixture.app.eventHistoryScope(fixture.ctx, caller, evidence, keys[0])
	if err != nil || got != want {
		t.Fatalf("scope verdict=%+v want=%+v err=%v", got, want, err)
	}
}

func TestEventHistoryScopeRechecksCurrentDomainWithoutCanReadOrActorTruthCache(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopePosts(t, fixture)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	// Neither captured Post has title "ready": the data fact and CanRead are
	// false, while the explicit source domain still authorizes invalidation.
	count, err := CallerCount(fixture.ctx, caller, fixture.users)
	if err != nil || count != 0 {
		t.Fatalf("fixture CanRead count=%d want 0 err=%v", count, err)
	}
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
	// B changes only persisted scope data; the actor and its policy stay fixed.
	eventHistoryScopeSetName(t, fixture, "revoked")
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: false})
	eventHistoryScopeSetName(t, fixture, "source")
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
	// User 2 still matches this actor's scope in the current DB, so the false
	// current result also proves the probe retained the exact historical key.
	other := eventHistoryScopeCaller(t, fixture, 2)
	assertEventHistoryScope(t, fixture, other, evidence, eventHistoryScopeVerdict{})
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
}

func TestEventHistoryScopePreservesLossIndependentOfDataFactRelevance(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.users, selector, golem.GeneratedUpdateInput[policyFactRuntimeUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, name, "revoked")))
		return err
	})
	caller := eventHistoryScopeCaller(t, fixture, 1)
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true})
	// Restoring current authorization does not rewrite A's scope-loss phases.
	eventHistoryScopeSetName(t, fixture, "source")
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, current: true})
}

func TestEventHistoryScopeTreatsKnownAbsentOwnerAsFalse(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		id := golem.GeneratedEqualField[policyFactRuntimeUser, golem.UUID](fixture.schema.UserID)
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		_, err := SystemTxCreate(fixture.ctx, transaction, fixture.users, golem.GeneratedCreateInput[policyFactRuntimeUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, id, golem.UUID{15: 3}), golem.GeneratedCreateFieldValue(fixture.schema.User, name, "source")))
		return err
	})
	assertEventHistoryScope(t, fixture, eventHistoryScopeCaller(t, fixture, 3), evidence, eventHistoryScopeVerdict{after: true, current: true})
}

func TestEventHistoryScopeRejectsUnownedEvidenceAndUnadmittedCallers(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopePosts(t, fixture)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	foreignFixture := eventHistoryScopeFixture(t, false)
	foreign := eventHistoryScopeCaller(t, foreignFixture, 1)
	otherActor := eventHistoryScopeCaller(t, fixture, 2)
	otherCapture := schematest.NewSubscribedGraph(t).Registry
	wrongGeneration := fixture.schema.Bundle.GenerationDigest()
	wrongGeneration[0] ^= 1
	bundle := fixture.schema.Bundle
	changedRegistry, err := schema.New(golem.GeneratedSchemaBundle(wrongGeneration, bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), bundle.Providers()...))
	if err != nil {
		t.Fatal(err)
	}
	pgGraph, err := policyhistory.New(evidence.registry, policyir.ProviderPostgreSQL, evidence.value.Graph().Input())
	if err != nil {
		t.Fatal(err)
	}
	pgEvidence, err := mutationfact.NewPolicyHistory(evidence.value.CausationID(), evidence.value.FactsDigest(), pgGraph)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], *eventPolicyHistory, *string)
	}{
		{"foreign caller", func(c *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], _ *eventPolicyHistory, _ *string) {
			*c = *foreign
		}},
		{"missing executor", func(c *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], _ *eventPolicyHistory, _ *string) {
			c.executor = nil
		}},
		{"unadmitted executor even outside actor domain", func(c *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], _ *eventPolicyHistory, _ *string) {
			*c = *otherActor
			c.executor = databaseExecution(fixture.app.database)
			c.executor.scoped = true
		}},
		{"inactive executor", func(c *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], _ *eventPolicyHistory, _ *string) {
			c.executor = databaseExecution(fixture.app.database)
			c.executor.active.Store(false)
		}},
		{"foreign executor database", func(c *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], _ *eventPolicyHistory, _ *string) {
			c.executor = foreign.executor
		}},
		{"missing owner key", func(_ *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], _ *eventPolicyHistory, key *string) {
			*key = "missing-owner"
		}},
		{"different generation", func(_ *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], e *eventPolicyHistory, _ *string) {
			e.registry = changedRegistry
		}},
		{"different capture registry", func(_ *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], e *eventPolicyHistory, _ *string) {
			e.registry = otherCapture
		}},
		{"different physical provider", func(_ *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], e *eventPolicyHistory, _ *string) {
			e.value = pgEvidence
		}},
		{"missing graph", func(_ *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], e *eventPolicyHistory, _ *string) {
			e.value = mutationfact.PolicyHistory{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, candidateEvidence, key := *caller, *evidence, evidence.value.Graph().RootKeys()[0]
			test.change(&candidate, &candidateEvidence, &key)
			got, err := fixture.app.eventHistoryScope(fixture.ctx, &candidate, &candidateEvidence, key)
			var refusal *events.Error
			if got != (eventHistoryScopeVerdict{}) || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation {
				t.Fatalf("invalid authority verdict=%+v err=%v", got, err)
			}
			if err.Error() != events.Failure(events.CodeSubscriptionRevalidation).Error() {
				t.Fatal("scope error disclosed private evidence or driver details")
			}
		})
	}
	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	if got, err := fixture.app.eventHistoryScope(ctx, caller, evidence, evidence.value.Graph().RootKeys()[0]); got != (eventHistoryScopeVerdict{}) || err != context.Canceled {
		t.Fatalf("cancellation verdict=%+v err=%v", got, err)
	}
}

func TestEventHistoryScopeRequiresActualFactOwningPolicy(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, true)
	evidence := eventHistoryScopePosts(t, fixture)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	got, err := fixture.app.eventHistoryScope(fixture.ctx, caller, evidence, evidence.value.Graph().RootKeys()[0])
	var refusal *events.Error
	if got != (eventHistoryScopeVerdict{}) || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation {
		t.Fatalf("non-fact policy scope verdict=%+v err=%v", got, err)
	}
}

func TestEventHistoryScopeRejectsAdmittedCallerTransaction(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopePosts(t, fixture)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
	entered := false
	err := CallerTransaction(fixture.ctx, caller, func(transaction *CallerTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		_, err := transactionOperation(fixture.ctx, transactionCall{binding: transaction.caller.executor}, func(ctx context.Context) (struct{}, error) {
			entered = true
			binding := transaction.caller.executor
			if err := binding.requireAdmitted(ctx); err != nil {
				t.Fatalf("fixture transaction operation must be admitted: %v", err)
			}
			// Establish a real database snapshot before asking for current scope.
			var users int
			if err := binding.transaction.GetContext(ctx, &users, `SELECT COUNT(*) FROM users`); err != nil {
				return struct{}{}, err
			}
			if users == 0 {
				t.Fatal("fixture transaction read no source rows")
			}
			got, err := fixture.app.eventHistoryScope(ctx, transaction.caller, evidence, evidence.value.Graph().RootKeys()[0])
			var refusal *events.Error
			if got != (eventHistoryScopeVerdict{}) || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation {
				t.Fatalf("admitted transaction supplied a current scope verdict: %+v err=%v", got, err)
			}
			return struct{}{}, nil
		})
		return err
	})
	if err != nil || !entered {
		t.Fatalf("admitted transaction fixture: entered=%v err=%v", entered, err)
	}
	// Rejecting the transaction capability must not poison the normal caller.
	assertEventHistoryScope(t, fixture, caller, evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
}

func TestEventHistoryScopePreparationBatchesCurrentFencesInOneStatement(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopePosts(t, fixture)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	ownerKey := evidence.value.Graph().RootKeys()[0]
	// The second current fence checks the original source-to-owner prefix:
	// Post 141 must still belong to the historically selected User 1.
	postID, err := mutationdecode.IdentityValue(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue([16]byte{15: 141}))
	if err != nil {
		t.Fatal(err)
	}
	authorID, err := mutationdecode.IdentityValue(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue([16]byte{15: 1}))
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := equalityCondition(fixture.app.registry, fixture.app.provider, policyir.ModelID(fixture.schema.Post), []mutationdecode.IdentityComponent{postID, authorID})
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantScope, wantPrefix bool) {
		t.Helper()
		ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.User, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
		defer observeexec.Finish(span, observe.OutcomeSuccess, observe.ReasonNone)
		verdict, scopeProbe, err := fixture.app.prepareEventHistoryScope(ctx, caller, evidence, ownerKey)
		if err != nil {
			t.Fatal(err)
		}
		if verdict != (eventHistoryScopeVerdict{before: true, after: true}) || scopeProbe == nil || scopeProbe.answered || span.StatementCount() != 0 {
			t.Fatalf("preparation executed a current read or changed historical flags: verdict=%+v statements=%d", verdict, span.StatementCount())
		}
		prefixProbe := &deleteProbe{target: fixture.schema.Post, condition: prefix}
		if err := fixture.app.runDeleteProbes(ctx, caller, []*deleteProbe{scopeProbe, prefixProbe}); err != nil {
			t.Fatal(err)
		}
		if !scopeProbe.answered || !prefixProbe.answered || scopeProbe.exists != wantScope || prefixProbe.exists != wantPrefix || span.StatementCount() != 1 {
			t.Fatalf("current fences did not share one statement: scope=%+v prefix=%+v statements=%d", scopeProbe, prefixProbe, span.StatementCount())
		}
	}
	check(true, true)
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 141), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	eventHistoryScopeSetName(t, fixture, "revoked")
	check(false, false)
}
