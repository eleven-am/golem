package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queue"
)

const lockOrderStatusID = "00000000-0000-4000-8000-0000000000aa"

type lockOrderRun struct {
	cancelErr     error
	callbackErr   atomic.Pointer[error]
	cancelTries   int
	finalStatus   string
	finalJobState queue.State
}

func runLockOrder(t *testing.T, name string, statusFirst bool, options ...golem.TransactionOption) *lockOrderRun {
	t.Helper()
	ctx := context.Background()
	registry, jobType := failedRegistry(t, name, 1, func(context.Context, queue.Job[queueGatePayload]) error {
		return queue.Terminal(errors.New("poison"))
	})
	var fixture failedFixture
	insideFail := make(chan struct{})
	release := make(chan struct{})
	var insideOnce sync.Once
	run := &lockOrderRun{}
	callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
		insideOnce.Do(func() { close(insideFail) })
		<-release
		_, err := transaction.system.executor.transaction.ExecContext(ctx, fixture.rename, "failed", lockOrderStatusID, "canceled")
		if err != nil {
			run.callbackErr.Store(&err)
		}
		return err
	})
	fixture = openFailedFixture(t, "postgresql", &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
	if _, err := fixture.database.ExecContext(ctx, fixture.insert, lockOrderStatusID, "queued"); err != nil {
		t.Fatal(err)
	}
	identity, err := fixture.app.Enqueue(ctx, newQueueGatePending(t, jobType))
	if err != nil {
		t.Fatal(err)
	}
	runFailedWorker(t, fixture.app)
	<-insideFail

	caller, err := fixture.app.ForPrincipal(ctx, testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	open := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(open)
	cancelStatus := func(transaction *CallerTx[testPrincipal, testActor]) error {
		_, err := transaction.caller.executor.transaction.ExecContext(ctx, fixture.rename, "canceled", lockOrderStatusID, "failed")
		return err
	}
	run.cancelErr = CallerTransaction(ctx, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		run.cancelTries++
		if statusFirst {
			if err := cancelStatus(transaction); err != nil {
				return err
			}
			open()
			_, err := CallerTxCancelJob(ctx, transaction, identity)
			return err
		}
		open()
		if _, err := CallerTxCancelJob(ctx, transaction, identity); err != nil {
			return err
		}
		return cancelStatus(transaction)
	}, options...)
	open()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err := fixture.app.QueueOperator().Inspect(ctx, identity)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == queue.StateFailed || status.State == queue.StateCanceled {
			run.finalJobState = status.State
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := fixture.database.QueryRowContext(ctx, fixture.lookup, lockOrderStatusID).Scan(&run.finalStatus); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRecipeCancelLocksTheJobBeforeTheStatusRow(t *testing.T) {
	run := runLockOrder(t, "lockorder.job.first", false, golem.RetryConflicts(3))
	if run.cancelErr != nil || run.cancelTries != 1 || run.callbackErr.Load() != nil {
		t.Fatalf("cancel err=%v tries=%d callback err=%v", run.cancelErr, run.cancelTries, run.callbackErr.Load())
	}
	if (run.finalStatus != "failed" && run.finalStatus != "canceled") || (run.finalJobState != queue.StateFailed && run.finalJobState != queue.StateCanceled) {
		t.Fatalf("status=%q job=%q", run.finalStatus, run.finalJobState)
	}
}

func TestStatusRowFirstCancelDeadlocksWithTheFailureCallback(t *testing.T) {
	run := runLockOrder(t, "lockorder.status.first", true)
	callbackErr := run.callbackErr.Load()
	if !isConflict(run.cancelErr) && callbackErr == nil {
		t.Fatalf("no side reported the deadlock: cancel err=%v", run.cancelErr)
	}
}

func TestRecipeCancelGuardsOnTheObservedJob(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		name := "unguarded"
		if guarded {
			name = "guarded"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := openFailedFixture(t, "postgresql", nil, nil)
			if _, err := fixture.database.ExecContext(ctx, fixture.insert, lockOrderStatusID, "queued:A"); err != nil {
				t.Fatal(err)
			}
			caller, err := fixture.app.ForPrincipal(ctx, testPrincipal{Allow: true})
			if err != nil {
				t.Fatal(err)
			}
			statement := strings.Replace(fixture.rename, `"name"<>`, `"name"=`, 1)
			err = CallerTransaction(ctx, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
				var observed string
				if err := transaction.caller.executor.transaction.QueryRowContext(ctx, fixture.lookup, lockOrderStatusID).Scan(&observed); err != nil {
					return err
				}
				if _, err := fixture.database.ExecContext(ctx, fixture.rename, "queued:B", lockOrderStatusID, "none"); err != nil {
					return err
				}
				guard := observed
				if !guarded {
					guard = "queued:B"
				}
				_, err := transaction.caller.executor.transaction.ExecContext(ctx, statement, "canceled", lockOrderStatusID, guard)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			var status string
			if err := fixture.database.QueryRowContext(ctx, fixture.lookup, lockOrderStatusID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "canceled"
			if guarded {
				want = "queued:B"
			}
			if status != want {
				t.Fatalf("status=%q want %q", status, want)
			}
		})
	}
}
