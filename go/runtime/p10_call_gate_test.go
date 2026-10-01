package runtime_test

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

type p10RetainKey struct{}

func p10FinishesWithin(t *testing.T, deadline time.Duration, run func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		buffer := make([]byte, 1<<22)
		t.Fatalf("did not finish within %s:\n%s", deadline, buffer[:goruntime.Stack(buffer, true)])
	}
}

func p10ReadIsWaiting(function string) bool {
	buffer := make([]byte, 1<<22)
	stacks := string(buffer[:goruntime.Stack(buffer, true)])
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, function) && strings.Contains(stack, "sync.(*Mutex).Lock") {
			return true
		}
	}
	return false
}

func TestHookExecutorWithARetainedContextLocksThroughItsOwnHookAcrossProviders(t *testing.T) {
	sources := []struct {
		name     string
		derive   func(context.Context) context.Context
		succeeds bool
	}{
		{"retained", func(ctx context.Context) context.Context { return ctx }, true},
		{"retained and detached", context.WithoutCancel, true},
		{"retained and cancelled", func(ctx context.Context) context.Context {
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			return cancelled
		}, false},
	}
	for _, source := range sources {
		source := source
		t.Run(source.name, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				caller := fixture.caller(t, "alpha")
				first, second, inner := p10OperationID(t, 6900), p10OperationID(t, 6901), p10OperationID(t, 6902)
				var retained context.Context
				var innerErr error
				p10operations.Reset(nil)
				p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
					switch ctx.Value(p10RetainKey{}) {
					case "first":
						retained = ctx
					case "second":
						innerErr = fixture.createTeam(context.WithValue(source.derive(retained), p10RetainKey{}, "inner"), executor, inner)
					}
					return nil
				})
				var err error
				p10FinishesWithin(t, 20*time.Second, func() {
					err = caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
						if _, err := tx.Teams.Create(context.WithValue(context.Background(), p10RetainKey{}, "first"), p10TeamInput(first)); err != nil {
							return err
						}
						_, err := tx.Teams.Create(context.WithValue(context.Background(), p10RetainKey{}, "second"), p10TeamInput(second))
						return err
					})
				})
				if err != nil {
					t.Fatalf("transaction = %v", err)
				}
				if source.succeeds != (innerErr == nil) {
					t.Fatalf("executor write through a %s context = %v", source.name, innerErr)
				}
				for id, want := range map[golem.UUID]bool{first: true, second: true, inner: source.succeeds} {
					if _, ok := fixture.teamOwner(t, id); ok != want {
						t.Fatalf("team %s persisted=%t want %t", id, ok, want)
					}
				}
			})
		})
	}
}

func TestReadsInFlightWhenTheCallbackReturnsFinishBeforeCommitAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		inFlight := p10OperationID(t, 6950)
		started, callbackReturned, transactionReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
		readResult, lateResult := make(chan error, 1), make(chan error, 1)
		readBeforeReturn := make(chan bool, 1)
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
			if p10AttemptDepth(ctx) != 7 {
				return nil
			}
			close(started)
			p10AwaitCondition(func() bool { return p10Closed(transactionReturned) || p10UsageCloseIsWaiting() })
			close(callbackReturned)
			p10AwaitCondition(func() bool {
				return len(lateResult) != 0 || p10Closed(transactionReturned) || p10ReadIsWaiting("runtime.executePreparedCount[")
			})
			return nil
		})
		before := fixture.outboxRows(t)
		var err error
		p10FinishesWithin(t, 20*time.Second, func() {
			err = caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
				go func() {
					_, err := tx.Teams.Create(context.WithValue(context.Background(), p10AttemptDepthKey{}, 7), p10TeamInput(inFlight))
					if err != nil {
						t.Errorf("in-flight write = %v", err)
					}
				}()
				<-started
				go func() {
					rows, err := tx.Teams.FindMany(context.Background(), golem.Where(p10operations.Teams.ID.Eq(inFlight)))
					if err == nil && len(rows) != 1 {
						err = errors.New("the in-flight read did not see the in-flight write")
					}
					readBeforeReturn <- !p10Closed(transactionReturned)
					readResult <- err
				}()
				go func() {
					<-callbackReturned
					_, err := tx.Teams.Count(context.Background(), golem.Where(p10operations.Teams.Owner.Eq("alpha")))
					lateResult <- err
				}()
				p10AwaitCondition(func() bool { return p10ReadIsWaiting("runtime.executeRenderedPlan[") })
				return nil
			})
			close(transactionReturned)
		})
		if err != nil {
			t.Fatalf("transaction = %v", err)
		}
		if err := <-readResult; err != nil {
			t.Fatalf("read admitted before the callback returned = %v", err)
		}
		if !<-readBeforeReturn {
			t.Fatal("the transaction finished while an admitted read was in flight")
		}
		if err := <-lateResult; err == nil || !strings.Contains(err.Error(), "after its callback returned") {
			t.Fatalf("read after the callback returned = %v", err)
		}
		if _, ok := fixture.teamOwner(t, inFlight); !ok {
			t.Fatal("the in-flight write did not commit")
		}
		if got := fixture.outboxRows(t) - before; got != 1 {
			t.Fatalf("outbox rows=%d want 1", got)
		}
	})
}
