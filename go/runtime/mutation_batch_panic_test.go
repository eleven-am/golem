package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
)

type batchPanicHookKey int

const batchPanicHook batchPanicHookKey = 1

type batchPanicValue struct{ message string }

func forEachHookedMutationResultProvider(t *testing.T, limits MutationLimits, hookFactory func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor], run func(testing.TB, mutationResultFixture)) {
	t.Helper()
	discard := func(context.Context, golem.AfterCommitFailure) {}
	t.Run("sqlite", func(t *testing.T) {
		run(t, newMutationResultFixtureWithHooks(t, limits, hookFactory, discard))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			base := newMutationResultFixtureWithHooks(t, limits, hookFactory, discard)
			run(t, reopenHookedMutationResultOnPostgres(t, profile, base, limits))
		})
	}
}

func reopenHookedMutationResultOnPostgres(t *testing.T, profile postgresAcceptanceProfile, base mutationResultFixture, limits MutationLimits) mutationResultFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p4_panic_%s_%d_%d", profile.name, os.Getpid(), suffix))
	systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_panic_system_%s_%d_%d", profile.name, os.Getpid(), suffix))
	schemaFixture := schematest.NewSubscribedIndexedPostgreSQLNamespaces(t, namespace, systemNamespace)
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, profile.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
		_ = database.Close()
	})
	if err := provider.ApplyInitial(ctx, database, schemaFixture.PostgreSQL); err != nil {
		t.Fatal(err)
	}
	users := `"` + string(namespace) + `"."users"`
	for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO `+users+`("id","name") VALUES ($1,$2)`, user[0], user[1]); err != nil {
			t.Fatal(err)
		}
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(database, golem.PostgreSQL), Bundle: schemaFixture.Bundle,
		Bindings: base.app.bindings, Descriptors: base.app.descriptors, MutationLimits: limits,
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
		ResolvePrincipal: base.app.resolvePrincipal, SnapshotActor: base.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	base.app, base.schema = app, schemaFixture
	return base
}

func batchPanicHookFactory(afterCommits, afters *atomic.Int64) func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
	return func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		panicIfRequested := func(hookContext context.Context) {
			if value, ok := hookContext.Value(batchPanicHook).(batchPanicValue); ok {
				panic(value)
			}
		}
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(hookContext context.Context, _ golem.UpdateManyHookResult[mutationResultPost]) error {
				afters.Add(1)
				panicIfRequested(hookContext)
				return nil
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.DeleteManyHookResult[mutationResultPost]](schema.Post, golem.HookDeleteMany, func(hookContext context.Context, _ golem.DeleteManyHookResult[mutationResultPost]) error {
				afters.Add(1)
				panicIfRequested(hookContext)
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(context.Context, golem.UpdateManyHookResult[mutationResultPost]) error {
				afterCommits.Add(1)
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.DeleteManyHookResult[mutationResultPost]](schema.Post, golem.HookDeleteMany, func(context.Context, golem.DeleteManyHookResult[mutationResultPost]) error {
				afterCommits.Add(1)
				return nil
			}),
		}
	}
}

func batchPanicOutboxCount(t testing.TB, fixture mutationResultFixture) int {
	t.Helper()
	var count int
	if err := fixture.app.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	return count
}

// assertBatchPanicReleasedEveryConnection is what distinguishes an aborted
// batch scope from an abandoned one. A panic that skips the rollback leaves the
// batch's own transaction or SQLite connection checked out of the pool forever,
// while the row state is identical either way because nothing was committed.
func assertBatchPanicReleasedEveryConnection(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		if fixture.app.database.Stats().InUse == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("panicked batch left %d connections checked out", fixture.app.database.Stats().InUse)
}

func expectBatchPanic(t testing.TB, want batchPanicValue, invoke func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered != any(want) {
			t.Fatalf("recovered=%#v want=%#v", recovered, want)
		}
	}()
	invoke()
	t.Fatal("batch execution panic did not propagate")
}

// TestBatchHookPanicIsCollapsedToAClosedHookErrorAndRollsBack fixes the batch
// half of the hook panic contract. Generated hooks are invoked through
// golem.invokeGeneratedHookSafely, so a panicking UpdateMany/DeleteMany hook
// never re-panics: it becomes the closed hook refusal and the write rolls back.
func TestBatchHookPanicIsCollapsedToAClosedHookErrorAndRollsBack(t *testing.T) {
	var afterCommits, afters atomic.Int64
	forEachHookedMutationResultProvider(t, MutationLimits{}, batchPanicHookFactory(&afterCommits, &afters), func(t testing.TB, fixture mutationResultFixture) {
		ctx := context.Background()
		caller := mustMutationResultCaller(t, fixture)
		for _, id := range []byte{101, 102} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "panic-batch")); err != nil {
				t.Fatal(err)
			}
		}
		set := fixture.postID.In(golem.UUID{15: 101}, golem.UUID{15: 102})
		facts := batchPanicOutboxCount(t, fixture)
		afterCommitsBefore, aftersBefore := afterCommits.Load(), afters.Load()
		panicContext := context.WithValue(ctx, batchPanicHook, batchPanicValue{"batch hook panic"})

		count, err := CallerUpdateMany(panicContext, caller, fixture.postDescriptor, set, fixture.updateManyTitle("panic-written"))
		var failure *golem.Error
		if count != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
			t.Fatalf("update-many hook panic count=%d failure=%#v err=%v", count, failure, err)
		}
		assertMutationResultTitleCount(t, fixture, "panic-batch", 2)
		assertMutationResultTitleCount(t, fixture, "panic-written", 0)

		count, err = CallerDeleteMany(panicContext, caller, fixture.postDescriptor, set)
		if count != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
			t.Fatalf("delete-many hook panic count=%d failure=%#v err=%v", count, failure, err)
		}
		assertMutationResultTitleCount(t, fixture, "panic-batch", 2)

		err = CallerTransaction(panicContext, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, createErr := CallerTxCreate(panicContext, transaction, fixture.postDescriptor, fixture.createPost(103, golem.UUID{15: 1}, "panic-companion")); createErr != nil {
				return createErr
			}
			_, updateErr := CallerTxUpdateMany(panicContext, transaction, fixture.postDescriptor, set, fixture.updateManyTitle("panic-written"))
			return updateErr
		})
		if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
			t.Fatalf("transactional hook panic failure=%#v err=%v", failure, err)
		}
		assertMutationResultTitleCount(t, fixture, "panic-batch", 2)
		assertMutationResultTitleCount(t, fixture, "panic-written", 0)
		assertMutationResultTitleCount(t, fixture, "panic-companion", 0)

		if got := batchPanicOutboxCount(t, fixture); got != facts {
			t.Fatalf("panicked batches wrote facts=%d want=%d", got, facts)
		}
		if afters.Load() != aftersBefore+3 {
			t.Fatalf("after hooks ran %d times", afters.Load()-aftersBefore)
		}
		if afterCommits.Load() != afterCommitsBefore {
			t.Fatal("after-commit hooks ran for a rolled-back batch")
		}
	})
}

// TestBatchExecutionPanicRollsBackAndRepanics is the gate on the batch copy of
// the recover-rollback-repanic owner. Application hooks cannot reach it, so the
// private after-capture execution seam is what raises the panic while the
// batch's own transaction or savepoint is open.
func TestBatchExecutionPanicRollsBackAndRepanics(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, func(t testing.TB, fixture mutationResultFixture) {
		ctx := context.Background()
		system := fixture.app.System()
		for _, id := range []byte{121, 122} {
			if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "execution-panic")); err != nil {
				t.Fatal(err)
			}
		}
		facts := batchPanicOutboxCount(t, fixture)

		owned := batchPanicValue{"batch-owned transaction panic"}
		ownedContext := contextWithBatchAfterCaptureObserver(ctx, func(context.Context) error { panic(owned) })
		expectBatchPanic(t, owned, func() {
			_, _ = SystemUpdateMany(ownedContext, system, fixture.postDescriptor, fixture.title.Eq("execution-panic"), fixture.updateManyTitle("execution-written"))
		})
		assertBatchPanicReleasedEveryConnection(t, fixture)
		assertMutationResultTitleCount(t, fixture, "execution-panic", 2)
		assertMutationResultTitleCount(t, fixture, "execution-written", 0)

		savepoint := batchPanicValue{"batch savepoint panic"}
		savepointContext := contextWithBatchAfterCaptureObserver(ctx, func(context.Context) error { panic(savepoint) })
		expectBatchPanic(t, savepoint, func() {
			_ = SystemTransaction(savepointContext, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				if _, createErr := SystemTxCreate(savepointContext, transaction, fixture.postDescriptor, fixture.createPost(123, golem.UUID{15: 1}, "execution-companion")); createErr != nil {
					return createErr
				}
				_, updateErr := SystemTxUpdateMany(savepointContext, transaction, fixture.postDescriptor, fixture.title.Eq("execution-panic"), fixture.updateManyTitle("execution-written"))
				return updateErr
			})
		})
		assertBatchPanicReleasedEveryConnection(t, fixture)
		assertMutationResultTitleCount(t, fixture, "execution-panic", 2)
		assertMutationResultTitleCount(t, fixture, "execution-written", 0)
		assertMutationResultTitleCount(t, fixture, "execution-companion", 0)
		if got := batchPanicOutboxCount(t, fixture); got != facts {
			t.Fatalf("panicked batch execution wrote facts=%d want=%d", got, facts)
		}
	})
}

// TestBatchHooksWithTouchedRowLimitExceededMidBatch covers the combination no
// existing limit test reaches: hooks are registered and the batch's own capture
// succeeds, so the execution-wide touched-row ceiling is only reached after the
// after hook has already observed the result.
func TestBatchHooksWithTouchedRowLimitExceededMidBatch(t *testing.T) {
	var afterCommits, afters atomic.Int64
	forEachHookedMutationResultProvider(t, MutationLimits{MaxTouchedRows: 3}, batchPanicHookFactory(&afterCommits, &afters), func(t testing.TB, fixture mutationResultFixture) {
		ctx := context.Background()
		caller := mustMutationResultCaller(t, fixture)
		for _, id := range []byte{111, 112, 113} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "limit-batch")); err != nil {
				t.Fatal(err)
			}
		}
		set := fixture.postID.In(golem.UUID{15: 111}, golem.UUID{15: 112}, golem.UUID{15: 113})
		facts := batchPanicOutboxCount(t, fixture)
		afterCommitsBefore, aftersBefore := afterCommits.Load(), afters.Load()

		err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, createErr := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(114, golem.UUID{15: 1}, "limit-companion")); createErr != nil {
				return createErr
			}
			_, updateErr := CallerTxUpdateMany(ctx, transaction, fixture.postDescriptor, set, fixture.updateManyTitle("limit-written"))
			return updateErr
		})
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
			t.Fatalf("mid-batch touched-row overflow err=%v failure=%#v", err, failure)
		}
		assertMutationResultTitleCount(t, fixture, "limit-batch", 3)
		assertMutationResultTitleCount(t, fixture, "limit-written", 0)
		assertMutationResultTitleCount(t, fixture, "limit-companion", 0)
		if got := batchPanicOutboxCount(t, fixture); got != facts {
			t.Fatalf("mid-batch overflow wrote facts=%d want=%d", got, facts)
		}
		if afters.Load() != aftersBefore+1 {
			t.Fatalf("after hooks ran %d times before the touched-row ceiling", afters.Load()-aftersBefore)
		}
		if afterCommits.Load() != afterCommitsBefore {
			t.Fatal("after-commit hooks ran for a rolled-back batch")
		}
	})
}
