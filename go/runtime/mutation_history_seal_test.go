package runtime

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestMutationHistorySealPersistsOneGraphWithTheActualCausation(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	var causal string
	var capturedState *mutationState
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		for _, id := range []byte{111, 112} {
			if err := mutationHistorySealCreate(fixture, transaction, id); err != nil {
				return err
			}
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
		capturedState = state
		causal = state.facts[0].CausationID
		before := state.bytes
		if err := state.sealHistory(fixture.ctx, graph, fixture.app.eventSchemas); err != nil {
			return err
		}
		if state.historySeal == nil || state.bytes <= before || state.bytes > state.limits.outboxBytes {
			t.Fatal("sealing did not stage bounded complete evidence")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !capturedState.finished || capturedState.facts != nil || capturedState.historySeal != nil || capturedState.history != nil {
		t.Fatal("commit retained transaction-local capture state")
	}
	// Read the bytes back from the real immutable outbox after commit. The
	// reader has no surviving transaction graph or mutation fact buffer.
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != 2 {
		t.Fatalf("persisted facts=%d want2", len(rows))
	}
	resolver, err := newEventSchemaHistory(fixture.app.registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	companion, err := mutationfact.DecodePolicyHistoryCausation(rows, resolver)
	if err != nil {
		t.Fatal(err)
	}
	graph := companion.Graph()
	if len(graph.Roots()) != 1 {
		t.Fatalf("persisted source roots=%d want1", len(graph.Roots()))
	}
	key := graph.RootKeys()[0]
	for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
		children, known := graph.Children(phase, key, policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
		want := 0
		if phase == policyhistory.After {
			want = 2
		}
		if !known || len(children) != want {
			t.Fatalf("phase=%d complete=%v children=%d want=%d", phase, known, len(children), want)
		}
	}
	for index, row := range rows {
		envelope, err := mutationfact.ValidateStoredRow(row, resolver)
		if err != nil {
			t.Fatal(err)
		}
		_, stored := envelope.PolicyHistoryCompanion()
		if stored != (index == 0) {
			t.Fatal("graph was not stored exactly once")
		}
	}
}

func TestMutationHistorySealBudgetFailureRollsBackRowsAndFacts(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	var causal string
	var refusal error
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if err := mutationHistorySealCreate(fixture, transaction, 113); err != nil {
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
		before := append([]mutationfact.OutboxRow(nil), state.facts...)
		bytes, epoch := state.bytes, state.factEpoch
		// A valid lowered application budget can fit ordinary facts while
		// refusing the complete historical evidence. Nothing may be clipped.
		state.limits.outboxBytes = bytes
		refusal = state.sealHistory(fixture.ctx, graph, fixture.app.eventSchemas)
		mutationHistoryLimit(t, refusal, golem.LimitOutboxBytes)
		if !reflect.DeepEqual(state.facts, before) || state.bytes != bytes || state.factEpoch != epoch || state.historySeal != nil {
			t.Fatal("failed sealing changed the original fact buffer")
		}
		return refusal
	})
	if err == nil || !errors.Is(err, refusal) {
		t.Fatalf("transaction did not preserve sealing refusal: %v", err)
	}
	if len(mutationHistorySealStoredRows(t, fixture, causal)) != 0 {
		t.Fatal("failed sealing committed causal facts")
	}
	mutationHistorySealAbsent(t, fixture, 113)
}

func TestMutationHistorySealRejectsLaterCapturedAndUncapturedWrites(t *testing.T) {
	for _, uncaptured := range []bool{false, true} {
		t.Run(map[bool]string{false: "captured", true: "uncaptured"}[uncaptured], func(t *testing.T) {
			fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
			var causal string
			err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
				if err := mutationHistorySealCreate(fixture, transaction, 114); err != nil {
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
				if err := state.sealHistory(fixture.ctx, graph, fixture.app.eventSchemas); err != nil {
					return err
				}
				epoch := state.historyEpoch
				if uncaptured {
					_, err = SystemTxCreate(fixture.ctx, transaction, mutationHistoryGraphComments(fixture.schema), mutationHistoryGraphCommentInput(fixture.schema, 115, 114, "uncaptured-model"))
					if err == nil && state.historyEpoch != epoch {
						t.Fatal("fixture's extra model unexpectedly participated in the capture inventory")
					}
					return err
				}
				_, err = SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 114), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "later")))
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "changed after historical graph sealing") {
				t.Fatalf("stale sealed graph reached commit: %v", err)
			}
			if len(mutationHistorySealStoredRows(t, fixture, causal)) != 0 {
				t.Fatal("stale sealed graph committed facts")
			}
			mutationHistorySealAbsent(t, fixture, 114)
		})
	}
}

func mutationHistorySealCreate(fixture policyFactRuntimeFixture, transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor], id byte) error {
	_, err := SystemTxCreate(fixture.ctx, transaction, fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}), golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}), golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "private-graph-value")))
	return err
}

func mutationHistorySealAbsent(t *testing.T, fixture policyFactRuntimeFixture, id byte) {
	t.Helper()
	var count int
	if err := fixture.app.database.GetContext(fixture.ctx, &count, `SELECT COUNT(*) FROM posts WHERE id=?`, golem.UUID{15: id}.String()); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rolled-back write survived")
	}
}

func mutationHistorySealStoredRows(t *testing.T, fixture policyFactRuntimeFixture, causal string) []mutationfact.OutboxRow {
	t.Helper()
	rows, err := fixture.app.database.QueryxContext(fixture.ctx, `SELECT event_id,fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,transaction_ordinal,metadata,delete_snapshot,recorded_at FROM _golem_outbox WHERE causation_id=? ORDER BY transaction_ordinal`, causal)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []mutationfact.OutboxRow
	for rows.Next() {
		var row mutationfact.OutboxRow
		var recorded int64
		if err := rows.Scan(&row.EventID, &row.FactVersion, &row.CodecIdentity, &row.GenerationFingerprint, &row.ModelID, &row.Action, &row.BeforeIdentity, &row.AfterIdentity, &row.CausationID, &row.TransactionOrdinal, &row.Metadata, &row.DeleteSnapshot, &recorded); err != nil {
			t.Fatal(err)
		}
		row.RecordedAt = time.UnixMicro(recorded).UTC()
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
