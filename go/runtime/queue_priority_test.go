package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/queue"
)

func TestInteractivePriorityReachesFailureCallback(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		registry, jobType := failedRegistry(t, "priority.failure", 3, func(context.Context, queue.Job[queueGatePayload]) error {
			return queue.Terminal(errors.New("terminal work"))
		})
		var seen atomic.Pointer[queue.Failure[queueGatePayload]]
		callback := OnJobFailed[testPrincipal, testActor](jobType, func(_ context.Context, _ *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
			seen.Store(&failure)
			return nil
		})
		fixture := openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
		pending, err := jobType.New(queueGatePayload{Title: "interactive failure"}, queue.WithPriority(queue.PriorityInteractive))
		if err != nil {
			t.Fatal(err)
		}
		id, err := fixture.app.Enqueue(context.Background(), pending)
		if err != nil {
			t.Fatal(err)
		}
		runFailedWorker(t, fixture.app)
		status := awaitJobState(t, fixture.app, id, queue.StateFailed)
		failure := seen.Load()
		if status.Priority != queue.PriorityInteractive || failure == nil || failure.Priority != queue.PriorityInteractive || failure.ID != id || failure.Attempt != 1 || failure.Code != queue.FailureTerminal || failure.Payload.Title != "interactive failure" {
			t.Fatalf("priority was lost on terminal callback: status=%#v failure=%#v", status, failure)
		}
	})
}

func TestPromoteJobCommitsAndRollsBackWithCallerTransaction(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		registry, jobType := failedRegistry(t, "priority.transaction", 3, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
		fixture := openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, nil)
		ctx := context.Background()
		pending, err := jobType.New(queueGatePayload{}, queue.After(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		id, err := fixture.app.Enqueue(ctx, pending)
		if err != nil {
			t.Fatal(err)
		}
		before, err := fixture.app.QueueOperator().Inspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		caller, err := fixture.app.ForPrincipal(ctx, testPrincipal{Allow: true})
		if err != nil {
			t.Fatal(err)
		}
		rollback := errors.New("rollback promotion")
		err = CallerTransaction(ctx, caller, func(tx *CallerTx[testPrincipal, testActor]) error {
			changed, err := CallerTxPromoteJob(ctx, tx, id, queue.PriorityInteractive, queue.ReadyNow())
			if err != nil || !changed {
				return errors.Join(errors.New("promotion did not change pending work"), err)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("rollback transaction: %v", err)
		}
		after, err := fixture.app.QueueOperator().Inspect(ctx, id)
		if err != nil || after.Priority != queue.PriorityNormal || !after.AvailableAt.Equal(before.AvailableAt) || after.Attempt != before.Attempt {
			t.Fatalf("rollback leaked promotion: %#v error=%v", after, err)
		}
		err = CallerTransaction(ctx, caller, func(tx *CallerTx[testPrincipal, testActor]) error {
			changed, err := CallerTxPromoteJob(ctx, tx, id, queue.PriorityInteractive)
			if err == nil && !changed {
				return errors.New("promotion did not change pending work")
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		after, err = fixture.app.QueueOperator().Inspect(ctx, id)
		if err != nil || after.Priority != queue.PriorityInteractive || !after.AvailableAt.Equal(before.AvailableAt) || after.Attempt != 0 {
			t.Fatalf("committed promotion lost state: %#v error=%v", after, err)
		}
	})
}
