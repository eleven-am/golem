package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestMutationHistoryFinalizeSealsOnceAtOuterCallbackEnd(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	var causal string
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		binding := transaction.system.executor
		if binding.mutation.registry != fixture.app.registry || binding.mutation.eventSchemas != fixture.app.eventSchemas {
			t.Fatal("mutation configuration lost exact app schema owners")
		}
		for _, id := range []byte{201, 202} {
			if err := mutationHistorySealCreate(fixture, transaction, id); err != nil {
				return err
			}
		}
		state, err := binding.mutationState()
		if err != nil {
			return err
		}
		causal = state.facts[0].CausationID
		if err := finalizeMutationHistory(fixture.ctx, binding); err != nil {
			return err
		}
		before := append([]mutationfact.OutboxRow(nil), state.facts...)
		seal, bytes := state.historySeal, state.bytes
		if seal == nil {
			t.Fatal("finalization did not seal the actual fact buffer")
		}
		if err := finalizeMutationHistory(fixture.ctx, binding); err != nil {
			return err
		}
		if state.historySeal != seal || state.bytes != bytes || !reflect.DeepEqual(before, state.facts) {
			t.Fatal("unchanged repeated finalization re-encoded or resealed facts")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := mutationHistoryFinalizeStored(t, fixture, causal, 2)
	children, complete := graph.Children(policyhistory.After, graph.RootKeys()[0], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
	if !complete || len(children) != 2 {
		t.Fatalf("final graph children=%d complete=%v", len(children), complete)
	}
}

func TestMutationHistoryFinalizeIncludesHookWritesAndExcludesRolledBackChildren(t *testing.T) {
	var fixture policyFactRuntimeFixture
	var target byte
	refusal := errors.New("rollback hook child")
	refuse := false
	fixture = newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		schemaTransform:    func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
		postMutationGrants: true,
		hooks: func(graph schematest.GraphFixture) []golem.HookBinding[policyFactRuntimeActor] {
			return []golem.HookBinding[policyFactRuntimeActor]{
				golem.GeneratedAfterHookBinding[policyFactRuntimeActor, policyFactRuntimePost, golem.UpdateManyHookResult[policyFactRuntimePost]](graph.Post, golem.HookUpdateMany, func(ctx context.Context, result golem.UpdateManyHookResult[policyFactRuntimePost]) error {
					_, err := golem.HookUpdateRow(ctx, result.Executor(), fixture.posts, mutationHistoryRuntimeTarget(fixture, target), mutationHistoryRuntimeUpdate(fixture, "hook-final"))
					if err != nil {
						return err
					}
					if refuse {
						return refusal
					}
					return nil
				}),
			}
		},
	})
	for _, id := range []byte{211, 212} {
		fixture.mutate(t, golem.EventCreated, id, 1, "before")
	}
	var causal string
	err := CallerTransaction(fixture.ctx, fixture.caller(t), func(transaction *CallerTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := CallerTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 212), mutationHistoryRuntimeUpdate(fixture, "sibling")); err != nil {
			return err
		}
		target, refuse = 211, true
		if _, err := CallerTxUpdateMany(fixture.ctx, transaction, fixture.posts, fixture.postID.Eq(golem.UUID{15: target}), mutationHistoryRuntimeUpdateMany(fixture, "batch")); !errors.Is(err, refusal) {
			t.Fatalf("child savepoint did not return its hook refusal: %v", err)
		}
		target, refuse = 212, false
		if _, err := CallerTxUpdateMany(fixture.ctx, transaction, fixture.posts, fixture.postID.Eq(golem.UUID{15: target}), mutationHistoryRuntimeUpdateMany(fixture, "batch")); err != nil {
			return err
		}
		state, err := transaction.caller.executor.mutationState()
		if err != nil {
			return err
		}
		causal = state.facts[0].CausationID
		return finalizeMutationHistory(fixture.ctx, transaction.caller.executor)
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := mutationHistoryFinalizeStored(t, fixture, causal, 3)
	children, complete := graph.Children(policyhistory.After, graph.RootKeys()[0], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
	if !complete || len(children) != 2 {
		t.Fatal("finalizer lost the complete sibling inventory")
	}
	for _, row := range children {
		id := mutationHistoryGraphRowID(t, row, fixture.schema.PostID)
		want := "before"
		if id == 212 {
			want = "hook-final"
		}
		mutationHistoryText(t, &row, fixture.schema.PostTitle, want)
	}
	mutationHistoryRuntimeTitle(t, fixture, 211, "before")
	mutationHistoryRuntimeTitle(t, fixture, 212, "hook-final")
}

func TestMutationHistoryFinalizeCapFailureRollsBackApplicationAndDelivery(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	var causal string
	var refusal error
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if err := mutationHistorySealCreate(fixture, transaction, 213); err != nil {
			return err
		}
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		causal = state.facts[0].CausationID
		before := append([]mutationfact.OutboxRow(nil), state.facts...)
		bytes, epoch := state.bytes, state.factEpoch
		state.limits.outboxBytes = bytes // V3 fits, complete history does not.
		refusal = finalizeMutationHistory(fixture.ctx, transaction.system.executor)
		mutationHistoryLimit(t, refusal, golem.LimitOutboxBytes)
		if !reflect.DeepEqual(before, state.facts) || state.bytes != bytes || state.factEpoch != epoch || state.historySeal != nil {
			t.Fatal("failed finalization changed the old buffer")
		}
		return refusal
	})
	if err == nil || !errors.Is(err, refusal) {
		t.Fatalf("finalization refusal did not abort commit: %v", err)
	}
	mutationHistorySealAbsent(t, fixture, 213)
	if len(mutationHistorySealStoredRows(t, fixture, causal)) != 0 {
		t.Fatal("failed finalization committed facts")
	}
	var delivery int
	if err := fixture.app.database.GetContext(fixture.ctx, &delivery, `SELECT COUNT(*) FROM _golem_outbox_delivery WHERE causation_id=?`, causal); err != nil || delivery != 0 {
		t.Fatalf("failed finalization left delivery rows=%d err=%v", delivery, err)
	}
}

func TestMutationHistoryFinalizeRequiresSchemaOwnersAndRejectsChangedSeals(t *testing.T) {
	for _, mode := range []string{"registry", "resolver", "captured-write", "uncaptured-write"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
			var causal string
			err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
				if err := mutationHistorySealCreate(fixture, transaction, 214); err != nil {
					return err
				}
				binding := transaction.system.executor
				state, err := binding.mutationState()
				if err != nil {
					return err
				}
				causal = state.facts[0].CausationID
				switch mode {
				case "registry":
					binding.mutation.registry = nil
				case "resolver":
					binding.mutation.eventSchemas = nil
				default:
					if err := finalizeMutationHistory(fixture.ctx, binding); err != nil {
						return err
					}
					if mode == "captured-write" {
						_, err = SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 214), mutationHistoryRuntimeUpdate(fixture, "later"))
					} else {
						_, err = SystemTxCreate(fixture.ctx, transaction, mutationHistoryGraphComments(fixture.schema), mutationHistoryGraphCommentInput(fixture.schema, 215, 214, "uncaptured"))
					}
					if err != nil {
						return err
					}
				}
				return finalizeMutationHistory(fixture.ctx, binding)
			})
			if err == nil {
				t.Fatal("invalid finalization silently retained V3 or reused a stale seal")
			}
			if strings.HasSuffix(mode, "write") && !strings.Contains(err.Error(), "changed after historical graph sealing") {
				t.Fatalf("later write was not refused by sealed epochs: %v", err)
			}
			if (mode == "registry" || mode == "resolver") && !strings.Contains(err.Error(), "require their owning registry and event schema resolver") {
				t.Fatalf("missing schema owner failed for an unrelated reason: %v", err)
			}
			mutationHistorySealAbsent(t, fixture, 214)
			if len(mutationHistorySealStoredRows(t, fixture, causal)) != 0 {
				t.Fatal("invalid finalization committed facts")
			}
		})
	}
}

func TestMutationHistoryFinalizePreservesTerminalAndFailureChecks(t *testing.T) {
	failure := errors.New("prior mutation failed")
	for _, mode := range []string{"finished", "flushed", "failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			state := mutationHistoryState(t)
			binding := &executionBinding{state: state}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "finished":
				state.finished = true
			case "flushed":
				state.flushed = true
			case "failure":
				state.failure = failure
			case "canceled":
				cancel()
			}
			err := finalizeMutationHistory(ctx, binding)
			if err == nil {
				t.Fatal("empty-state no-op bypassed terminal or failure check")
			}
			if mode == "failure" && !errors.Is(err, failure) {
				t.Fatalf("prior mutation failure was replaced: %v", err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was replaced: %v", err)
			}
		})
	}
}

func TestMutationHistoryFinalizeLeavesOrdinaryV3AndEmptyTransactionsAlone(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true})
	var causal string
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if err := finalizeMutationHistory(fixture.ctx, transaction.system.executor); err != nil {
			return err
		}
		if err := mutationHistorySealCreate(fixture, transaction, 216); err != nil {
			return err
		}
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		causal = state.facts[0].CausationID
		return finalizeMutationHistory(fixture.ctx, transaction.system.executor)
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != 1 || rows[0].FactVersion != int64(mutationfact.FormatVersion) {
		t.Fatal("ordinary uncaptured transaction changed its fact format")
	}
}

func mutationHistoryFinalizeStored(t *testing.T, fixture policyFactRuntimeFixture, causal string, count int) policyhistory.Graph {
	t.Helper()
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != count {
		t.Fatalf("stored causation count=%d want%d", len(rows), count)
	}
	value, err := mutationfact.DecodePolicyHistoryCausation(rows, fixture.app.eventSchemas)
	if err != nil {
		t.Fatal(err)
	}
	for index, row := range rows {
		envelope, err := mutationfact.ValidateStoredRow(row, fixture.app.eventSchemas)
		if err != nil {
			t.Fatal(err)
		}
		_, anchor := envelope.PolicyHistoryCompanion()
		if row.TransactionOrdinal != int64(index+1) || anchor != (index == 0) {
			t.Fatal("finalizer changed ordinal continuity or stored multiple anchors")
		}
	}
	return value.Graph()
}
