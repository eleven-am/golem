package runtime_test

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

func p10UsageCloseIsWaiting() bool {
	buffer := make([]byte, 1<<22)
	stacks := string(buffer[:goruntime.Stack(buffer, true)])
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, "usageGate).close") && strings.Contains(stack, "sync.(*Cond).Wait") {
			return true
		}
	}
	return false
}

func TestInFlightHookExecutorCallCompletesBeforeItsHookReturnsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		outer, inner := p10OperationID(t, 6300), p10OperationID(t, 6301)
		innerStarted, releaseInner, outerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
		innerResult := make(chan error, 1)
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
			switch p10AttemptDepth(ctx) {
			case 0:
				go func() {
					innerResult <- fixture.createTeam(context.WithValue(ctx, p10AttemptDepthKey{}, 1), executor, inner)
				}()
				<-innerStarted
				return nil
			case 1:
				close(innerStarted)
				<-releaseInner
			}
			return nil
		})
		outerResult := make(chan error, 1)
		go func() {
			_, err := caller.Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(outer), p10operations.Teams.Owner.Create("alpha")))
			close(outerDone)
			outerResult <- err
		}()
		finishedEarly := false
		p10AwaitCondition(func() bool {
			if p10Closed(outerDone) {
				finishedEarly = true
				return true
			}
			return p10UsageCloseIsWaiting()
		})
		close(releaseInner)
		if finishedEarly {
			t.Fatal("the hook returned while its executor call was still in flight")
		}
		if err := <-outerResult; err != nil {
			t.Fatalf("outer write = %v", err)
		}
		if err := <-innerResult; err != nil {
			t.Fatalf("in-flight executor write = %v", err)
		}
		for _, id := range []golem.UUID{outer, inner} {
			if _, ok := fixture.teamOwner(t, id); !ok {
				t.Fatalf("team %s did not persist", id)
			}
		}
	})
}

func TestTransactionWriteThroughACapturedContextIsStillGuardedAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		outer, invite := p10OperationID(t, 6400), p10OperationID(t, 6401)
		operationEnded := make(chan struct{})
		written := make(chan struct{})
		result := make(chan error, 1)
		var captured context.Context
		p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
			if operation != "inviteMember" {
				return nil
			}
			go func() {
				result <- scoped.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
					if _, err := tx.Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(outer), p10operations.Teams.Owner.Create("alpha"))); err != nil {
						return err
					}
					if _, err := tx.Invites.Create(captured, p10operations.Invites.Create(
						p10operations.Invites.ID.Create(invite), p10operations.Invites.TeamID.Create(fixture.alpha),
						p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("captured@example.test"), p10operations.Invites.Status.Create("pending"),
					)); err != nil {
						return err
					}
					close(written)
					<-operationEnded
					return nil
				})
			}()
			select {
			case <-written:
			case err := <-result:
				result <- err
			}
			return errP10StopOperation
		})
		p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
			captured = context.WithoutCancel(ctx)
			return nil
		})
		if _, err := p10operations.Mutate(context.Background(), fixture.caller(t, "alpha"), p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 6402), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
			t.Fatalf("operation = %v", err)
		}
		close(operationEnded)
		err := <-result
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeConflict {
			t.Fatalf("late transaction with a grant write = %v, want CONFLICT", err)
		}
		if exists, _, _ := fixture.inviteExists(t, invite); exists {
			t.Fatal("a grant write committed after its operation ended")
		}
	})
}

func TestCallerTransactionWritesEndWithItsCallbackAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		inFlight, late := p10OperationID(t, 6500), p10OperationID(t, 6501)
		started, release := make(chan struct{}), make(chan struct{})
		inFlightResult, lateResult := make(chan error, 1), make(chan error, 1)
		callbackReturned := make(chan struct{})
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
			if p10AttemptDepth(ctx) == 7 {
				close(started)
				<-release
			}
			return nil
		})
		before := fixture.outboxRows(t)
		transactionResult := make(chan error, 1)
		go func() {
			transactionResult <- caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
				go func() {
					_, err := tx.Teams.Create(context.WithValue(context.Background(), p10AttemptDepthKey{}, 7), p10operations.Teams.Create(p10operations.Teams.ID.Create(inFlight), p10operations.Teams.Owner.Create("alpha")))
					inFlightResult <- err
				}()
				go func() {
					<-callbackReturned
					_, err := tx.Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(late), p10operations.Teams.Owner.Create("alpha")))
					lateResult <- err
				}()
				<-started
				return nil
			})
		}()
		finishedEarly := false
		p10AwaitCondition(func() bool {
			if len(transactionResult) != 0 {
				finishedEarly = true
				return true
			}
			return p10UsageCloseIsWaiting()
		})
		close(callbackReturned)
		lateErr := <-lateResult
		close(release)
		if finishedEarly {
			t.Fatal("the transaction committed while a write was in flight")
		}
		if err := <-transactionResult; err != nil {
			t.Fatalf("transaction = %v", err)
		}
		if err := <-inFlightResult; err != nil {
			t.Fatalf("in-flight write = %v", err)
		}
		if lateErr == nil || !strings.Contains(lateErr.Error(), "after its callback returned") {
			t.Fatalf("write after the callback returned = %v", lateErr)
		}
		if _, ok := fixture.teamOwner(t, inFlight); !ok {
			t.Fatal("the in-flight write did not commit")
		}
		if _, ok := fixture.teamOwner(t, late); ok {
			t.Fatal("a write after the callback returned persisted")
		}
		if got := fixture.outboxRows(t) - before; got != 1 {
			t.Fatalf("outbox rows=%d want 1 for the in-flight write", got)
		}
	})
}
