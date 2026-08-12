package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationupsert "github.com/eleven-am/golem/go/internal/mutation/upsert"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

type optimisticConcurrencyAPIRow struct{}

type optimisticConcurrencyCleanupFailureQueryer struct {
	sqlx.QueryerContext
	execer sqlx.ExecerContext
	fail   error
	fired  atomic.Bool
}

func (queryer *optimisticConcurrencyCleanupFailureQueryer) ExecContext(ctx context.Context, statement string, arguments ...any) (sql.Result, error) {
	if strings.HasPrefix(statement, "ROLLBACK TO SAVEPOINT") && queryer.fired.CompareAndSwap(false, true) {
		return nil, queryer.fail
	}
	return queryer.execer.ExecContext(ctx, statement, arguments...)
}

func TestOptimisticConcurrencyRuntimeABIIsTypedAndExplicit(t *testing.T) {
	var callerUpdate func(context.Context, *Caller[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ExistingVersion, golem.UpdateInput[optimisticConcurrencyAPIRow], ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = CallerUpdateVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	var callerDelete func(context.Context, *Caller[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ExistingVersion, ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = CallerDeleteVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	var callerUpsert func(context.Context, *Caller[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ConcurrencyExpectation, golem.CreateInput[optimisticConcurrencyAPIRow], golem.UpdateInput[optimisticConcurrencyAPIRow], ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = CallerUpsertVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	var systemUpdate func(context.Context, System[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ExistingVersion, golem.UpdateInput[optimisticConcurrencyAPIRow], ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = SystemUpdateVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	var systemDelete func(context.Context, System[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ExistingVersion, ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = SystemDeleteVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	var systemUpsert func(context.Context, System[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ConcurrencyExpectation, golem.CreateInput[optimisticConcurrencyAPIRow], golem.UpdateInput[optimisticConcurrencyAPIRow], ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = SystemUpsertVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	var callerTxUpsert func(context.Context, *CallerTx[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ConcurrencyExpectation, golem.CreateInput[optimisticConcurrencyAPIRow], golem.UpdateInput[optimisticConcurrencyAPIRow], ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = CallerTxUpsertVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	var systemTxUpsert func(context.Context, *SystemTx[struct{}, struct{}], golem.ModelDescriptor[optimisticConcurrencyAPIRow], golem.MutationTarget[optimisticConcurrencyAPIRow], golem.ConcurrencyExpectation, golem.CreateInput[optimisticConcurrencyAPIRow], golem.UpdateInput[optimisticConcurrencyAPIRow], ...golem.Projection[optimisticConcurrencyAPIRow]) (golem.Row[optimisticConcurrencyAPIRow], error) = SystemTxUpsertVersioned[struct{}, struct{}, optimisticConcurrencyAPIRow]
	if callerUpdate == nil || callerDelete == nil || callerUpsert == nil || systemUpdate == nil || systemDelete == nil || systemUpsert == nil || callerTxUpsert == nil || systemTxUpsert == nil {
		t.Fatal("typed optimistic-concurrency runtime ABI is absent")
	}
}

func TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite(t *testing.T) {
	ctx := context.Background()
	schema := schematest.NewOptimisticConcurrency(t)
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "optimistic-concurrency.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	fixture := openMutationVocabularyFixture(t, database, golem.SQLite, schema)
	system := fixture.app.System()
	user := golem.GeneratedCreateInput[mutationResultUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "alice"),
	)
	if _, err := SystemCreate(ctx, system, fixture.userDescriptor, user); err != nil {
		t.Fatal(err)
	}
	decimal, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	decimalField := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal)
	createPost := func(id byte, title string, extra ...golem.CreateValue[mutationResultPost]) golem.CreateInput[mutationResultPost] {
		values := []golem.CreateValue[mutationResultPost]{
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.authorID, golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.title, title),
			golem.GeneratedCreateFieldValue(schema.Post, decimalField, decimal),
		}
		return golem.GeneratedCreateInput(schema.Post, append(values, extra...)...)
	}
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(1, "one")); err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	assertOptimisticConcurrencyRow(t, database, 1, "one", 1)

	if _, err := SystemUpdateVersioned(ctx, system, fixture.postDescriptor, fixture.target(1), golem.ExpectVersion(1), fixture.updateTitle("two")); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 1, "two", 2)

	_, err = SystemUpdateVersioned(ctx, system, fixture.postDescriptor, fixture.target(1), golem.ExpectVersion(1), fixture.updateTitle("stale"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	assertOptimisticConcurrencyRow(t, database, 1, "two", 2)

	_, err = SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(1), fixture.updateTitle("legacy"))
	assertOptimisticConcurrencyError(t, err, golem.CodeBadUserInput, "mutation request is invalid")
	assertOptimisticConcurrencyRow(t, database, 1, "two", 2)

	authored := createPost(2, "forged",
		golem.GeneratedCreateFieldValue(schema.Post, fixture.bigInt, int64(77)),
	)
	_, err = SystemCreate(ctx, system, fixture.postDescriptor, authored)
	assertOptimisticConcurrencyError(t, err, golem.CodeBadUserInput, "mutation request is invalid")
	var forged int
	if err := database.GetContext(ctx, &forged, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(2)); err != nil || forged != 0 {
		t.Fatalf("forged create rows=%d err=%v", forged, err)
	}

	if _, err := SystemDeleteVersioned(ctx, system, fixture.postDescriptor, fixture.target(1), golem.ExpectVersion(2)); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := database.GetContext(ctx, &remaining, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(1)); err != nil || remaining != 0 {
		t.Fatalf("remaining rows=%d err=%v", remaining, err)
	}

	if _, err := SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(2), golem.ExpectAbsent(), createPost(2, "created"), fixture.updateTitle("unused")); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 2, "created", 1)
	_, err = SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(2), golem.ExpectAbsent(), createPost(2, "again"), fixture.updateTitle("unused"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	assertOptimisticConcurrencyRow(t, database, 2, "created", 1)
	if _, err := SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(2), golem.ExpectExisting(1), createPost(2, "unused"), fixture.updateTitle("updated")); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 2, "updated", 2)
	_, err = SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(2), golem.ExpectExisting(1), createPost(2, "unused"), fixture.updateTitle("stale"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	_, err = SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(3), golem.ExpectExisting(1), createPost(3, "unused"), fixture.updateTitle("missing"))
	assertOptimisticConcurrencyError(t, err, golem.CodeNotFound, "record not found")
	_, err = SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(3), golem.ExpectAbsent(), createPost(4, "mismatch"), fixture.updateTitle("unused"))
	assertOptimisticConcurrencyError(t, err, golem.CodeBadUserInput, "mutation request is invalid")

	var createBefore, createAfter, createCommit, updateBefore, updateAfter, updateCommit, deleteBefore atomic.Int64
	updatedField := golem.GeneratedEqualField[mutationResultPost, time.Time](schema.PostDateTime)
	var updateValuesMu sync.Mutex
	updateValues := make([]time.Time, 0)
	var retargetCreate, retargetUpdate, addOptionalUpdate atomic.Bool
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.Or(fixture.title.Eq("visible"), fixture.title.Eq("grant")))
		rules.CanDelete(golem.All[mutationResultPost]())
		rules.CannotReadFields(golem.All[mutationResultPost](), fixture.bigInt)
		rules.CanReadFields(fixture.title.Eq("token-readable"), fixture.bigInt)
		rules.CannotUpdateFields(golem.All[mutationResultPost](), fixture.optionalInt)
		rules.CanUpdateFields(fixture.title.Eq("grant"), fixture.optionalInt)
		return rules.Freeze(schema.Post)
	})
	hooks := []golem.HookBinding[mutationResultActor]{
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
			createBefore.Add(1)
			if retargetCreate.Load() {
				request.ReplaceInput(createPost(9, "retargeted"))
			}
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			createAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			createCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
			updateBefore.Add(1)
			if retargetUpdate.Load() {
				request.ReplaceTarget(fixture.target(6))
			}
			if addOptionalUpdate.Load() {
				request.ReplaceInput(golem.GeneratedUpdateInput(schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, fixture.title, "grant"),
					golem.GeneratedSetFieldValue(schema.Post, fixture.optionalInt, int64(5)),
				))
			}
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, result golem.UpdateHookResult[mutationResultPost]) error {
			updateAfter.Add(1)
			updated, present := golem.Value(result.After(), updatedField).Get()
			if !present {
				return errors.New("runtime-owned update timestamp is absent from hook result")
			}
			updateValuesMu.Lock()
			updateValues = append(updateValues, updated.UTC())
			updateValuesMu.Unlock()
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
			updateCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.DeleteHookRequest[mutationResultPost]](schema.Post, golem.HookDelete, func(context.Context, *golem.DeleteHookRequest[mutationResultPost]) error {
			deleteBefore.Add(1)
			return nil
		}),
	}
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app.bindings = bindings
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(6, "hidden")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(7, "visible")); err != nil {
		t.Fatal(err)
	}
	_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(6), golem.ExpectAbsent(), createPost(6, "replacement"), fixture.updateTitle("visible"))
	assertOptimisticConcurrencyError(t, err, golem.CodeNotFound, "record not found")
	_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(6), golem.ExpectExisting(1), createPost(6, "unused"), fixture.updateTitle("visible"))
	assertOptimisticConcurrencyError(t, err, golem.CodeNotFound, "record not found")
	_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(7), golem.ExpectExisting(2), createPost(7, "unused"), fixture.updateTitle("visible"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	if updateBefore.Load() != 0 || createBefore.Load() != 0 {
		t.Fatalf("refused upsert hooks create=%d update=%d, want zero", createBefore.Load(), updateBefore.Load())
	}
	row, err := CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(7), golem.ExpectExisting(1), createPost(7, "unused"), fixture.updateTitle("visible"), golem.Select[mutationResultPost](fixture.title, fixture.bigInt))
	if err != nil {
		t.Fatal(err)
	}
	maskedToken := golem.Value(row, fixture.bigInt)
	if !maskedToken.IsSelected() || !maskedToken.IsNull() {
		t.Fatalf("masked concurrency token state=%d, want selected-null", maskedToken.State())
	}
	if updateBefore.Load() != 1 {
		t.Fatalf("successful update Before count=%d, want one", updateBefore.Load())
	}
	retargetBefore, retargetAfter, retargetCommit := updateBefore.Load(), updateAfter.Load(), updateCommit.Load()
	retargetUpdate.Store(true)
	_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(7), golem.ExpectExisting(2), createPost(7, "unused"), fixture.updateTitle("visible"))
	assertOptimisticConcurrencyError(t, err, golem.CodeBadUserInput, "mutation hook rejected the operation")
	retargetUpdate.Store(false)
	if updateBefore.Load() != retargetBefore+1 || updateAfter.Load() != retargetAfter || updateCommit.Load() != retargetCommit {
		t.Fatalf("retargeted update hooks before=%d/%d after=%d/%d commit=%d/%d", updateBefore.Load(), retargetBefore+1, updateAfter.Load(), retargetAfter, updateCommit.Load(), retargetCommit)
	}
	assertOptimisticConcurrencyRow(t, database, 7, "visible", 2)
	retargetCreate.Store(true)
	_, err = CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(8), golem.ExpectAbsent(), createPost(8, "created"), fixture.updateTitle("unused"))
	assertOptimisticConcurrencyError(t, err, golem.CodeBadUserInput, "mutation hook rejected the operation")
	if createBefore.Load() != 1 || createAfter.Load() != 0 || createCommit.Load() != 0 {
		t.Fatalf("retargeted create hooks=%d/%d/%d", createBefore.Load(), createAfter.Load(), createCommit.Load())
	}
	retargetCreate.Store(false)
	if _, err := CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(8), golem.ExpectAbsent(), createPost(8, "created"), fixture.updateTitle("unused")); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 8, "created", 1)
	if createBefore.Load() != 2 || createAfter.Load() != 1 || createCommit.Load() != 1 {
		t.Fatalf("successful create hooks=%d/%d/%d", createBefore.Load(), createAfter.Load(), createCommit.Load())
	}
	rollback := errors.New("rollback optimistic-concurrency transaction")
	err = SystemTransaction(ctx, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := SystemTxUpsertVersioned(ctx, transaction, fixture.postDescriptor, fixture.target(8), golem.ExpectExisting(1), createPost(8, "unused"), fixture.updateTitle("rolled-back")); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("system transaction rollback error=%v", err)
	}
	assertOptimisticConcurrencyRow(t, database, 8, "created", 1)
	if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		_, err := CallerTxUpsertVersioned(ctx, transaction, fixture.postDescriptor, fixture.target(7), golem.ExpectExisting(2), createPost(7, "unused"), fixture.updateTitle("visible"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 7, "visible", 3)
	err = SystemTransaction(ctx, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := SystemTxUpsertVersioned(ctx, transaction, fixture.postDescriptor, fixture.target(11), golem.ExpectAbsent(), createPost(11, "rolled-back-absent"), fixture.updateTitle("unused")); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("system expect-absent transaction rollback error=%v", err)
	}
	var txAbsentRows int
	if err := database.GetContext(ctx, &txAbsentRows, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(11)); err != nil || txAbsentRows != 0 {
		t.Fatalf("rolled-back expect-absent rows=%d err=%v", txAbsentRows, err)
	}
	if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		_, err := CallerTxUpsertVersioned(ctx, transaction, fixture.postDescriptor, fixture.target(12), golem.ExpectAbsent(), createPost(12, "visible"), fixture.updateTitle("unused"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 12, "visible", 1)
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(10, "exhausted")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE "posts" SET "big_int" = ? WHERE "id" = ?`, int64(math.MaxInt64), mutationResultUUIDText(10)); err != nil {
		t.Fatal(err)
	}
	_, err = SystemUpdateVersioned(ctx, system, fixture.postDescriptor, fixture.target(10), golem.ExpectVersion(math.MaxInt64), fixture.updateTitle("overflow"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	_, err = SystemDeleteVersioned(ctx, system, fixture.postDescriptor, fixture.target(10), golem.ExpectVersion(math.MaxInt64))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	assertOptimisticConcurrencyRow(t, database, 10, "exhausted", math.MaxInt64)
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(13, "visible")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE "posts" SET "big_int" = ? WHERE "id" = ?`, int64(math.MaxInt64), mutationResultUUIDText(13)); err != nil {
		t.Fatal(err)
	}
	overflowCollector := &p8ObservationCollector{}
	fixture.app.observer = overflowCollector
	overflowUpdateBefore, overflowDeleteBefore := updateBefore.Load(), deleteBefore.Load()
	_, err = CallerUpdateVersioned(ctx, caller, fixture.postDescriptor, fixture.target(13), golem.ExpectVersion(math.MaxInt64), fixture.updateTitle("overflow"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	_, err = CallerDeleteVersioned(ctx, caller, fixture.postDescriptor, fixture.target(13), golem.ExpectVersion(math.MaxInt64))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	if updateBefore.Load() != overflowUpdateBefore || deleteBefore.Load() != overflowDeleteBefore {
		t.Fatalf("overflow invoked Before hooks update=%d/%d delete=%d/%d", updateBefore.Load(), overflowUpdateBefore, deleteBefore.Load(), overflowDeleteBefore)
	}
	overflowUpdates := overflowCollector.matching(observe.KindMutation, observe.OperationMutationUpdate)
	overflowDeletes := overflowCollector.matching(observe.KindMutation, observe.OperationMutationDelete)
	if len(overflowUpdates) != 1 || overflowUpdates[0].statements != 1 || overflowUpdates[0].reason != observe.ReasonConflict || len(overflowDeletes) != 1 || overflowDeletes[0].statements != 1 || overflowDeletes[0].reason != observe.ReasonConflict {
		t.Fatalf("overflow observations update=%+v delete=%+v", overflowUpdates, overflowDeletes)
	}
	fixture.app.observer = nil
	assertOptimisticConcurrencyRow(t, database, 13, "visible", math.MaxInt64)
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(14, "grant")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(15, "visible")); err != nil {
		t.Fatal(err)
	}
	grantCollector := &p8ObservationCollector{}
	fixture.app.observer = grantCollector
	grantBefore, grantAfter, grantCommit := updateBefore.Load(), updateAfter.Load(), updateCommit.Load()
	addOptionalUpdate.Store(true)
	if _, err := CallerUpdateVersioned(ctx, caller, fixture.postDescriptor, fixture.target(14), golem.ExpectVersion(1), fixture.updateTitle("grant")); err != nil {
		t.Fatalf("hook-added allowed update: %v: %v", err, errors.Unwrap(err))
	}
	_, err = CallerUpdateVersioned(ctx, caller, fixture.postDescriptor, fixture.target(15), golem.ExpectVersion(1), fixture.updateTitle("visible"))
	assertOptimisticConcurrencyError(t, err, golem.CodeForbidden, "mutation is not authorized")
	addOptionalUpdate.Store(false)
	if updateBefore.Load() != grantBefore+2 || updateAfter.Load() != grantAfter+1 || updateCommit.Load() != grantCommit+1 {
		t.Fatalf("hook-added field hooks before=%d/%d after=%d/%d commit=%d/%d", updateBefore.Load(), grantBefore+2, updateAfter.Load(), grantAfter+1, updateCommit.Load(), grantCommit+1)
	}
	var allowedOptional *int64
	if err := database.GetContext(ctx, &allowedOptional, `SELECT "optional_int" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(14)); err != nil || allowedOptional == nil || *allowedOptional != 5 {
		t.Fatalf("allowed hook-added field=%v err=%v", allowedOptional, err)
	}
	var deniedOptional *int64
	if err := database.GetContext(ctx, &deniedOptional, `SELECT "optional_int" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(15)); err != nil || deniedOptional != nil {
		t.Fatalf("denied hook-added field=%v err=%v", deniedOptional, err)
	}
	grantObservations := grantCollector.matching(observe.KindMutation, observe.OperationMutationUpdate)
	if len(grantObservations) != 2 || grantObservations[0].statements != 5 || grantObservations[0].outcome != observe.OutcomeSuccess || grantObservations[1].statements != 3 || grantObservations[1].outcome != observe.OutcomeRefused {
		t.Fatalf("hook-added field observations=%+v", grantObservations)
	}
	fixture.app.observer = nil

	var factsBeforeFaults int
	if err := database.GetContext(ctx, &factsBeforeFaults, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil {
		t.Fatal(err)
	}
	for index, stage := range []absentUpsertFaultStage{
		absentUpsertFaultBegin, absentUpsertFaultGuard, absentUpsertFaultAuthorizedProbe,
		absentUpsertFaultExactProbe, absentUpsertFaultBeforeCreate, absentUpsertFaultCleanup, absentUpsertFaultFinish,
	} {
		var calls atomic.Int64
		faultContext := contextWithAbsentUpsertFault(ctx, func(got absentUpsertFaultStage, _ *sqlxUpsertAttempt) error {
			if got != stage {
				return nil
			}
			calls.Add(1)
			return &pgconn.PgError{Code: "40001", Message: "deterministic trusted interference"}
		})
		id := byte(20 + index)
		_, err := CallerUpsertVersioned(faultContext, caller, fixture.postDescriptor, fixture.target(id), golem.ExpectAbsent(), createPost(id, "fault"), fixture.updateTitle("unused"))
		assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
		wantCalls := int64(fixture.app.mutationLimits.upsertAttempts)
		if calls.Load() != wantCalls {
			t.Fatalf("fault stage %d calls=%d, want bounded provider retries=%d", stage, calls.Load(), wantCalls)
		}
		var count int
		if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(id)); err != nil || count != 0 {
			t.Fatalf("fault stage %d rows=%d err=%v", stage, count, err)
		}
	}
	var factsAfterFaults int
	if err := database.GetContext(ctx, &factsAfterFaults, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || factsAfterFaults != factsBeforeFaults {
		t.Fatalf("interference facts=%d want=%d err=%v", factsAfterFaults, factsBeforeFaults, err)
	}

	// One logical upsert owns one frozen preparation and one root observation,
	// even when trusted provider interference forces a complete second attempt.
	retryCollector := &p8ObservationCollector{}
	fixture.app.observer = retryCollector
	var retryOrdinalsMu sync.Mutex
	retryOrdinals := make([]uint32, 0, 2)
	retryContext := contextWithUpsertAttemptFinishFault(ctx, func(ordinal uint32) error {
		retryOrdinalsMu.Lock()
		retryOrdinals = append(retryOrdinals, ordinal)
		retryOrdinalsMu.Unlock()
		if ordinal == 1 {
			time.Sleep(2 * time.Millisecond)
			return &pgconn.PgError{Code: "40001", Message: "first optimistic upsert attempt must replay"}
		}
		return nil
	})
	updateBeforeRetry, updateAfterRetry, updateCommitRetry := updateBefore.Load(), updateAfter.Load(), updateCommit.Load()
	updateValuesMu.Lock()
	updateValuesBeforeRetry := len(updateValues)
	updateValuesMu.Unlock()
	if _, err := CallerUpsertVersioned(retryContext, caller, fixture.postDescriptor, fixture.target(7), golem.ExpectExisting(3), createPost(7, "unused"), fixture.updateTitle("visible")); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 7, "visible", 4)
	if updateBefore.Load() != updateBeforeRetry+2 || updateAfter.Load() != updateAfterRetry+2 || updateCommit.Load() != updateCommitRetry+1 {
		t.Fatalf("existing retry hooks before=%d/%d after=%d/%d commit=%d/%d", updateBefore.Load(), updateBeforeRetry+2, updateAfter.Load(), updateAfterRetry+2, updateCommit.Load(), updateCommitRetry+1)
	}
	updateValuesMu.Lock()
	if len(updateValues) != updateValuesBeforeRetry+2 || !updateValues[updateValuesBeforeRetry].Equal(updateValues[updateValuesBeforeRetry+1]) {
		t.Fatalf("existing retry runtime Updated values=%v from=%d", updateValues, updateValuesBeforeRetry)
	}
	wantExistingUpdatedMicros := updateValues[updateValuesBeforeRetry].UnixMicro()
	updateValuesMu.Unlock()
	var storedExistingUpdatedMicros int64
	if err := database.GetContext(ctx, &storedExistingUpdatedMicros, `SELECT "datetime_value" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(7)); err != nil || storedExistingUpdatedMicros != wantExistingUpdatedMicros {
		t.Fatalf("stored existing-retry Updated=%d want=%d err=%v", storedExistingUpdatedMicros, wantExistingUpdatedMicros, err)
	}
	retryOrdinalsMu.Lock()
	if len(retryOrdinals) != 2 || retryOrdinals[0] != 1 || retryOrdinals[1] != 2 {
		t.Fatalf("existing retry ordinals=%v, want [1 2]", retryOrdinals)
	}
	retryOrdinalsMu.Unlock()
	existingRetryObservations := retryCollector.matching(observe.KindMutation, observe.OperationMutationUpsert)
	if len(existingRetryObservations) != 1 || existingRetryObservations[0].outcome != observe.OutcomeSuccess || existingRetryObservations[0].statements != 8 {
		t.Fatalf("existing retry observations=%+v", existingRetryObservations)
	}

	retryCollector = &p8ObservationCollector{}
	fixture.app.observer = retryCollector
	retryOrdinals = retryOrdinals[:0]
	createBeforeRetry, createAfterRetry, createCommitRetry := createBefore.Load(), createAfter.Load(), createCommit.Load()
	var attemptUpdatedMu sync.Mutex
	attemptUpdated := make([]int64, 0, 2)
	retryContext = contextWithAbsentUpsertFault(retryContext, func(stage absentUpsertFaultStage, attempt *sqlxUpsertAttempt) error {
		if stage != absentUpsertFaultCleanup {
			return nil
		}
		var value int64
		if err := attempt.queryer.QueryRowxContext(ctx, `SELECT "datetime_value" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(28)).Scan(&value); err != nil {
			return err
		}
		attemptUpdatedMu.Lock()
		attemptUpdated = append(attemptUpdated, value)
		attemptUpdatedMu.Unlock()
		return nil
	})
	if _, err := CallerUpsertVersioned(retryContext, caller, fixture.postDescriptor, fixture.target(28), golem.ExpectAbsent(), createPost(28, "visible"), fixture.updateTitle("unused")); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 28, "visible", 1)
	if createBefore.Load() != createBeforeRetry+2 || createAfter.Load() != createAfterRetry+2 || createCommit.Load() != createCommitRetry+1 {
		t.Fatalf("absent retry hooks before=%d/%d after=%d/%d commit=%d/%d", createBefore.Load(), createBeforeRetry+2, createAfter.Load(), createAfterRetry+2, createCommit.Load(), createCommitRetry+1)
	}
	attemptUpdatedMu.Lock()
	if len(attemptUpdated) != 2 || attemptUpdated[0] != attemptUpdated[1] {
		t.Fatalf("retry runtime Updated values=%v", attemptUpdated)
	}
	wantUpdatedMicros := attemptUpdated[0]
	attemptUpdatedMu.Unlock()
	var storedUpdatedMicros int64
	if err := database.GetContext(ctx, &storedUpdatedMicros, `SELECT "datetime_value" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(28)); err != nil || storedUpdatedMicros != wantUpdatedMicros {
		t.Fatalf("stored retry Updated=%d want=%d err=%v", storedUpdatedMicros, wantUpdatedMicros, err)
	}
	retryOrdinalsMu.Lock()
	if len(retryOrdinals) != 2 || retryOrdinals[0] != 1 || retryOrdinals[1] != 2 {
		t.Fatalf("absent retry ordinals=%v, want [1 2]", retryOrdinals)
	}
	retryOrdinalsMu.Unlock()
	absentRetryObservations := retryCollector.matching(observe.KindMutation, observe.OperationMutationUpsert)
	if len(absentRetryObservations) != 1 || absentRetryObservations[0].outcome != observe.OutcomeSuccess || absentRetryObservations[0].statements != 18 {
		t.Fatalf("absent retry observations=%+v", absentRetryObservations)
	}
	fixture.app.observer = nil
	var factsAfterRetries int
	if err := database.GetContext(ctx, &factsAfterRetries, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || factsAfterRetries != factsAfterFaults+2 {
		t.Fatalf("retry facts=%d want=%d err=%v", factsAfterRetries, factsAfterFaults+2, err)
	}

	// Deterministic uniqueness is a terminal data conflict, never a provider
	// retry. An abort failure is also terminal and cannot be hidden behind the
	// otherwise clean conflict from the failed attempt.
	var uniqueFinishCalls atomic.Int64
	uniqueContext := contextWithUpsertAttemptFinishFault(ctx, func(uint32) error {
		uniqueFinishCalls.Add(1)
		return &pgconn.PgError{Code: "23505", Message: "deterministic unique violation"}
	})
	_, err = CallerUpsertVersioned(uniqueContext, caller, fixture.postDescriptor, fixture.target(7), golem.ExpectExisting(4), createPost(7, "unused"), fixture.updateTitle("visible"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	if uniqueFinishCalls.Load() != 1 {
		t.Fatalf("unique existing-upsert finish calls=%d, want one", uniqueFinishCalls.Load())
	}
	assertOptimisticConcurrencyRow(t, database, 7, "visible", 4)

	var abortFaultCalls atomic.Int64
	abortSentinel := errors.New("deterministic optimistic-concurrency abort failure")
	abortContext := contextWithAbsentUpsertFault(ctx, func(stage absentUpsertFaultStage, attempt *sqlxUpsertAttempt) error {
		if stage != absentUpsertFaultGuard {
			return nil
		}
		abortFaultCalls.Add(1)
		original := attempt.abort
		attempt.abort = func() error { return errors.Join(original(), abortSentinel) }
		return &pgconn.PgError{Code: "40001", Message: "abort must succeed before retry"}
	})
	_, err = CallerUpsertVersioned(abortContext, caller, fixture.postDescriptor, fixture.target(29), golem.ExpectAbsent(), createPost(29, "visible"), fixture.updateTitle("unused"))
	assertOptimisticConcurrencyError(t, err, golem.CodeBadUserInput, "mutation could not be completed")
	if abortFaultCalls.Load() != 1 || !errors.Is(err, abortSentinel) {
		t.Fatalf("abort-failure calls=%d error=%v", abortFaultCalls.Load(), err)
	}

	var scopedFaultCalls atomic.Int64
	scopedFaultContext := contextWithUpsertAttemptFinishFault(ctx, func(uint32) error {
		scopedFaultCalls.Add(1)
		return &pgconn.PgError{Code: "40001", Message: "scoped upsert cannot replay"}
	})
	err = CallerTransaction(scopedFaultContext, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		_, err := CallerTxUpsertVersioned(scopedFaultContext, transaction, fixture.postDescriptor, fixture.target(29), golem.ExpectAbsent(), createPost(29, "visible"), fixture.updateTitle("unused"))
		return err
	})
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	if scopedFaultCalls.Load() != 1 {
		t.Fatalf("scoped upsert finish calls=%d, want one", scopedFaultCalls.Load())
	}
	var scopedRows int
	if err := database.GetContext(ctx, &scopedRows, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(29)); err != nil || scopedRows != 0 {
		t.Fatalf("scoped fault rows=%d err=%v", scopedRows, err)
	}
	var systemScopedFaultCalls atomic.Int64
	systemScopedFaultContext := contextWithUpsertAttemptFinishFault(ctx, func(uint32) error {
		systemScopedFaultCalls.Add(1)
		return &pgconn.PgError{Code: "40001", Message: "system transaction upsert cannot replay"}
	})
	err = SystemTransaction(systemScopedFaultContext, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		_, err := SystemTxUpsertVersioned(systemScopedFaultContext, transaction, fixture.postDescriptor, fixture.target(29), golem.ExpectAbsent(), createPost(29, "visible"), fixture.updateTitle("unused"))
		return err
	})
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	if systemScopedFaultCalls.Load() != 1 {
		t.Fatalf("system scoped upsert finish calls=%d, want one", systemScopedFaultCalls.Load())
	}
	if err := database.GetContext(ctx, &scopedRows, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(29)); err != nil || scopedRows != 0 {
		t.Fatalf("system scoped fault rows=%d err=%v", scopedRows, err)
	}
	connection, err := database.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	manualBinding := scopedExecution(database, connection)
	if err := manualBinding.enableMutation(mutationConfig(fixture.app, manualBinding)); err != nil {
		_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		manualBinding.close()
		_ = connection.Close()
		t.Fatal(err)
	}
	manualSystem := System[mutationResultPrincipal, mutationResultActor]{app: fixture.app, executor: manualBinding}
	var manualBegins atomic.Int64
	manualContext := contextWithAbsentUpsertFault(ctx, func(stage absentUpsertFaultStage, _ *sqlxUpsertAttempt) error {
		if stage == absentUpsertFaultBegin {
			manualBegins.Add(1)
		}
		return nil
	})
	manualContext = contextWithUpsertAttemptFinishFault(manualContext, func(uint32) error {
		return &pgconn.PgError{Code: "40001", Message: "scoped nil-transaction binding cannot replay"}
	})
	_, err = SystemUpsertVersioned(manualContext, manualSystem, fixture.postDescriptor, fixture.target(29), golem.ExpectAbsent(), createPost(29, "visible"), fixture.updateTitle("unused"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	if manualBegins.Load() != 1 {
		t.Fatalf("scoped nil-transaction attempt begins=%d, want one", manualBegins.Load())
	}
	_, rollbackErr := connection.ExecContext(context.Background(), "ROLLBACK")
	manualBinding.discardMutation()
	manualBinding.close()
	closeErr := connection.Close()
	if rollbackErr != nil || closeErr != nil {
		t.Fatalf("scoped nil-transaction cleanup rollback=%v close=%v", rollbackErr, closeErr)
	}
	if err := database.GetContext(ctx, &scopedRows, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(29)); err != nil || scopedRows != 0 {
		t.Fatalf("manual scoped fault rows=%d err=%v", scopedRows, err)
	}

	cleanupSentinel := errors.New("deterministic inner savepoint rollback failure")
	var cleanupFaultCalls atomic.Int64
	cleanupContext := contextWithAbsentUpsertFault(ctx, func(stage absentUpsertFaultStage, attempt *sqlxUpsertAttempt) error {
		if stage != absentUpsertFaultBeforeCreate {
			return nil
		}
		cleanupFaultCalls.Add(1)
		execer, ok := attempt.queryer.(sqlx.ExecerContext)
		if !ok {
			return errors.New("cleanup-failure queryer cannot execute")
		}
		if _, err := execer.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title","big_int","decimal_value") VALUES (?,?,?,?,?)`, mutationResultUUIDText(27), mutationResultUUIDText(1), "visible", int64(1), int64(12_500_000_000_000)); err != nil {
			return err
		}
		attempt.queryer = &optimisticConcurrencyCleanupFailureQueryer{QueryerContext: attempt.queryer, execer: execer, fail: cleanupSentinel}
		return nil
	})
	_, err = CallerUpsertVersioned(cleanupContext, caller, fixture.postDescriptor, fixture.target(27), golem.ExpectAbsent(), createPost(27, "loser"), fixture.updateTitle("unused"))
	var cleanupPublic *golem.Error
	if !errors.As(err, &cleanupPublic) || cleanupPublic.Code == golem.CodeConflict || cleanupPublic.Code == golem.CodeNotFound || !errors.Is(err, cleanupSentinel) {
		t.Fatalf("inner cleanup failure error=%v public=%#v", err, cleanupPublic)
	}
	if cleanupFaultCalls.Load() != 1 {
		t.Fatalf("inner cleanup failure calls=%d, want one", cleanupFaultCalls.Load())
	}
	var cleanupRows int
	if err := database.GetContext(ctx, &cleanupRows, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(27)); err != nil || cleanupRows != 0 {
		t.Fatalf("inner cleanup failure rows=%d err=%v", cleanupRows, err)
	}

	collisionBefore, collisionAfter, collisionCommit := createBefore.Load(), createAfter.Load(), createCommit.Load()
	for _, collision := range []struct {
		id      byte
		title   string
		code    golem.ErrorCode
		message string
	}{{30, "visible", golem.CodeConflict, "mutation conflicted"}, {31, "hidden", golem.CodeNotFound, "record not found"}} {
		var injected atomic.Int64
		collisionContext := contextWithAbsentUpsertFault(ctx, func(stage absentUpsertFaultStage, attempt *sqlxUpsertAttempt) error {
			if stage != absentUpsertFaultBeforeCreate {
				return nil
			}
			injected.Add(1)
			execer, ok := attempt.queryer.(sqlx.ExecerContext)
			if !ok {
				return errors.New("collision queryer cannot insert a winner")
			}
			_, err := execer.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title","big_int","decimal_value") VALUES (?,?,?,?,?)`, mutationResultUUIDText(collision.id), mutationResultUUIDText(1), collision.title, int64(1), int64(12_500_000_000_000))
			return err
		})
		_, err := CallerUpsertVersioned(collisionContext, caller, fixture.postDescriptor, fixture.target(collision.id), golem.ExpectAbsent(), createPost(collision.id, "loser"), fixture.updateTitle("unused"))
		assertOptimisticConcurrencyError(t, err, collision.code, collision.message)
		if injected.Load() != 1 {
			t.Fatalf("collision id=%d injections=%d, want one", collision.id, injected.Load())
		}
	}
	if createBefore.Load() != collisionBefore+2 || createAfter.Load() != collisionAfter || createCommit.Load() != collisionCommit {
		t.Fatalf("race-lost hooks before=%d/%d after=%d/%d commit=%d/%d", createBefore.Load(), collisionBefore+2, createAfter.Load(), collisionAfter, createCommit.Load(), collisionCommit)
	}
	var factsAfterCollisions int
	if err := database.GetContext(ctx, &factsAfterCollisions, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || factsAfterCollisions != factsAfterRetries {
		t.Fatalf("collision facts=%d want=%d err=%v", factsAfterCollisions, factsAfterRetries, err)
	}
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(32, "observed")); err != nil {
		t.Fatal(err)
	}
	collector := &p8ObservationCollector{}
	fixture.app.observer = collector
	if _, err := SystemUpdateVersioned(ctx, system, fixture.postDescriptor, fixture.target(32), golem.ExpectVersion(1), fixture.updateTitle("observed-update")); err != nil {
		t.Fatal(err)
	}
	_, err = SystemUpdateVersioned(ctx, system, fixture.postDescriptor, fixture.target(32), golem.ExpectVersion(1), fixture.updateTitle("stale"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	updates := collector.matching(observe.KindMutation, observe.OperationMutationUpdate)
	if len(updates) != 2 || updates[0].statements != 4 || updates[0].outcome != observe.OutcomeSuccess || updates[1].statements != 1 || updates[1].outcome != observe.OutcomeRefused || updates[1].reason != observe.ReasonConflict {
		t.Fatalf("CAS update observations=%+v", updates)
	}
	if _, err := SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(32), golem.ExpectExisting(2), createPost(32, "unused"), fixture.updateTitle("observed-upsert")); err != nil {
		t.Fatal(err)
	}
	_, err = SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(32), golem.ExpectExisting(2), createPost(32, "unused"), fixture.updateTitle("stale"))
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	upserts := collector.matching(observe.KindMutation, observe.OperationMutationUpsert)
	if len(upserts) != 2 || upserts[0].statements != 4 || upserts[0].outcome != observe.OutcomeSuccess || upserts[1].statements != 1 || upserts[1].reason != observe.ReasonConflict {
		t.Fatalf("CAS upsert observations=%+v", upserts)
	}
	var factsAfterObservations int
	if err := database.GetContext(ctx, &factsAfterObservations, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || factsAfterObservations != factsAfterCollisions+3 {
		// The seed create plus the two successful CAS writes each emit one fact;
		// both stale refusals emit none.
		t.Fatalf("observation facts=%d want=%d err=%v", factsAfterObservations, factsAfterCollisions+3, err)
	}
	fixture.app.observer = nil
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(40, "race")); err != nil {
		t.Fatal(err)
	}
	var factsBeforeUpdateRace int
	if err := database.GetContext(ctx, &factsBeforeUpdateRace, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for _, title := range []string{"winner-a", "winner-b"} {
		title := title
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			_, err := SystemUpdateVersioned(ctx, system, fixture.postDescriptor, fixture.target(40), golem.ExpectVersion(1), fixture.updateTitle(title))
			results <- err
		}()
	}
	close(start)
	writers.Wait()
	close(results)
	assertOneOptimisticConcurrencyWinner(t, results)
	var racedVersion int64
	if err := database.GetContext(ctx, &racedVersion, `SELECT "big_int" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(40)); err != nil || racedVersion != 2 {
		t.Fatalf("update race version=%d err=%v", racedVersion, err)
	}
	var factsAfterUpdateRace int
	if err := database.GetContext(ctx, &factsAfterUpdateRace, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || factsAfterUpdateRace != factsBeforeUpdateRace+1 {
		t.Fatalf("update race facts=%d want=%d err=%v", factsAfterUpdateRace, factsBeforeUpdateRace+1, err)
	}

	start = make(chan struct{})
	results = make(chan error, 2)
	writers = sync.WaitGroup{}
	for _, title := range []string{"create-a", "create-b"} {
		title := title
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			_, err := SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(41), golem.ExpectAbsent(), createPost(41, title), fixture.updateTitle("unused"))
			results <- err
		}()
	}
	close(start)
	writers.Wait()
	close(results)
	assertOneOptimisticConcurrencyWinner(t, results)
	assertOptimisticConcurrencyRowVersion(t, database, 41, 1)
	var factsAfterUpsertRace int
	if err := database.GetContext(ctx, &factsAfterUpsertRace, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || factsAfterUpsertRace != factsAfterUpdateRace+1 {
		t.Fatalf("upsert race facts=%d want=%d err=%v", factsAfterUpsertRace, factsAfterUpdateRace+1, err)
	}

	projection, err := golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany, Model: schema.Post,
		Selection:  []golem.RuntimeReadSelectionInput{{Kind: golem.RuntimeReadScalar, Field: schema.PostTitle}},
		Projection: golem.ProjectionSelect,
	})
	if err != nil {
		t.Fatal(err)
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(fixture.target(40))
	if err != nil {
		t.Fatal(err)
	}
	frozenUpdate, err := golem.RuntimeFreezeUpdateInput(fixture.updateTitle("forged-frozen"))
	if err != nil {
		t.Fatal(err)
	}
	frozenCreate, err := golem.RuntimeFreezeCreateInput(createPost(40, "forged-frozen"))
	if err != nil {
		t.Fatal(err)
	}
	frozenMany, err := golem.RuntimeFreezeUpdateManyInput(fixture.updateManyTitle("forged-frozen"))
	if err != nil {
		t.Fatal(err)
	}
	where, err := fixture.postID.Eq(golem.UUID{15: 40}).Freeze(fixture.postDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := NewCallerMutationExecution(caller, CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.postDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	beforeFrozenCreate, beforeFrozenUpdate := createBefore.Load(), updateBefore.Load()
	for _, requestInput := range []golem.RuntimeMutationRequestInput{
		{Operation: golem.RuntimeMutationUpdate, Model: schema.Post, Target: &frozenTarget, Input: &frozenUpdate, Projection: &projection},
		{Operation: golem.RuntimeMutationDelete, Model: schema.Post, Target: &frozenTarget, Projection: &projection},
		{Operation: golem.RuntimeMutationUpsert, Model: schema.Post, Target: &frozenTarget, Create: &frozenCreate, Update: &frozenUpdate, Projection: &projection},
		{Operation: golem.RuntimeMutationUpdateMany, Model: schema.Post, Where: &where, Input: &frozenMany},
		{Operation: golem.RuntimeMutationDeleteMany, Model: schema.Post, Where: &where},
	} {
		request, err := golem.RuntimeFreezeMutationRequest(requestInput)
		if err != nil {
			t.Fatal(err)
		}
		_, err = execution.ExecuteFrozenMutation(ctx, request)
		assertOptimisticConcurrencyError(t, err, golem.CodeBadUserInput, "mutation request is invalid")
	}
	if createBefore.Load() != beforeFrozenCreate || updateBefore.Load() != beforeFrozenUpdate {
		t.Fatalf("model-erased bypass invoked hooks create=%d/%d update=%d/%d", createBefore.Load(), beforeFrozenCreate, updateBefore.Load(), beforeFrozenUpdate)
	}
	assertOptimisticConcurrencyRowVersion(t, database, 40, 2)
}

func TestOptimisticConcurrencyPostgreSQLCrossConnectionRaces(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			sequence := mutationVocabularyNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_oc_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_oc_system_%s_%d_%d", profile.name, os.Getpid(), sequence))
			schema := schematest.NewOptimisticConcurrencyPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(applicationNamespace)+`" CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			fixture := openMutationVocabularyFixture(t, database, golem.PostgreSQL, schema)
			system := fixture.app.System()
			decimal, err := golem.ParseDecimal("1.25")
			if err != nil {
				t.Fatal(err)
			}
			decimalField := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal)
			createPost := func(id byte, title string) golem.CreateInput[mutationResultPost] {
				return golem.GeneratedCreateInput(schema.Post,
					golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
					golem.GeneratedCreateFieldValue(schema.Post, fixture.authorID, golem.UUID{15: 1}),
					golem.GeneratedCreateFieldValue(schema.Post, fixture.title, title),
					golem.GeneratedCreateFieldValue(schema.Post, decimalField, decimal),
				)
			}
			user := golem.GeneratedCreateInput[mutationResultUser](schema.User,
				golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 1}),
				golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "alice"),
			)
			if _, err := SystemCreate(ctx, system, fixture.userDescriptor, user); err != nil {
				t.Fatal(err)
			}
			if _, err := SystemCreate(ctx, system, fixture.postDescriptor, createPost(50, "race")); err != nil {
				t.Fatal(err)
			}
			outbox := `"` + string(systemNamespace) + `"."_golem_outbox"`
			if _, err := database.ExecContext(ctx, `DELETE FROM `+outbox); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var writers sync.WaitGroup
			for _, title := range []string{"winner-a", "winner-b"} {
				title := title
				writers.Add(1)
				go func() {
					defer writers.Done()
					<-start
					_, err := SystemUpdateVersioned(ctx, system, fixture.postDescriptor, fixture.target(50), golem.ExpectVersion(1), fixture.updateTitle(title))
					results <- err
				}()
			}
			close(start)
			writers.Wait()
			close(results)
			assertOneOptimisticConcurrencyWinner(t, results)
			posts := `"` + string(applicationNamespace) + `"."posts"`
			var version int64
			if err := database.GetContext(ctx, &version, `SELECT "big_int" FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(50)); err != nil || version != 2 {
				t.Fatalf("update-race version=%d want=2 err=%v", version, err)
			}
			var facts int
			if err := database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+outbox); err != nil || facts != 1 {
				t.Fatalf("update-race facts=%d want=1 err=%v", facts, err)
			}

			start = make(chan struct{})
			results = make(chan error, 2)
			writers = sync.WaitGroup{}
			for _, title := range []string{"create-a", "create-b"} {
				title := title
				writers.Add(1)
				go func() {
					defer writers.Done()
					<-start
					_, err := SystemUpsertVersioned(ctx, system, fixture.postDescriptor, fixture.target(51), golem.ExpectAbsent(), createPost(51, title), fixture.updateTitle("unused"))
					results <- err
				}()
			}
			close(start)
			writers.Wait()
			close(results)
			assertOneOptimisticConcurrencyWinner(t, results)
			if err := database.GetContext(ctx, &version, `SELECT "big_int" FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(51)); err != nil || version != 1 {
				t.Fatalf("upsert-race version=%d want=1 err=%v", version, err)
			}
			if err := database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+outbox); err != nil || facts != 2 {
				t.Fatalf("upsert-race facts=%d want=2 err=%v", facts, err)
			}

			var before, after, commit atomic.Int64
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[mutationResultUser]()
				rules.CanRead(golem.All[mutationResultUser]())
				rules.CanCreate(golem.All[mutationResultUser]())
				rules.CanUpdate(golem.All[mutationResultUser]())
				rules.CanDelete(golem.All[mutationResultUser]())
				return rules.Freeze(schema.User)
			})
			postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[mutationResultPost]()
				rules.CanRead(golem.All[mutationResultPost]())
				rules.CanCreate(golem.All[mutationResultPost]())
				rules.CanUpdate(fixture.title.Eq("visible"))
				rules.CanDelete(golem.All[mutationResultPost]())
				return rules.Freeze(schema.Post)
			})
			hooks := []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error {
					before.Add(1)
					entered <- struct{}{}
					<-release
					return nil
				}),
				golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error { after.Add(1); return nil }),
				golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error { commit.Add(1); return nil }),
			}
			bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, hooks))
			if err != nil {
				t.Fatal(err)
			}
			fixture.app.bindings = bindings
			caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
			for _, collision := range []struct {
				id      byte
				title   string
				code    golem.ErrorCode
				message string
			}{{52, "visible", golem.CodeConflict, "mutation conflicted"}, {53, "hidden", golem.CodeNotFound, "record not found"}} {
				entered = make(chan struct{}, 1)
				release = make(chan struct{})
				result := make(chan error, 1)
				go func(collision struct {
					id      byte
					title   string
					code    golem.ErrorCode
					message string
				}) {
					_, err := CallerUpsertVersioned(ctx, caller, fixture.postDescriptor, fixture.target(collision.id), golem.ExpectAbsent(), createPost(collision.id, "loser"), fixture.updateTitle("unused"))
					result <- err
				}(collision)
				<-entered
				if _, err := database.ExecContext(ctx, `INSERT INTO `+posts+`("id","author_id","title","big_int","decimal_value") VALUES ($1,$2,$3,$4,$5)`, mutationResultUUIDText(collision.id), mutationResultUUIDText(1), collision.title, int64(1), "1.25"); err != nil {
					t.Fatal(err)
				}
				close(release)
				assertOptimisticConcurrencyError(t, <-result, collision.code, collision.message)
			}
			if before.Load() != 2 || after.Load() != 0 || commit.Load() != 0 {
				t.Fatalf("durable collision hooks=%d/%d/%d", before.Load(), after.Load(), commit.Load())
			}
			if err := database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+outbox); err != nil || facts != 2 {
				t.Fatalf("durable collision facts=%d want=2 err=%v", facts, err)
			}
		})
	}
}

func assertOneOptimisticConcurrencyWinner(t testing.TB, results <-chan error) {
	t.Helper()
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var failure *golem.Error
		if errors.As(err, &failure) && failure.Code == golem.CodeConflict && failure.Message == "mutation conflicted" {
			conflicts++
			continue
		}
		t.Fatalf("race error=%v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("race successes=%d conflicts=%d, want one each", successes, conflicts)
	}
}

func assertOptimisticConcurrencyRowVersion(t testing.TB, database *sqlx.DB, id byte, want int64) {
	t.Helper()
	var version int64
	if err := database.GetContext(context.Background(), &version, `SELECT "big_int" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(id)); err != nil || version != want {
		t.Fatalf("row %d version=%d want=%d err=%v", id, version, want, err)
	}
}

func assertOptimisticConcurrencyRow(t testing.TB, database interface {
	QueryRowxContext(context.Context, string, ...any) *sqlx.Row
}, id byte, wantTitle string, wantVersion int64) {
	t.Helper()
	var title string
	var version int64
	if err := database.QueryRowxContext(context.Background(), `SELECT "title", "big_int" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(id)).Scan(&title, &version); err != nil {
		t.Fatal(err)
	}
	if title != wantTitle || version != wantVersion {
		t.Fatalf("row title=%q version=%d, want %q/%d", title, version, wantTitle, wantVersion)
	}
}

func assertOptimisticConcurrencyError(t testing.TB, err error, code golem.ErrorCode, message string) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != code || failure.Message != message {
		t.Fatalf("error=%v failure=%#v cause=%v, want code=%s message=%q", err, failure, errors.Unwrap(err), code, message)
	}
}

func TestOptimisticConcurrencyRollbackFailureCannotMasqueradeAsUniqueCollision(t *testing.T) {
	unique := &pgconn.PgError{Code: "23505", Message: "unique collision"}
	if mutationupsert.UniqueCollision(&absentCreateRollbackFailure{cause: unique}) {
		t.Fatal("rollback failure was incorrectly reclassified as a clean unique collision")
	}
}
