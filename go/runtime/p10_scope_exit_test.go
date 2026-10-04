package runtime

import (
	"context"
	"errors"
	goruntime "runtime"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func noMutationResultHooks(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
	return nil
}

func TestNestedSavepointCommitClosesItsScopeWhenReleaseFailsAcrossProviders(t *testing.T) {
	forEachHookedMutationResultProvider(t, MutationLimits{}, noMutationResultHooks, func(t testing.TB, fixture mutationResultFixture) {
		ctx := context.Background()
		transaction, err := fixture.app.database.BeginTxx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = transaction.Rollback() }()
		binding := transactionExecution(fixture.app.database, transaction)
		if err := binding.enableMutation(mutationConfig(fixture.app, fixture.app.System().executor)); err != nil {
			t.Fatal(err)
		}
		state, err := binding.mutationState()
		if err != nil {
			t.Fatal(err)
		}
		boundary := &systemNestedBoundary[mutationResultPrincipal, mutationResultActor]{app: fixture.app, source: binding, stance: mutationir.System}
		nested, err := boundary.BeginNested(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if err := nested.CommitNested(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatalf("commit with a failing RELEASE = %v, want the cancellation", err)
		}
		state.mu.Lock()
		open, failure := len(state.open), state.failure
		state.mu.Unlock()
		if open != 0 {
			t.Fatalf("a failed RELEASE left %d mutation scope owners open", open)
		}
		if failure != nil {
			t.Fatalf("a recovered savepoint poisoned the transaction: %v", failure)
		}
		if err := nested.RollbackNested(ctx); err != nil {
			t.Fatalf("rollback after the commit closed the scope = %v", err)
		}
		state.mu.Lock()
		failure = state.failure
		state.mu.Unlock()
		if failure != nil {
			t.Fatalf("rolling back a closed scope poisoned the transaction: %v", failure)
		}
		var one int
		if err := transaction.QueryRowxContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("transaction unusable after the recovered savepoint: %d %v", one, err)
		}
	})
}

func TestBatchAndScalarHookGatesCloseWhenTheHookPhaseUnwinds(t *testing.T) {
	unwinds := map[string]func(){
		"panic":  func() { panic("executor factory panicked") },
		"goexit": goruntime.Goexit,
	}
	phases := map[string]func(context.Context, *callerMutationHookExecution[mutationResultActor]){
		"batch": func(ctx context.Context, hooks *callerMutationHookExecution[mutationResultActor]) {
			result := golem.RuntimeMutationHookResult{}
			_ = invokeBatchAfterHooks(ctx, hooks, nil, golem.ModelID{}, golem.HookOperation(""), &result)
		},
		"scalar": func(ctx context.Context, hooks *callerMutationHookExecution[mutationResultActor]) {
			_ = hooks.observeResult(ctx, nil, golem.RuntimeMutationHookResult{})
		},
	}
	for phaseName, phase := range phases {
		for unwindName, unwind := range unwinds {
			phase, unwind := phase, unwind
			t.Run(phaseName+"/"+unwindName, func(t *testing.T) {
				var gate *hookExecutorGate
				hooks := &callerMutationHookExecution[mutationResultActor]{
					executor: func(_ *executionBinding, opened *hookExecutorGate) golem.HookExecutor {
						gate = opened
						unwind()
						return golem.HookExecutor{}
					},
				}
				done := make(chan any, 1)
				go func() {
					unwound := true
					defer func() {
						recovered := recover()
						if !unwound {
							recovered = "returned"
						}
						done <- recovered
					}()
					phase(context.Background(), hooks)
					unwound = false
				}()
				if outcome := <-done; outcome == "returned" {
					t.Fatal("the hook phase returned instead of unwinding")
				}
				if gate == nil {
					t.Fatal("the hook phase never opened an executor gate")
				}
				if gate.usage.enter() {
					gate.usage.leave()
					t.Fatal("the executor gate stayed open after the hook phase unwound")
				}
			})
		}
	}
}
