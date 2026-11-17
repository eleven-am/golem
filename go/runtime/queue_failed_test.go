package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

func TestFailureCallbackCommitsAtomicallyWithFailed(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		registry, jobType := failedRegistry(t, "failed.atomic", 5, func(context.Context, queue.Job[queueGatePayload]) error {
			return queue.Terminal(errors.New("poison"))
		})
		var fixture failedFixture
		var seen atomic.Pointer[queue.Failure[queueGatePayload]]
		callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
			seen.Store(&failure)
			return fixture.record(transaction, "failed-"+string(failure.ID))
		})
		fixture = openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
		identity, err := fixture.app.Enqueue(context.Background(), newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		runFailedWorker(t, fixture.app)
		status := awaitJobState(t, fixture.app, identity, queue.StateFailed)
		if fixture.users(t, "failed-"+string(identity)) != 1 {
			t.Fatal("failed job committed without its status row")
		}
		failure := seen.Load()
		if failure == nil || failure.ID != identity || failure.Code != queue.FailureTerminal || failure.Payload.Title != "draft" || failure.Attempt != 1 || failure.MaxAttempts != 5 {
			t.Fatalf("failure=%#v status=%#v", failure, status)
		}
	})
}

func TestFailureCallbackErrorRollsBackAndRetriesAfterLeaseExpiry(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		var executions atomic.Int64
		registry, jobType := failedRegistry(t, "failed.rollback", 5, func(context.Context, queue.Job[queueGatePayload]) error {
			executions.Add(1)
			return queue.Terminal(errors.New("poison"))
		})
		var fixture failedFixture
		var calls atomic.Int64
		refused := make(chan struct{})
		callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
			if err := fixture.record(transaction, "rollback-"+string(failure.ID)); err != nil {
				return err
			}
			if calls.Add(1) == 1 {
				defer close(refused)
				return errors.New("callback refused")
			}
			return nil
		})
		fixture = openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
		identity, err := fixture.app.Enqueue(context.Background(), newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		runFailedWorker(t, fixture.app)
		select {
		case <-refused:
		case <-time.After(30 * time.Second):
			t.Fatal("the callback never ran")
		}
		name := "rollback-" + string(identity)
		status, err := fixture.app.QueueOperator().Inspect(context.Background(), identity)
		if err != nil || status.State == queue.StateFailed || fixture.users(t, name) != 0 {
			t.Fatalf("a refused callback leaked state=%s users=%d error=%v", status.State, fixture.users(t, name), err)
		}
		awaitJobState(t, fixture.app, identity, queue.StateFailed)
		if fixture.users(t, name) != 1 || calls.Load() != 2 || executions.Load() != 1 {
			t.Fatalf("users=%d calls=%d executions=%d", fixture.users(t, name), calls.Load(), executions.Load())
		}
	})
}

func TestFailureCallbackRunsForACrashOnTheLastAttempt(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		var executions atomic.Int64
		registry, jobType := failedRegistry(t, "failed.crash", 1, func(context.Context, queue.Job[queueGatePayload]) error {
			executions.Add(1)
			return nil
		})
		var fixture failedFixture
		var code atomic.Value
		callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
			code.Store(failure.Code)
			return fixture.record(transaction, "crash-"+string(failure.ID))
		})
		fixture = openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
		identity, err := fixture.app.Enqueue(context.Background(), newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		crashed, err := fixture.app.queueStore.Claim(context.Background(), queueprovider.ClaimOptions{Types: []string{"failed.crash"}, Limit: 1, LeaseDuration: 100 * time.Millisecond})
		if err != nil || len(crashed) != 1 {
			t.Fatalf("crashed claim %#v error=%v", crashed, err)
		}
		time.Sleep(250 * time.Millisecond)
		runFailedWorker(t, fixture.app)
		awaitJobState(t, fixture.app, identity, queue.StateFailed)
		if fixture.users(t, "crash-"+string(identity)) != 1 || executions.Load() != 0 || code.Load() != queue.FailureAttemptsExhausted {
			t.Fatalf("users=%d executions=%d code=%v", fixture.users(t, "crash-"+string(identity)), executions.Load(), code.Load())
		}
	})
}

func TestFailureCallbackReportsAnUndecodablePayload(t *testing.T) {
	registry, jobType := failedRegistry(t, "failed.payload", 5, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
	var fixture failedFixture
	var seen atomic.Pointer[queue.Failure[queueGatePayload]]
	callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
		seen.Store(&failure)
		return nil
	})
	fixture = openFailedFixture(t, "sqlite", &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
	raw, err := queueprovider.NewIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.queueStore.Enqueue(context.Background(), nil, queueprovider.EnqueueRequest{ID: raw, Type: "failed.payload", Payload: []byte(`[1,2]`), MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	identity := queue.JobID(raw)
	runFailedWorker(t, fixture.app)
	awaitJobState(t, fixture.app, identity, queue.StateFailed)
	failure := seen.Load()
	if failure == nil || failure.Code != queue.FailurePayloadInvalid || failure.Payload.Title != "" {
		t.Fatalf("failure=%#v", failure)
	}
}

func TestQueueFailedRefusalsAtOpen(t *testing.T) {
	registry, jobType := failedRegistry(t, "failed.open", 1, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
	_, stranger := failedRegistry(t, "failed.stranger", 1, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
	callback := func(jobType queue.Type[queueGatePayload]) JobFailed[testPrincipal, testActor] {
		return OnJobFailed[testPrincipal, testActor](jobType, func(context.Context, *SystemTx[testPrincipal, testActor], queue.Failure[queueGatePayload]) error {
			return nil
		})
	}
	config := &QueueConfig{Registry: registry, Limits: failedLimits()}
	rows := []struct {
		name   string
		config *QueueConfig
		failed []JobFailed[testPrincipal, testActor]
	}{
		{"without a queue", nil, []JobFailed[testPrincipal, testActor]{callback(jobType)}},
		{"zero value", config, []JobFailed[testPrincipal, testActor]{{}}},
		{"nil callback", config, []JobFailed[testPrincipal, testActor]{OnJobFailed[testPrincipal, testActor, queueGatePayload](jobType, nil)}},
		{"unregistered type", config, []JobFailed[testPrincipal, testActor]{callback(stranger)}},
		{"duplicate type", config, []JobFailed[testPrincipal, testActor]{callback(jobType), callback(jobType)}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := openFailedApp(t, "sqlite", row.config, row.failed)
			if code, ok := queue.CodeOf(err); !ok || code != queue.CodeConfigInvalid {
				t.Fatalf("Open reported %v", err)
			}
		})
	}
}
