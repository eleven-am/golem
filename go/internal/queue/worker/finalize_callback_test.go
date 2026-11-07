package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

type failCall struct {
	record queueprovider.Record
	code   string
}

type failRecorder struct {
	store queueprovider.Store
	mutex sync.Mutex
	calls []failCall
	react func(call int, record queueprovider.Record, code string) error
}

func (recorder *failRecorder) fail(ctx context.Context, record queueprovider.Record, code string) (bool, error) {
	recorder.mutex.Lock()
	recorder.calls = append(recorder.calls, failCall{record: record, code: code})
	call := len(recorder.calls)
	recorder.mutex.Unlock()
	if recorder.react != nil {
		if err := recorder.react(call, record, code); err != nil {
			return false, &CallbackError{Err: err}
		}
	}
	return recorder.store.Fail(ctx, nil, record.ID, record.LeaseToken, code)
}

func (recorder *failRecorder) snapshot() []failCall {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]failCall(nil), recorder.calls...)
}

func startFailing(t *testing.T, fixture *harness, registry *queue.Registry, limits queue.Limits, recorder *failRecorder) func() {
	t.Helper()
	worker, err := New(fixture.store, registry, limits, "", nil, recorder.fail)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("worker ended with %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Error("worker did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestFailureCallbackCodes(t *testing.T) {
	rows := []struct {
		name     string
		attempts int
		timeout  time.Duration
		handle   func(context.Context, queue.Job[gatePayload]) error
		want     string
	}{
		{
			name: "terminal", attempts: 5, want: "terminal",
			handle: func(context.Context, queue.Job[gatePayload]) error { return queue.Terminal(errors.New("poison")) },
		},
		{
			name: "exhausted", attempts: 1, want: "attempts_exhausted",
			handle: func(context.Context, queue.Job[gatePayload]) error { return errors.New("transient") },
		},
		{
			name: "panic on the last attempt", attempts: 1, want: "attempts_exhausted",
			handle: func(context.Context, queue.Job[gatePayload]) error { panic("boom") },
		},
		{
			name: "timeout on the last attempt", attempts: 1, timeout: 30 * time.Millisecond, want: "attempts_exhausted",
			handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			fixture := newHarness(t)
			registry := queue.NewRegistry()
			jobType := register(t, registry, queue.Definition[gatePayload]{Type: "gate.failed.codes", MaxAttempts: row.attempts, Timeout: row.timeout, Handle: row.handle})
			identity := fixture.enqueue(t, newPending(t, jobType))
			recorder := &failRecorder{store: fixture.store}
			startFailing(t, fixture, registry, gateLimits(), recorder)
			record := awaitState(t, fixture, identity, queueprovider.StateFailed)
			calls := recorder.snapshot()
			if len(calls) != 1 || calls[0].code != row.want || calls[0].record.ID != identity || calls[0].record.Finalize {
				t.Fatalf("calls=%#v want code %s", calls, row.want)
			}
			if record.LastCode != row.want {
				t.Fatalf("job=%#v", record)
			}
		})
	}
}

func TestExpiredFinalAttemptIsFinalizedByTheWorkerNotTheStore(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	var executions atomic.Int64
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.failed.crash", MaxAttempts: 1,
		Handle: func(context.Context, queue.Job[gatePayload]) error {
			executions.Add(1)
			return nil
		},
	})
	identity := fixture.enqueue(t, newPending(t, jobType))
	crashed, err := fixture.store.Claim(context.Background(), queueprovider.ClaimOptions{Types: []string{"gate.failed.crash"}, Limit: 1, LeaseDuration: 100 * time.Millisecond})
	if err != nil || len(crashed) != 1 {
		t.Fatalf("crashed claim %#v error=%v", crashed, err)
	}
	time.Sleep(250 * time.Millisecond)
	recorder := &failRecorder{store: fixture.store}
	startFailing(t, fixture, registry, gateLimits(), recorder)
	record := awaitState(t, fixture, identity, queueprovider.StateFailed)
	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].code != "attempts_exhausted" || !calls[0].record.Finalize || calls[0].record.AttemptCount != 1 {
		t.Fatalf("calls=%#v", calls)
	}
	if executions.Load() != 0 || record.AttemptCount != 1 {
		t.Fatalf("executions=%d job=%#v", executions.Load(), record)
	}
}

func TestCallbackFailureDefersTheTerminalDecisionAndRetriesAfterLeaseExpiry(t *testing.T) {
	for name, react := range map[string]func(int, queueprovider.Record, string) error{
		"error": func(call int, _ queueprovider.Record, _ string) error {
			if call == 1 {
				return errors.New("callback refused")
			}
			return nil
		},
		"panic": func(call int, _ queueprovider.Record, _ string) error {
			if call == 1 {
				panic("callback exploded")
			}
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newHarness(t)
			registry := queue.NewRegistry()
			var executions atomic.Int64
			jobType := register(t, registry, queue.Definition[gatePayload]{
				Type: "gate.failed.retry", MaxAttempts: 5,
				Handle: func(context.Context, queue.Job[gatePayload]) error {
					executions.Add(1)
					return queue.Terminal(errors.New("poison"))
				},
			})
			identity := fixture.enqueue(t, newPending(t, jobType))
			recorder := &failRecorder{store: fixture.store, react: react}
			startFailing(t, fixture, registry, gateLimits(), recorder)
			record := awaitState(t, fixture, identity, queueprovider.StateFailed)
			calls := recorder.snapshot()
			if len(calls) != 2 || calls[0].record.Finalize || !calls[1].record.Finalize || calls[0].code != "terminal" || calls[1].code != "terminal" {
				t.Fatalf("calls=%#v", calls)
			}
			if executions.Load() != 1 || record.AttemptCount != 1 || record.LastCode != "terminal" {
				t.Fatalf("executions=%d job=%#v", executions.Load(), record)
			}
		})
	}
}

func TestTerminalIntentIsPersistedBeforeTheCallbackRuns(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.failed.intent", MaxAttempts: 5,
		Handle: func(context.Context, queue.Job[gatePayload]) error { return queue.Terminal(errors.New("poison")) },
	})
	identity := fixture.enqueue(t, newPending(t, jobType))
	var observed atomic.Value
	recorder := &failRecorder{store: fixture.store, react: func(call int, _ queueprovider.Record, _ string) error {
		if call == 1 {
			observed.Store(fixture.inspect(t, identity).LastCode)
			return errors.New("callback refused")
		}
		return nil
	}}
	startFailing(t, fixture, registry, gateLimits(), recorder)
	awaitState(t, fixture, identity, queueprovider.StateFailed)
	if code, _ := observed.Load().(string); code != "terminal" {
		t.Fatalf("last code before the callback=%q", code)
	}
}

func TestCancelStopsAFailingCallback(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.failed.cancel", MaxAttempts: 1,
		Handle: func(context.Context, queue.Job[gatePayload]) error { return errors.New("transient") },
	})
	identity := fixture.enqueue(t, newPending(t, jobType))
	attempted := make(chan struct{})
	var once sync.Once
	recorder := &failRecorder{store: fixture.store, react: func(int, queueprovider.Record, string) error {
		once.Do(func() { close(attempted) })
		return errors.New("callback never succeeds")
	}}
	control, err := NewOperator(fixture.store, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	startFailing(t, fixture, registry, gateLimits(), recorder)
	awaitSignal(t, attempted, "the first callback")
	if changed, err := control.Cancel(context.Background(), queue.JobID(identity)); err != nil || !changed {
		t.Fatalf("cancel changed=%t error=%v", changed, err)
	}
	record := awaitState(t, fixture, identity, queueprovider.StateCanceled)
	if record.LastCode != codeCanceled {
		t.Fatalf("job=%#v", record)
	}
	if calls := recorder.snapshot(); len(calls) != 1 {
		t.Fatalf("callback kept running after cancel: %d calls", len(calls))
	}
}
