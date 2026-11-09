package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/queue"
)

const lockOrderStatusID = "00000000-0000-4000-8000-0000000000aa"

type lockOrderRun struct {
	cancelErr   error
	callbackErr atomic.Pointer[error]
}

func runLockOrder(t *testing.T, name string) *lockOrderRun {
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
	run.cancelErr = CallerTransaction(ctx, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		if _, err := transaction.caller.executor.transaction.ExecContext(ctx, fixture.rename, "canceled", lockOrderStatusID, "failed"); err != nil {
			return err
		}
		open()
		_, err := CallerTxCancelJob(ctx, transaction, identity)
		return err
	})
	open()
	return run
}

func TestStatusRowFirstCancelDeadlocksWithTheFailureCallback(t *testing.T) {
	run := runLockOrder(t, "lockorder.status.first")
	callbackErr := run.callbackErr.Load()
	if !isConflict(run.cancelErr) && callbackErr == nil {
		t.Fatalf("no side reported the deadlock: cancel err=%v", run.cancelErr)
	}
}
