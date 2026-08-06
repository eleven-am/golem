package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

func TestMutationStateScopeRollbackRewindsAccountingAndOrdinals(t *testing.T) {
	limits, err := normalizeMutationLimits(MutationLimits{MaxTouchedRows: 3, MaxFacts: 3, MaxOutboxBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	causation := mutationfact.CausationID{1}
	state, err := newMutationState(limits, causation)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.touch(1); err != nil {
		t.Fatal(err)
	}
	if err := state.appendOutboxRow(runtimeStateRow(state, 1, []byte{1, 2})); err != nil {
		t.Fatal(err)
	}
	scope, err := state.beginScope()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.touch(2); err != nil {
		t.Fatal(err)
	}
	if err := state.appendOutboxRow(runtimeStateRow(state, 2, []byte{3, 4})); err != nil {
		t.Fatal(err)
	}
	if err := scope.rollback(); err != nil {
		t.Fatal(err)
	}
	// The rolled-back ordinal is reusable and its row/byte/touched accounting no
	// longer contributes to the outer transaction.
	if err := state.appendOutboxRow(runtimeStateRow(state, 2, []byte{5, 6})); err != nil {
		t.Fatalf("reused ordinal: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.ordinal != 2 || state.touched != 1 || len(state.facts) != 2 || state.bytes != 6 {
		t.Fatalf("state ordinal=%d touched=%d facts=%d bytes=%d", state.ordinal, state.touched, len(state.facts), state.bytes)
	}
}

func TestMutationStateFactAndByteLimitsAcceptBoundaryRejectBoundaryPlusOne(t *testing.T) {
	limits, _ := normalizeMutationLimits(MutationLimits{MaxFacts: 1, MaxOutboxBytes: 4})
	state, err := newMutationState(limits, mutationfact.CausationID{2})
	if err != nil {
		t.Fatal(err)
	}
	// Three metadata bytes plus one identity byte is the exact binary-column
	// contribution defined by OutboxRow.EncodedBytes.
	if err := state.appendOutboxRow(runtimeStateRow(state, 1, []byte{1, 2, 3})); err != nil {
		t.Fatalf("exact boundary rejected: %v", err)
	}
	if err := state.appendOutboxRow(runtimeStateRow(state, 2, nil)); err == nil {
		t.Fatal("fact boundary+1 was accepted")
	}

	state, _ = newMutationState(limits, mutationfact.CausationID{3})
	over := runtimeStateRow(state, 1, []byte{1, 2, 3, 4})
	if err := state.appendOutboxRow(over); err == nil {
		t.Fatal("byte boundary+1 was accepted")
	}
}

func TestMutationDataAndFactCommitOrRollbackTogether(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, assertMutationDataAndFactAtomic)
}

func assertMutationDataAndFactAtomic(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	outbox := nestedAcceptanceOutbox(fixture.app)
	insert := fixture.app.database.Rebind(`INSERT INTO ` + posts + `("id","author_id","title") VALUES (?,?,?)`)
	payload := []byte{0x47, 0x4f, 0x00, 0xff, 0x10}
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := transaction.caller.executor.transaction.ExecContext(ctx, insert, mutationResultUUIDText(71), mutationResultUUIDText(1), "atomic"); err != nil {
			return err
		}
		state, err := transaction.caller.executor.mutationState()
		if err != nil {
			return err
		}
		if err := state.touch(1); err != nil {
			return err
		}
		return state.appendOutboxRow(runtimeStateRow(state, 1, payload))
	})
	if err != nil {
		t.Fatal(err)
	}
	var title string
	queryTitle := fixture.app.database.Rebind(`SELECT "title" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.Get(&title, queryTitle, mutationResultUUIDText(71)); err != nil || title != "atomic" {
		t.Fatalf("data title=%q err=%v", title, err)
	}
	var stored []byte
	if err := fixture.app.database.Get(&stored, `SELECT "metadata" FROM `+outbox); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, payload) {
		t.Fatalf("stored metadata=%x want=%x", stored, payload)
	}
	sentinel := errors.New("rollback data and fact")
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := transaction.caller.executor.transaction.ExecContext(ctx, insert, mutationResultUUIDText(79), mutationResultUUIDText(1), "rolled-back"); err != nil {
			return err
		}
		state, err := transaction.caller.executor.mutationState()
		if err != nil {
			return err
		}
		if err := state.touch(1); err != nil {
			return err
		}
		if err := state.appendOutboxRow(runtimeStateRow(state, 1, []byte{9, 9})); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback err=%v", err)
	}
	var dataCount, factCount int
	queryCount := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.Get(&dataCount, queryCount, mutationResultUUIDText(79)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.database.Get(&factCount, `SELECT COUNT(*) FROM `+outbox); err != nil {
		t.Fatal(err)
	}
	if dataCount != 0 || factCount != 1 {
		t.Fatalf("rollback data=%d facts=%d", dataCount, factCount)
	}
	for _, id := range []byte{73, 74} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "batch-atomic")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+outbox); err != nil {
		t.Fatal(err)
	}
	batchRollback := errors.New("rollback batch data and facts")
	err = SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		count, updateErr := SystemTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.title.Eq("batch-atomic"), fixture.updateManyTitle("batch-rolled-back"))
		if updateErr != nil || count != 2 {
			return errors.Join(updateErr, fmt.Errorf("batch rollback count=%d", count))
		}
		return batchRollback
	})
	if !errors.Is(err, batchRollback) {
		t.Fatalf("batch rollback err=%v", err)
	}
	assertMutationResultTitleCount(t, fixture, "batch-atomic", 2)
	assertMutationResultTitleCount(t, fixture, "batch-rolled-back", 0)
	if err := fixture.app.database.Get(&factCount, `SELECT COUNT(*) FROM `+outbox); err != nil || factCount != 0 {
		t.Fatalf("batch rollback facts=%d err=%v", factCount, err)
	}
}

func TestFactLimitsRollBackInsteadOfDrop(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{MaxOutboxBytes: 4}, assertFactLimitRollback)
}

func assertFactLimitRollback(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	outbox := nestedAcceptanceOutbox(fixture.app)
	insert := fixture.app.database.Rebind(`INSERT INTO ` + posts + `("id","author_id","title") VALUES (?,?,?)`)
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := transaction.caller.executor.transaction.ExecContext(ctx, insert, mutationResultUUIDText(72), mutationResultUUIDText(1), "rollback"); err != nil {
			return err
		}
		state, err := transaction.caller.executor.mutationState()
		if err != nil {
			return err
		}
		return state.appendOutboxRow(runtimeStateRow(state, 1, []byte{1, 2, 3, 4}))
	})
	if err == nil {
		t.Fatal("outbox byte overflow committed")
	}
	var dataCount, factCount int
	queryCount := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.Get(&dataCount, queryCount, mutationResultUUIDText(72)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.database.Get(&factCount, `SELECT COUNT(*) FROM `+outbox); err != nil {
		t.Fatal(err)
	}
	if dataCount != 0 || factCount != 0 {
		t.Fatalf("rollback data=%d facts=%d", dataCount, factCount)
	}
}

func TestAfterCommitInvalidationAndFailureReportingRunOnlyAfterCommit(t *testing.T) {
	fixture := openTransactionFixture(t)
	caller, err := fixture.app.ForPrincipal(context.Background(), testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("after commit failed")
	var callbacks, reports atomic.Int64
	beforeEpoch := caller.executor.invalidationEpoch()
	fixture.app.afterCommitError = func(_ context.Context, failure golem.AfterCommitFailure) {
		if !errors.Is(failure.Cause(), sentinel) || failure.Operation() != golem.HookCreate {
			t.Errorf("failure=%v operation=%s", failure.Cause(), failure.Operation())
		}
		reports.Add(1)
	}
	err = CallerTransaction(context.Background(), caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		if _, err := transaction.caller.executor.transaction.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000073", "committed-first"); err != nil {
			return err
		}
		state, err := transaction.caller.executor.mutationState()
		if err != nil {
			return err
		}
		if err := state.touch(1); err != nil {
			return err
		}
		if err := state.addAfterCommit(golem.HookCreate, fixture.userDescriptor.Metadata().ModelID(), func(ctx context.Context) error {
			callbacks.Add(1)
			var count int
			if err := fixture.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "users" WHERE "id"=?`, "00000000-0000-0000-0000-000000000073"); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("after-commit observed count %d", count)
			}
			return sentinel
		}); err != nil {
			return err
		}
		if caller.executor.invalidationEpoch() != beforeEpoch || callbacks.Load() != 0 || reports.Load() != 0 {
			t.Fatal("post-commit work ran before commit")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("committed success became an error: %v", err)
	}
	if caller.executor.invalidationEpoch() != beforeEpoch+1 || callbacks.Load() != 1 || reports.Load() != 1 {
		t.Fatalf("post-commit epoch=%d callback=%d report=%d", caller.executor.invalidationEpoch(), callbacks.Load(), reports.Load())
	}

	err = CallerTransaction(context.Background(), caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		state, err := transaction.caller.executor.mutationState()
		if err != nil {
			return err
		}
		if err := state.addAfterCommit(golem.HookCreate, fixture.userDescriptor.Metadata().ModelID(), func(context.Context) error { callbacks.Add(1); return nil }); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil || caller.executor.invalidationEpoch() != beforeEpoch+1 || callbacks.Load() != 1 {
		t.Fatalf("rollback post-commit err=%v epoch=%d callback=%d", err, caller.executor.invalidationEpoch(), callbacks.Load())
	}
}

func TestAfterCommitConfigurationRequiresFailureHandler(t *testing.T) {
	model := golem.ModelID{1}
	hook := golem.GeneratedAfterCommitHookBinding[testActor, testUser, golem.CreateHookResult[testUser]](model, golem.HookCreate, func(context.Context, golem.CreateHookResult[testUser]) error { return nil })
	pkg := golem.GeneratedStampedPackageBindings[testActor](golem.SchemaDigest{1}, nil, []golem.HookBinding[testActor]{hook})
	bindings, err := golem.GeneratedApplicationBindings(golem.SchemaDigest{1}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAfterCommitHandler(bindings, nil); err == nil {
		t.Fatal("after-commit hook without failure handler was accepted")
	}
	if err := validateAfterCommitHandler(bindings, func(context.Context, golem.AfterCommitFailure) {}); err != nil {
		t.Fatalf("configured failure handler rejected: %v", err)
	}
}

func TestAfterCommitHookAndReporterPanicsAreContained(t *testing.T) {
	limits, err := normalizeMutationLimits(MutationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := newMutationState(limits, mutationfact.CausationID{9})
	if err != nil {
		t.Fatal(err)
	}
	model := golem.ModelID{1}
	var second atomic.Int64
	if err := state.addAfterCommit(golem.HookCreate, model, func(context.Context) error { panic("hook panic") }); err != nil {
		t.Fatal(err)
	}
	if err := state.addAfterCommit(golem.HookCreate, model, func(context.Context) error { second.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	state.committed(context.Background(), func(context.Context, golem.AfterCommitFailure) { panic("reporter panic") })
	if second.Load() != 1 {
		t.Fatal("a panicking after-commit hook or reporter stopped later committed work")
	}
}

func TestSystemTransactionInvalidationEpochAdvancesOnlyOnDirtyCommit(t *testing.T) {
	fixture := openTransactionFixture(t)
	system := fixture.app.System()
	before := system.executor.invalidationEpoch()
	if err := SystemTransaction(context.Background(), system, func(transaction *SystemTx[testPrincipal, testActor]) error {
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		return state.touch(1)
	}); err != nil {
		t.Fatal(err)
	}
	if system.executor.invalidationEpoch() != before+1 {
		t.Fatalf("system commit epoch=%d want=%d", system.executor.invalidationEpoch(), before+1)
	}
	sentinel := errors.New("rollback")
	if err := SystemTransaction(context.Background(), system, func(transaction *SystemTx[testPrincipal, testActor]) error {
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		if err := state.touch(1); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	if system.executor.invalidationEpoch() != before+1 {
		t.Fatalf("system rollback changed epoch=%d", system.executor.invalidationEpoch())
	}
}

func runtimeStateRow(state *mutationState, ordinal int64, metadata []byte) mutationfact.OutboxRow {
	return mutationfact.OutboxRow{
		EventID:               fmt.Sprintf("00000000-0000-0000-0000-%012d", ordinal),
		FactVersion:           int64(mutationfact.FormatVersion),
		CodecIdentity:         mutationfact.CodecIdentity,
		GenerationFingerprint: fmt.Sprintf("%064d", 1),
		ModelID:               fmt.Sprintf("%032d", 1),
		Action:                "created",
		AfterIdentity:         []byte{byte(ordinal)},
		CausationID:           formatMutationUUID(state.causation),
		TransactionOrdinal:    ordinal,
		Metadata:              append([]byte(nil), metadata...),
		RecordedAt:            time.Unix(1_700_000_000+ordinal, 123_456_789),
	}
}
