package runtime_test

import (
	"context"
	goruntime "runtime"
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

func TestReadOverlappingAnInFlightWriteIsRefusedAndTheWriteDrainsBeforeCommitAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		inFlight := p10OperationID(t, 6950)
		started, transactionReturned := make(chan struct{}), make(chan struct{})
		inFlightResult := make(chan error, 1)
		drainedFirst := make(chan bool, 1)
		var overlapErr error
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
			if p10AttemptDepth(ctx) != 7 {
				return nil
			}
			close(started)
			p10AwaitCondition(func() bool { return p10Closed(transactionReturned) || p10UsageCloseIsWaiting() })
			return nil
		})
		before := fixture.outboxRows(t)
		var err error
		p10FinishesWithin(t, 20*time.Second, func() {
			err = caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
				go func() {
					_, err := tx.Teams.Create(context.WithValue(context.Background(), p10AttemptDepthKey{}, 7), p10TeamInput(inFlight))
					drainedFirst <- !p10Closed(transactionReturned)
					inFlightResult <- err
				}()
				<-started
				_, overlapErr = tx.Teams.FindMany(context.Background(), golem.Where(p10operations.Teams.ID.Eq(inFlight)))
				return nil
			})
			close(transactionReturned)
		})
		if err != nil {
			t.Fatalf("transaction = %v", err)
		}
		if !p10ConcurrentUse(overlapErr) {
			t.Fatalf("read overlapping an in-flight write = %v, want the concurrent-use error", overlapErr)
		}
		if !<-drainedFirst {
			t.Fatal("the transaction finished while an admitted write was in flight")
		}
		if err := <-inFlightResult; err != nil {
			t.Fatalf("in-flight write = %v", err)
		}
		if _, ok := fixture.teamOwner(t, inFlight); !ok {
			t.Fatal("the in-flight write did not commit")
		}
		if got := fixture.outboxRows(t) - before; got != 1 {
			t.Fatalf("outbox rows=%d want 1", got)
		}
	})
}
