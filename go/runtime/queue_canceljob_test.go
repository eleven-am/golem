package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/queue"
)

func cancelInCaller(t *testing.T, app *App[testPrincipal, testActor], identity queue.JobID) (bool, error) {
	t.Helper()
	ctx := context.Background()
	caller, err := app.ForPrincipal(ctx, testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	var canceled bool
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		var cancelErr error
		canceled, cancelErr = CallerTxCancelJob(ctx, transaction, identity)
		return cancelErr
	})
	return canceled, err
}

func TestCancelJobCancelsAPendingJobAndItNeverRuns(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		var mutex sync.Mutex
		ran := map[queue.JobID]bool{}
		registry, jobType := failedRegistry(t, "cancel.pending", 3, func(_ context.Context, job queue.Job[queueGatePayload]) error {
			mutex.Lock()
			defer mutex.Unlock()
			ran[job.ID] = true
			return nil
		})
		fixture := openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, nil)
		ctx := context.Background()
		doomed, err := fixture.app.Enqueue(ctx, newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		sentinel, err := fixture.app.Enqueue(ctx, newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		canceled, err := cancelInCaller(t, fixture.app, doomed)
		if err != nil || !canceled {
			t.Fatalf("cancel = %v, %v", canceled, err)
		}
		runFailedWorker(t, fixture.app)
		awaitJobState(t, fixture.app, sentinel, queue.StateSucceeded)
		status := awaitJobState(t, fixture.app, doomed, queue.StateCanceled)
		mutex.Lock()
		defer mutex.Unlock()
		if ran[doomed] || !ran[sentinel] || status.State != queue.StateCanceled {
			t.Fatalf("ran=%v status=%#v", ran, status)
		}
	})
}

func TestCancelJobRecordsARequestOnALeasedJob(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		started := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		registry, jobType := failedRegistry(t, "cancel.leased", 3, func(context.Context, queue.Job[queueGatePayload]) error {
			once.Do(func() { close(started) })
			<-release
			return nil
		})
		fixture := openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, nil)
		identity, err := fixture.app.Enqueue(context.Background(), newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		runFailedWorker(t, fixture.app)
		t.Cleanup(func() { close(release) })
		<-started
		canceled, err := cancelInCaller(t, fixture.app, identity)
		if err != nil || !canceled {
			t.Fatalf("cancel = %v, %v", canceled, err)
		}
		status, err := fixture.app.QueueOperator().Inspect(context.Background(), identity)
		if err != nil || status.State != queue.StateLeased || !status.CancelRequested {
			t.Fatalf("status=%#v err=%v", status, err)
		}
		if again, err := cancelInCaller(t, fixture.app, identity); err != nil || again {
			t.Fatalf("second cancel = %v, %v", again, err)
		}
	})
}

func TestCancelJobRollsBackWithTheTransaction(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		registry, jobType := failedRegistry(t, "cancel.rollback", 3, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
		fixture := openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, nil)
		ctx := context.Background()
		identity, err := fixture.app.Enqueue(ctx, newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		refusal := errors.New("refused")
		err = SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[testPrincipal, testActor]) error {
			if err := fixture.record(transaction, "cancel-rollback"); err != nil {
				return err
			}
			if canceled, err := SystemTxCancelJob(ctx, transaction, identity); err != nil || !canceled {
				return errors.Join(errors.New("cancel did not report true"), err)
			}
			return refusal
		})
		if !errors.Is(err, refusal) {
			t.Fatalf("transaction = %v", err)
		}
		status, err := fixture.app.QueueOperator().Inspect(ctx, identity)
		if err != nil || status.State != queue.StatePending || fixture.users(t, "cancel-rollback") != 0 {
			t.Fatalf("status=%#v err=%v", status, err)
		}
		if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[testPrincipal, testActor]) error {
			if err := fixture.record(transaction, "cancel-commit"); err != nil {
				return err
			}
			canceled, err := SystemTxCancelJob(ctx, transaction, identity)
			if err == nil && !canceled {
				err = errors.New("cancel did not report true")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		status, err = fixture.app.QueueOperator().Inspect(ctx, identity)
		if err != nil || status.State != queue.StateCanceled || fixture.users(t, "cancel-commit") != 1 {
			t.Fatalf("status=%#v err=%v", status, err)
		}
	})
}

func TestCancelJobReportsFalseForAnUnknownID(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		registry, _ := failedRegistry(t, "cancel.unknown", 3, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
		fixture := openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, nil)
		if canceled, err := cancelInCaller(t, fixture.app, "missing"); err != nil || canceled {
			t.Fatalf("cancel = %v, %v", canceled, err)
		}
	})
}
