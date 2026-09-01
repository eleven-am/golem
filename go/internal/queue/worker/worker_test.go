package worker

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queue"
)

type gatePayload struct {
	Value string `json:"value"`
}

type harness struct {
	store queueprovider.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	provider := sqlite.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := provider.QueueStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &harness{store: store}
}

func (fixture *harness) enqueue(t *testing.T, pending queue.Pending) string {
	t.Helper()
	identity, err := queueprovider.NewIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.store.Enqueue(context.Background(), nil, queueprovider.EnqueueRequest{
		ID: identity, Type: pending.TypeName(), Payload: pending.Payload(), MaxAttempts: pending.MaxAttempts(),
		Delay: pending.Delay(), DedupeKey: pending.DedupeKey(), ExclusiveKey: pending.ExclusiveKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func (fixture *harness) inspect(t *testing.T, identity string) queueprovider.Record {
	t.Helper()
	record, err := fixture.store.Inspect(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func (fixture *harness) start(t *testing.T, store queueprovider.Store, registry *queue.Registry, limits queue.Limits) (*Worker, func()) {
	return fixture.startObserved(t, store, registry, limits, "", nil)
}

func (fixture *harness) startObserved(t *testing.T, store queueprovider.Store, registry *queue.Registry, limits queue.Limits, provider golem.Provider, observer observe.Observer) (*Worker, func()) {
	t.Helper()
	worker, err := New(store, registry, limits, provider, observer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker ended with %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("worker did not stop")
		}
	}
	t.Cleanup(stop)
	return worker, stop
}

type stubStore struct {
	queueprovider.Store
	renew   func(context.Context, string, string, time.Duration) (queueprovider.Renewal, error)
	claim   func(context.Context, queueprovider.ClaimOptions) ([]queueprovider.Record, error)
	succeed func(context.Context, string, string, string) (bool, error)
}

func (stub stubStore) Renew(ctx context.Context, id, token string, duration time.Duration) (queueprovider.Renewal, error) {
	if stub.renew != nil {
		return stub.renew(ctx, id, token, duration)
	}
	return stub.Store.Renew(ctx, id, token, duration)
}

func (stub stubStore) Claim(ctx context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
	if stub.claim != nil {
		return stub.claim(ctx, options)
	}
	return stub.Store.Claim(ctx, options)
}

func (stub stubStore) Succeed(ctx context.Context, id, token, code string) (bool, error) {
	if stub.succeed != nil {
		return stub.succeed(ctx, id, token, code)
	}
	return stub.Store.Succeed(ctx, id, token, code)
}

type queueObserverFunc func(context.Context, observe.Observation)

func (observer queueObserverFunc) ObserveGolem(ctx context.Context, value observe.Observation) {
	observer(ctx, value)
}

func TestOperatorCancelRejectsInvalidIdentity(t *testing.T) {
	control, err := NewOperator(stubStore{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := control.Cancel(context.Background(), "")
	code, classified := queue.CodeOf(err)
	if changed || !classified || code != queue.CodeConfigInvalid {
		t.Fatalf("cancel changed=%t code=%q classified=%t error=%v", changed, code, classified, err)
	}
}

func register(t *testing.T, registry *queue.Registry, definition queue.Definition[gatePayload]) queue.Type[gatePayload] {
	t.Helper()
	jobType, err := queue.Register(registry, definition)
	if err != nil {
		t.Fatal(err)
	}
	return jobType
}

func newPending(t *testing.T, jobType queue.Type[gatePayload], options ...queue.Option) queue.Pending {
	t.Helper()
	pending, err := jobType.New(gatePayload{Value: "gate"}, options...)
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

func awaitRecord(t *testing.T, fixture *harness, identity, want string, match func(queueprovider.Record) bool) queueprovider.Record {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		record := fixture.inspect(t, identity)
		if match(record) {
			return record
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never became %s, last record %#v", identity, want, fixture.inspect(t, identity))
	return queueprovider.Record{}
}

func awaitState(t *testing.T, fixture *harness, identity string, state queueprovider.State) queueprovider.Record {
	t.Helper()
	return awaitRecord(t, fixture, identity, string(state), func(record queueprovider.Record) bool { return record.State == state })
}

func awaitSignal(t *testing.T, signal <-chan struct{}, reason string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", reason)
	}
}

func gateLimits() queue.Limits {
	return queue.Limits{Concurrency: 4, ClaimBatch: 8, LeaseDuration: time.Second, PollInterval: 10 * time.Millisecond, ShutdownGrace: 2 * time.Second}
}

// TestAttemptIncrementsAtClaimAndPoisonExhausts proves an attempt burned by a
// crashed worker still counts, so a payload that kills the process cannot retry
// forever.
func TestAttemptIncrementsAtClaimAndPoisonExhausts(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	var executions atomic.Int64
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.poison", MaxAttempts: 2, Backoff: queue.Backoff{Base: time.Hour, Cap: time.Hour},
		Handle: func(context.Context, queue.Job[gatePayload]) error {
			executions.Add(1)
			return errors.New("payload kills its worker")
		},
	})
	identity := fixture.enqueue(t, newPending(t, jobType))

	crashed, err := fixture.store.Claim(context.Background(), queueprovider.ClaimOptions{Types: []string{"gate.poison"}, Limit: 1, LeaseDuration: 100 * time.Millisecond})
	if err != nil || len(crashed) != 1 || crashed[0].AttemptCount != 1 {
		t.Fatalf("crashed claim %#v error=%v", crashed, err)
	}
	time.Sleep(250 * time.Millisecond)

	_, stop := fixture.start(t, fixture.store, registry, gateLimits())
	record := awaitState(t, fixture, identity, queueprovider.StateFailed)
	stop()
	if record.LastCode != codeAttemptsExhausted || record.AttemptCount != 2 {
		t.Fatalf("exhausted job %#v", record)
	}
	if executions.Load() != 1 {
		t.Fatalf("handler ran %d times; the crashed attempt was not counted", executions.Load())
	}
}

// TestOutcomeVocabulary proves each handler return lands in its own durable
// state rather than collapsing into another.
func TestOutcomeVocabulary(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	var executed sync.WaitGroup
	executed.Add(5)
	handler := func(result func() error) func(context.Context, queue.Job[gatePayload]) error {
		var once sync.Once
		return func(context.Context, queue.Job[gatePayload]) error {
			once.Do(executed.Done)
			return result()
		}
	}
	slow := queue.Backoff{Base: time.Hour, Cap: time.Hour}
	rows := []struct {
		name   string
		result func() error
		state  queueprovider.State
		code   string
	}{
		{name: "gate.succeed", result: func() error { return nil }, state: queueprovider.StateSucceeded},
		{name: "gate.degraded", result: func() error { return queue.CompletedWith("sprites_partial", errors.New("half")) }, state: queueprovider.StateSucceeded, code: "sprites_partial"},
		{name: "gate.terminal", result: func() error { return queue.Terminal(errors.New("poison")) }, state: queueprovider.StateFailed, code: codeTerminal},
		{name: "gate.retry", result: func() error { return errors.New("transient") }, state: queueprovider.StatePending, code: codeRetry},
		{name: "gate.scheduled", result: func() error { return queue.RetryIn(time.Hour, errors.New("rate limited")) }, state: queueprovider.StatePending, code: codeRetry},
	}
	identities := make([]string, len(rows))
	for index, row := range rows {
		jobType := register(t, registry, queue.Definition[gatePayload]{Type: row.name, MaxAttempts: 50, Backoff: slow, Handle: handler(row.result)})
		identities[index] = fixture.enqueue(t, newPending(t, jobType))
	}

	_, stop := fixture.start(t, fixture.store, registry, gateLimits())
	completed := make(chan struct{})
	go func() { executed.Wait(); close(completed) }()
	awaitSignal(t, completed, "every outcome to be handled")
	for index, row := range rows {
		want, state, code := row.name, row.state, row.code
		awaitRecord(t, fixture, identities[index], want, func(record queueprovider.Record) bool {
			return record.State == state && record.LastCode == code
		})
	}
	stop()

	for index, row := range rows {
		record := fixture.inspect(t, identities[index])
		if record.State != row.state || record.LastCode != row.code {
			t.Fatalf("%s recorded state=%s code=%q", row.name, record.State, record.LastCode)
		}
		if row.name == "gate.scheduled" && record.AvailableAt.Before(time.Now().Add(50*time.Minute)) {
			t.Fatalf("%s was rescheduled at %s rather than the stated horizon", row.name, record.AvailableAt)
		}
	}
}

// TestCancellation proves cancellation is durable state, terminal, and never
// recorded as a failure.
func TestCancellation(t *testing.T) {
	t.Run("pending is immediate and terminal", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type:   "gate.cancel.pending",
			Handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
		})
		identity := fixture.enqueue(t, newPending(t, jobType, queue.After(time.Hour)))
		records := make(chan observe.Observation, 2)
		observer := queueObserverFunc(func(_ context.Context, value observe.Observation) { records <- value })
		control, err := NewOperator(fixture.store, golem.SQLite, observer)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := control.Cancel(context.Background(), queue.JobID(identity))
		if err != nil || !changed {
			t.Fatalf("cancel changed=%t error=%v", changed, err)
		}
		if record := fixture.inspect(t, identity); record.State != queueprovider.StateCanceled || record.FinishedAt == nil {
			t.Fatalf("pending cancel produced %#v", record)
		}
		if len(records) != 1 {
			t.Fatalf("pending cancel observations=%d", len(records))
		}
		observation := <-records
		if observation.Phase() != observe.PhaseCancel || observation.Outcome() != observe.OutcomeCancelled || observation.QueueType() != "gate.cancel.pending" || observation.Attempt() != 0 {
			t.Fatalf("pending cancel observation=%#v", observation)
		}
	})

	t.Run("running cancels the handler and records canceled", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		started := make(chan struct{})
		var causes atomic.Value
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.cancel.running",
			Handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
				close(started)
				<-ctx.Done()
				causes.Store(context.Cause(ctx))
				return ctx.Err()
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		records := make(chan observe.Observation, 4)
		observer := queueObserverFunc(func(_ context.Context, value observe.Observation) { records <- value })
		_, stop := fixture.startObserved(t, fixture.store, registry, gateLimits(), golem.SQLite, observer)
		awaitSignal(t, started, "the handler to start")
		control, err := NewOperator(fixture.store, golem.SQLite, observer)
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := control.Cancel(context.Background(), queue.JobID(identity)); err != nil || !changed {
			t.Fatalf("cancel changed=%t error=%v", changed, err)
		}
		record := awaitState(t, fixture, identity, queueprovider.StateCanceled)
		stop()
		if record.LastCode != codeCanceled {
			t.Fatalf("running cancel recorded %#v", record)
		}
		if cause, _ := causes.Load().(error); !errors.Is(cause, ErrCanceled) {
			t.Fatalf("handler observed cancellation cause %v", cause)
		}
		if len(records) != 2 {
			t.Fatalf("running cancel observations=%d", len(records))
		}
		startedObservation, canceledObservation := <-records, <-records
		if startedObservation.Phase() != observe.PhaseStart || canceledObservation.Phase() != observe.PhaseCancel || canceledObservation.Outcome() != observe.OutcomeCancelled {
			t.Fatalf("running cancel observations=%#v %#v", startedObservation, canceledObservation)
		}
	})

	t.Run("survives the owning worker's restart", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		var executions atomic.Int64
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.cancel.restart",
			Handle: func(context.Context, queue.Job[gatePayload]) error {
				executions.Add(1)
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		claimed, err := fixture.store.Claim(context.Background(), queueprovider.ClaimOptions{Types: []string{"gate.cancel.restart"}, Limit: 1, LeaseDuration: 100 * time.Millisecond})
		if err != nil || len(claimed) != 1 {
			t.Fatalf("crashed owner claim %#v error=%v", claimed, err)
		}
		control, err := NewOperator(fixture.store, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := control.Cancel(context.Background(), queue.JobID(identity)); err != nil || !changed {
			t.Fatalf("cancel of a leased job changed=%t error=%v", changed, err)
		}
		time.Sleep(250 * time.Millisecond)

		_, stop := fixture.start(t, fixture.store, registry, gateLimits())
		record := awaitState(t, fixture, identity, queueprovider.StateCanceled)
		stop()
		if record.LastCode != codeCanceled {
			t.Fatalf("reclaimed cancellation recorded %#v", record)
		}
		if executions.Load() != 0 {
			t.Fatalf("the reclaiming worker executed a canceled job %d times", executions.Load())
		}
	})
}

// TestRenewalFailureCancelsHandlerContext proves a lost lease cancels the
// handler and suppresses its outcome.
func TestRenewalFailureCancelsHandlerContext(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	observed := make(chan error, 1)
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.renewal",
		Handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
			<-ctx.Done()
			observed <- context.Cause(ctx)
			return nil
		},
	})
	identity := fixture.enqueue(t, newPending(t, jobType))
	store := stubStore{Store: fixture.store, renew: func(context.Context, string, string, time.Duration) (queueprovider.Renewal, error) {
		return queueprovider.Renewal{Renewed: false}, nil
	}}
	limits := gateLimits()
	limits.LeaseDuration = 3 * time.Second
	_, stop := fixture.start(t, store, registry, limits)

	var cause error
	select {
	case cause = <-observed:
	case <-time.After(20 * time.Second):
		t.Fatal("the handler context was never canceled by a failed renewal")
	}
	stop()
	if !errors.Is(cause, ErrLeaseLost) {
		t.Fatalf("handler observed cancellation cause %v", cause)
	}
	record := fixture.inspect(t, identity)
	if record.State != queueprovider.StateLeased || record.LastCode != "" || record.FinishedAt != nil {
		t.Fatalf("a worker without its lease recorded an outcome: %#v", record)
	}
}

func TestHostileHandlerCannotTransitionAfterLeaseLoss(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.lease.lost.hostile",
		Handle: func(context.Context, queue.Job[gatePayload]) error {
			close(started)
			<-release
			return nil
		},
	})
	identity := fixture.enqueue(t, newPending(t, jobType))
	renewed := make(chan struct{})
	var renewalOnce sync.Once
	var renewals atomic.Int64
	store := stubStore{Store: fixture.store, renew: func(context.Context, string, string, time.Duration) (queueprovider.Renewal, error) {
		renewals.Add(1)
		renewalOnce.Do(func() { close(renewed) })
		return queueprovider.Renewal{}, nil
	}}
	limits := gateLimits()
	limits.AbandonGrace = 30 * time.Millisecond
	_, stop := fixture.start(t, store, registry, limits)
	awaitSignal(t, started, "the hostile lease-loss handler to start")
	awaitSignal(t, renewed, "the hostile handler to lose its lease")
	time.Sleep(100 * time.Millisecond)
	if renewals.Load() != 1 {
		t.Fatalf("lost lease was renewed %d times", renewals.Load())
	}
	record := fixture.inspect(t, identity)
	if record.State != queueprovider.StateLeased || record.AttemptCount != 1 || record.LastCode != "" || record.FinishedAt != nil {
		t.Fatalf("hostile lease-loss handler recorded %#v", record)
	}
	close(release)
	stop()
}

// TestPerTypeTimeoutIsIndependentOfLease proves the handler deadline and the
// lease are separate clocks.
func TestPerTypeTimeoutIsIndependentOfLease(t *testing.T) {
	t.Run("a short timeout schedules a retry under a long lease", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.timeout", Timeout: 50 * time.Millisecond, MaxAttempts: 50,
			Backoff: queue.Backoff{Base: time.Hour, Cap: time.Hour},
			Handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
				<-ctx.Done()
				return ctx.Err()
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		limits := gateLimits()
		limits.LeaseDuration = 5 * time.Second
		_, stop := fixture.start(t, fixture.store, registry, limits)
		record := awaitRecord(t, fixture, identity, "retryable after its deadline", func(record queueprovider.Record) bool {
			return record.State == queueprovider.StatePending && record.LastCode == codeRetry
		})
		stop()
		if record.AttemptCount != 1 {
			t.Fatalf("timed-out job recorded %#v", record)
		}
	})

	t.Run("a long job survives a short lease on heartbeats", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.heartbeat", Timeout: time.Minute,
			Handle: func(context.Context, queue.Job[gatePayload]) error {
				time.Sleep(2500 * time.Millisecond)
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		limits := gateLimits()
		limits.LeaseDuration = time.Second
		_, stop := fixture.start(t, fixture.store, registry, limits)
		record := awaitState(t, fixture, identity, queueprovider.StateSucceeded)
		stop()
		if record.AttemptCount != 1 {
			t.Fatalf("a heartbeating job was reclaimed: %#v", record)
		}
	})
}

func TestAbandonmentPersistsTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout schedules a retry", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		started := make(chan struct{})
		release := make(chan struct{})
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.abandon.timeout", Timeout: 20 * time.Millisecond, MaxAttempts: 2,
			Backoff: queue.Backoff{Base: time.Hour, Cap: time.Hour},
			Handle: func(context.Context, queue.Job[gatePayload]) error {
				close(started)
				<-release
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		limits := gateLimits()
		limits.AbandonGrace = 30 * time.Millisecond
		_, stop := fixture.start(t, fixture.store, registry, limits)
		awaitSignal(t, started, "the hostile timeout handler to start")
		record := awaitRecord(t, fixture, identity, "retry after abandonment", func(record queueprovider.Record) bool {
			return record.State == queueprovider.StatePending && record.LastCode == codeRetry
		})
		close(release)
		stop()
		if record.AttemptCount != 1 {
			t.Fatalf("abandoned timeout record=%#v", record)
		}
	})

	t.Run("cancellation becomes terminal", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		started := make(chan struct{})
		release := make(chan struct{})
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.abandon.cancel",
			Handle: func(context.Context, queue.Job[gatePayload]) error {
				close(started)
				<-release
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		limits := gateLimits()
		limits.AbandonGrace = 30 * time.Millisecond
		_, stop := fixture.start(t, fixture.store, registry, limits)
		awaitSignal(t, started, "the hostile canceled handler to start")
		control, err := NewOperator(fixture.store, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := control.Cancel(context.Background(), queue.JobID(identity)); err != nil || !changed {
			t.Fatalf("cancel changed=%t error=%v", changed, err)
		}
		record := awaitState(t, fixture, identity, queueprovider.StateCanceled)
		close(release)
		stop()
		if record.LastCode != codeCanceled {
			t.Fatalf("abandoned cancellation record=%#v", record)
		}
	})
}

func TestUncountedRetryPreservesAttemptBudget(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	var executions atomic.Int64
	var attempts sync.Map
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.retry.uncounted", MaxAttempts: 1,
		Handle: func(_ context.Context, job queue.Job[gatePayload]) error {
			index := executions.Add(1)
			attempts.Store(index, job.Attempt)
			if index < 3 {
				return queue.RetryInWithoutAttempt(time.Millisecond, errors.New("external capacity"))
			}
			return nil
		},
	})
	identity := fixture.enqueue(t, newPending(t, jobType))
	_, stop := fixture.start(t, fixture.store, registry, gateLimits())
	record := awaitState(t, fixture, identity, queueprovider.StateSucceeded)
	stop()
	if executions.Load() != 3 || record.AttemptCount != 1 {
		t.Fatalf("executions=%d record=%#v", executions.Load(), record)
	}
	for index := int64(1); index <= 3; index++ {
		if attempt, _ := attempts.Load(index); attempt != 1 {
			t.Fatalf("execution %d observed attempt %v", index, attempt)
		}
	}
}

// TestTypeCapacityGatesClaimWithoutRequeue proves a gated job is never leased,
// so its attempts and queue position are untouched.
func TestTypeCapacityGatesClaimWithoutRequeue(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	admitted := make(chan struct{}, 8)
	release := make(chan struct{})
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.capacity", MaxConcurrent: 1,
		Handle: func(context.Context, queue.Job[gatePayload]) error {
			admitted <- struct{}{}
			<-release
			return nil
		},
	})
	identities := []string{
		fixture.enqueue(t, newPending(t, jobType)),
		fixture.enqueue(t, newPending(t, jobType)),
		fixture.enqueue(t, newPending(t, jobType)),
	}
	_, stop := fixture.start(t, fixture.store, registry, gateLimits())
	awaitSignal(t, admitted, "the first job to be admitted")
	time.Sleep(200 * time.Millisecond)

	leased := 0
	for _, identity := range identities {
		record := fixture.inspect(t, identity)
		switch record.State {
		case queueprovider.StateLeased:
			leased++
		case queueprovider.StatePending:
			if record.AttemptCount != 0 || record.LeaseToken != "" {
				t.Fatalf("gated job was bounced through the database: %#v", record)
			}
		default:
			t.Fatalf("gated job reached %#v", record)
		}
	}
	if leased != 1 || len(admitted) != 0 {
		t.Fatalf("%d jobs held the single-slot type", leased)
	}
	close(release)
	for _, identity := range identities {
		awaitState(t, fixture, identity, queueprovider.StateSucceeded)
	}
	stop()
}

// TestEnqueueWakesIdleWorker proves the in-process wake bypasses the poll floor.
func TestEnqueueWakesIdleWorker(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	executed := make(chan struct{})
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.wake",
		Handle: func(context.Context, queue.Job[gatePayload]) error {
			close(executed)
			return nil
		},
	})
	limits := gateLimits()
	limits.PollInterval = time.Hour
	worker, stop := fixture.start(t, fixture.store, registry, limits)
	time.Sleep(100 * time.Millisecond)

	fixture.enqueue(t, newPending(t, jobType))
	worker.Wake()
	awaitSignal(t, executed, "the woken worker to claim")
	stop()
}

// TestShutdownReleasesUnstartedAndGracesRunning proves a deploy neither strands
// claimed work behind lease expiry nor loses a running job's outcome.
func TestShutdownReleasesUnstartedAndGracesRunning(t *testing.T) {
	t.Run("claimed but unstarted work returns to pending", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		var executions atomic.Int64
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.shutdown.unstarted",
			Handle: func(context.Context, queue.Job[gatePayload]) error {
				executions.Add(1)
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		var stopWorker atomic.Value
		store := stubStore{Store: fixture.store, claim: func(ctx context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
			records, err := fixture.store.Claim(ctx, options)
			if len(records) != 0 {
				if cancel, ok := stopWorker.Load().(context.CancelFunc); ok {
					cancel()
				}
			}
			return records, err
		}}
		worker, err := New(store, registry, gateLimits(), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		stopWorker.Store(cancel)
		defer cancel()
		if err := worker.Run(ctx); err != nil {
			t.Fatal(err)
		}
		record := fixture.inspect(t, identity)
		if record.State != queueprovider.StatePending || record.LeaseToken != "" {
			t.Fatalf("claimed-unstarted job was stranded: %#v", record)
		}
		if !record.AvailableAt.Before(time.Now().Add(time.Second)) {
			t.Fatalf("released job is not immediately claimable: %s", record.AvailableAt)
		}
		if executions.Load() != 0 {
			t.Fatalf("a released job was executed %d times", executions.Load())
		}
	})

	t.Run("running work keeps its outcome through the grace window", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		started := make(chan struct{})
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.shutdown.running",
			Handle: func(context.Context, queue.Job[gatePayload]) error {
				close(started)
				time.Sleep(200 * time.Millisecond)
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		worker, err := New(fixture.store, registry, gateLimits(), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		awaitSignal(t, started, "the handler to start")
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the worker never stopped")
		}
		if record := fixture.inspect(t, identity); record.State != queueprovider.StateSucceeded {
			t.Fatalf("shutdown discarded a completed job's outcome: %#v", record)
		}
	})

	t.Run("context-aware final attempt completes inside grace", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		started := make(chan struct{})
		release := make(chan struct{})
		canceled := make(chan error, 1)
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type:        "gate.shutdown.final",
			MaxAttempts: 1,
			Handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
				close(started)
				select {
				case <-ctx.Done():
					canceled <- context.Cause(ctx)
					return ctx.Err()
				case <-release:
					return nil
				}
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		limits := gateLimits()
		limits.ShutdownGrace = 500 * time.Millisecond
		worker, err := New(fixture.store, registry, limits, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		awaitSignal(t, started, "the final-attempt handler to start")
		cancel()
		select {
		case cause := <-canceled:
			t.Fatalf("handler was canceled before grace expired: %v", cause)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not stop after graceful completion")
		}
		record := fixture.inspect(t, identity)
		if record.State != queueprovider.StateSucceeded || record.AttemptCount != 1 {
			t.Fatalf("graceful shutdown damaged the final attempt: %#v", record)
		}
	})

	t.Run("handler cancellation begins only after grace", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		started := make(chan struct{})
		canceled := make(chan error, 1)
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type:        "gate.shutdown.expired",
			MaxAttempts: 1,
			Handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
				close(started)
				<-ctx.Done()
				canceled <- context.Cause(ctx)
				return ctx.Err()
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		limits := gateLimits()
		limits.ShutdownGrace = 150 * time.Millisecond
		worker, err := New(fixture.store, registry, limits, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		awaitSignal(t, started, "the handler to start")
		startedShutdown := time.Now()
		cancel()
		select {
		case cause := <-canceled:
			t.Fatalf("handler was canceled before grace expired: %v", cause)
		case <-time.After(75 * time.Millisecond):
		}
		select {
		case cause := <-canceled:
			if !errors.Is(cause, errShutdown) {
				t.Fatalf("shutdown cause=%v", cause)
			}
		case <-time.After(time.Second):
			t.Fatal("handler was not canceled after grace expired")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("worker did not stop after forced shutdown")
		}
		if elapsed := time.Since(startedShutdown); elapsed < limits.ShutdownGrace || elapsed > 3*limits.ShutdownGrace {
			t.Fatalf("shutdown duration=%s grace=%s", elapsed, limits.ShutdownGrace)
		}
		record := fixture.inspect(t, identity)
		if record.State != queueprovider.StateLeased || record.LastCode != "" {
			t.Fatalf("forced shutdown recorded a handler failure: %#v", record)
		}
	})
}

func TestQueueLifecycleObservationsFollowDurableTransitions(t *testing.T) {
	t.Run("retry then success", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.observe.retry", MaxAttempts: 2, Backoff: queue.Backoff{Base: time.Millisecond, Cap: time.Millisecond},
			Handle: func(_ context.Context, job queue.Job[gatePayload]) error {
				if job.Attempt == 1 {
					return errors.New("private handler detail")
				}
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		records := make(chan observe.Observation, 8)
		_, stop := fixture.startObserved(t, fixture.store, registry, gateLimits(), golem.SQLite, queueObserverFunc(func(_ context.Context, value observe.Observation) {
			records <- value
		}))
		awaitState(t, fixture, identity, queueprovider.StateSucceeded)
		stop()
		got := make([]observe.Observation, 0, len(records))
		for len(records) != 0 {
			got = append(got, <-records)
		}
		if len(got) != 4 {
			t.Fatalf("observations=%d", len(got))
		}
		wantPhases := []observe.Phase{observe.PhaseStart, observe.PhaseRetry, observe.PhaseStart, observe.PhaseFinish}
		wantOutcomes := []observe.Outcome{observe.OutcomeSuccess, observe.OutcomeRetrying, observe.OutcomeSuccess, observe.OutcomeSuccess}
		wantAttempts := []int{1, 1, 2, 2}
		for index, value := range got {
			if value.Kind() != observe.KindQueue || value.Operation() != observe.OperationQueueExecute || value.QueueType() != "gate.observe.retry" || value.Provider() != golem.SQLite || value.Phase() != wantPhases[index] || value.Outcome() != wantOutcomes[index] || value.Attempt() != wantAttempts[index] || value.Reason() != observe.ReasonNone {
				t.Fatalf("observation %d=%#v", index, value)
			}
		}
	})

	t.Run("attempt exhaustion", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.observe.exhausted", MaxAttempts: 1,
			Handle: func(context.Context, queue.Job[gatePayload]) error { return errors.New("private handler detail") },
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		records := make(chan observe.Observation, 4)
		_, stop := fixture.startObserved(t, fixture.store, registry, gateLimits(), golem.SQLite, queueObserverFunc(func(_ context.Context, value observe.Observation) {
			records <- value
		}))
		awaitState(t, fixture, identity, queueprovider.StateFailed)
		stop()
		if len(records) != 2 {
			t.Fatalf("observations=%d", len(records))
		}
		<-records
		finished := <-records
		if finished.Phase() != observe.PhaseFinish || finished.Outcome() != observe.OutcomeFailure || finished.Reason() != observe.ReasonLimit || finished.Attempt() != 1 {
			t.Fatalf("exhaustion observation=%#v", finished)
		}
	})

	t.Run("timeout retry", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.observe.timeout", MaxAttempts: 2, Timeout: 20 * time.Millisecond,
			Backoff: queue.Backoff{Base: time.Hour, Cap: time.Hour},
			Handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
				<-ctx.Done()
				return errors.New("private timeout detail")
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		retried := make(chan struct{}, 1)
		_, stop := fixture.startObserved(t, fixture.store, registry, gateLimits(), golem.SQLite, queueObserverFunc(func(_ context.Context, value observe.Observation) {
			if value.Phase() == observe.PhaseRetry && value.Reason() == observe.ReasonTimeout {
				retried <- struct{}{}
			}
		}))
		awaitRecord(t, fixture, identity, "retry after timeout", func(record queueprovider.Record) bool {
			return record.State == queueprovider.StatePending && record.LastCode == codeRetry
		})
		awaitSignal(t, retried, "the timeout observation")
		stop()
	})

	t.Run("lost fence emits no false finish", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		executed := make(chan struct{})
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type:   "gate.observe.fenced",
			Handle: func(context.Context, queue.Job[gatePayload]) error { close(executed); return nil },
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		records := make(chan observe.Observation, 4)
		store := stubStore{Store: fixture.store, succeed: func(context.Context, string, string, string) (bool, error) { return false, nil }}
		_, stop := fixture.startObserved(t, store, registry, gateLimits(), golem.SQLite, queueObserverFunc(func(_ context.Context, value observe.Observation) {
			records <- value
		}))
		awaitSignal(t, executed, "the fenced handler to execute")
		time.Sleep(25 * time.Millisecond)
		stop()
		if len(records) != 1 || (<-records).Phase() != observe.PhaseStart {
			t.Fatalf("lost fence observations=%d job=%#v", len(records), fixture.inspect(t, identity))
		}
	})

	t.Run("observer panic is correctness-neutral", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		jobType := register(t, registry, queue.Definition[gatePayload]{Type: "gate.observe.panic", Handle: func(context.Context, queue.Job[gatePayload]) error { return nil }})
		identity := fixture.enqueue(t, newPending(t, jobType))
		_, stop := fixture.startObserved(t, fixture.store, registry, gateLimits(), golem.SQLite, queueObserverFunc(func(context.Context, observe.Observation) { panic("observer") }))
		awaitState(t, fixture, identity, queueprovider.StateSucceeded)
		stop()
	})

	t.Run("blocking observer cannot delay execution", func(t *testing.T) {
		fixture := newHarness(t)
		registry := queue.NewRegistry()
		entry := make(chan error, 1)
		jobType := register(t, registry, queue.Definition[gatePayload]{
			Type: "gate.observe.blocking", Timeout: 50 * time.Millisecond,
			Handle: func(ctx context.Context, _ queue.Job[gatePayload]) error {
				entry <- ctx.Err()
				return nil
			},
		})
		identity := fixture.enqueue(t, newPending(t, jobType))
		_, stop := fixture.startObserved(t, fixture.store, registry, gateLimits(), golem.SQLite, queueObserverFunc(func(ctx context.Context, _ observe.Observation) {
			<-ctx.Done()
		}))
		select {
		case err := <-entry:
			if err != nil {
				t.Fatalf("observer delayed handler past its deadline: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("blocking observer prevented handler start")
		}
		awaitState(t, fixture, identity, queueprovider.StateSucceeded)
		stop()
	})
}

// TestOperatorSurface proves each operator action touches only the state it
// owns.
func TestOperatorSurface(t *testing.T) {
	fixture := newHarness(t)
	control, err := NewOperator(fixture.store, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	registry := queue.NewRegistry()
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type:   "gate.operator",
		Handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
	})

	_, err = control.Inspect(ctx, queue.JobID("absent"))
	if code, ok := queue.CodeOf(err); !ok || code != queue.CodeJobNotFound {
		t.Fatalf("inspect of an absent job returned %v", err)
	}

	failed := fixture.enqueue(t, newPending(t, jobType))
	leased, err := fixture.store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.operator"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(leased) != 1 {
		t.Fatalf("claim %#v error=%v", leased, err)
	}
	if changed, err := fixture.store.Fail(ctx, failed, leased[0].LeaseToken, codeTerminal); err != nil || !changed {
		t.Fatalf("fail changed=%t error=%v", changed, err)
	}
	page, err := control.ListFailed(ctx, queue.FailedQuery{Types: []string{"gate.operator"}, Limit: 1})
	if err != nil || len(page.Jobs) != 1 || page.Jobs[0].ID != queue.JobID(failed) || page.Jobs[0].State != queue.StateFailed || page.Jobs[0].FinishedAt == nil {
		t.Fatalf("failed page=%#v error=%v", page, err)
	}
	if _, err := control.ListFailed(ctx, queue.FailedQuery{}); func() bool {
		code, ok := queue.CodeOf(err)
		return !ok || code != queue.CodeConfigInvalid
	}() {
		t.Fatalf("unbounded failed query returned %v", err)
	}
	if changed, err := control.RequeueFailed(ctx, []queue.JobID{queue.JobID(failed)}); err != nil || changed != 1 {
		t.Fatalf("requeue failed changed=%d error=%v", changed, err)
	}
	status, err := control.Inspect(ctx, queue.JobID(failed))
	if err != nil || status.State != queue.StatePending || status.Attempt != 0 || status.FinishedAt != nil {
		t.Fatalf("requeued job %#v error=%v", status, err)
	}
	if changed, err := control.Requeue(ctx, queue.JobID(failed)); err != nil || changed {
		t.Fatalf("requeue touched a pending job: changed=%t error=%v", changed, err)
	}

	pending := fixture.enqueue(t, newPending(t, jobType, queue.After(time.Hour)))
	deleted, err := control.RunRetention(ctx, queue.RetentionPolicy{OlderThan: time.Now().Add(time.Hour), MaxRows: 1})
	if err != nil || deleted != 0 {
		t.Fatalf("retention deleted %d rows with no terminal work: %v", deleted, err)
	}
	terminal, err := fixture.store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.operator"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(terminal) != 1 {
		t.Fatalf("claim %#v error=%v", terminal, err)
	}
	if changed, err := fixture.store.Succeed(ctx, terminal[0].ID, terminal[0].LeaseToken, ""); err != nil || !changed {
		t.Fatalf("succeed changed=%t error=%v", changed, err)
	}
	if deleted, err := control.RunRetention(ctx, queue.RetentionPolicy{OlderThan: time.Now().Add(time.Hour), MaxRows: 1}); err != nil || deleted != 1 {
		t.Fatalf("retention deleted %d terminal rows: %v", deleted, err)
	}
	if _, err := control.Inspect(ctx, queue.JobID(pending)); err != nil {
		t.Fatalf("retention removed non-terminal work: %v", err)
	}
}

// TestStartupValidation proves every configuration refusal carries its typed
// code before any background work exists.
func TestStartupValidation(t *testing.T) {
	fixture := newHarness(t)
	populated := queue.NewRegistry()
	register(t, populated, queue.Definition[gatePayload]{
		Type:   "gate.validation",
		Handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
	})
	rows := []struct {
		name     string
		store    queueprovider.Store
		registry *queue.Registry
		limits   queue.Limits
	}{
		{name: "absent store", registry: populated},
		{name: "absent registry", store: fixture.store},
		{name: "empty registry", store: fixture.store, registry: queue.NewRegistry()},
		{name: "negative concurrency", store: fixture.store, registry: populated, limits: queue.Limits{Concurrency: -1}},
		{name: "lease below the floor", store: fixture.store, registry: populated, limits: queue.Limits{LeaseDuration: time.Millisecond}},
		{name: "oversized claim batch", store: fixture.store, registry: populated, limits: queue.Limits{ClaimBatch: queueprovider.MaximumClaimJobs + 1}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			worker, err := New(row.store, row.registry, row.limits, "", nil)
			if worker != nil {
				t.Fatalf("refused configuration produced a worker")
			}
			if code, ok := queue.CodeOf(err); !ok || code != queue.CodeConfigInvalid {
				t.Fatalf("refusal reported %v", err)
			}
		})
	}
	worker, err := New(fixture.store, populated, gateLimits(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	running := make(chan error, 1)
	go func() { running <- worker.Run(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for !worker.running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	second := worker.Run(ctx)
	cancel()
	<-running
	if code, ok := queue.CodeOf(second); !ok || code != queue.CodeWorkerRunning {
		t.Fatalf("a second concurrent Run reported %v", second)
	}
}

func TestSharedResourceConfigurationIsClosedAndUnambiguous(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	for _, name := range []string{"gate.resource.one", "gate.resource.two"} {
		register(t, registry, queue.Definition[gatePayload]{Type: name, Handle: func(context.Context, queue.Job[gatePayload]) error { return nil }})
	}
	rows := []struct {
		name      string
		resources []queue.Resource
	}{
		{name: "empty costs", resources: []queue.Resource{{Name: "gate.resource", Concurrency: 1}}},
		{name: "uncanonical name", resources: []queue.Resource{{Name: "Gate", Concurrency: 1, Costs: map[string]int{"gate.resource.one": 1}}}},
		{name: "zero concurrency", resources: []queue.Resource{{Name: "gate.resource", Costs: map[string]int{"gate.resource.one": 1}}}},
		{name: "cost above concurrency", resources: []queue.Resource{{Name: "gate.resource", Concurrency: 1, Costs: map[string]int{"gate.resource.one": 2}}}},
		{name: "unregistered type", resources: []queue.Resource{{Name: "gate.resource", Concurrency: 1, Costs: map[string]int{"gate.resource.absent": 1}}}},
		{name: "repeated resource", resources: []queue.Resource{
			{Name: "gate.resource", Concurrency: 1, Costs: map[string]int{"gate.resource.one": 1}},
			{Name: "gate.resource", Concurrency: 1, Costs: map[string]int{"gate.resource.two": 1}},
		}},
		{name: "type in two resources", resources: []queue.Resource{
			{Name: "gate.resource.one", Concurrency: 1, Costs: map[string]int{"gate.resource.one": 1}},
			{Name: "gate.resource.two", Concurrency: 1, Costs: map[string]int{"gate.resource.one": 1}},
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			worker, err := New(fixture.store, registry, gateLimits(), "", nil, row.resources...)
			if worker != nil {
				t.Fatal("refused resource configuration produced a worker")
			}
			if code, ok := queue.CodeOf(err); !ok || code != queue.CodeConfigInvalid {
				t.Fatalf("resource refusal reported %v", err)
			}
		})
	}
}

func TestDispatchCarriesTheCompleteResourcePlan(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	for _, name := range []string{"gate.resource.one", "gate.resource.two", "gate.unpooled"} {
		register(t, registry, queue.Definition[gatePayload]{Type: name, Handle: func(context.Context, queue.Job[gatePayload]) error { return nil }})
	}
	costs := map[string]int{"gate.resource.one": 2, "gate.resource.two": 1}
	var claims []queueprovider.ClaimOptions
	store := stubStore{Store: fixture.store, claim: func(_ context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
		claims = append(claims, options)
		return nil, nil
	}}
	worker, err := New(store, registry, gateLimits(), "", nil, queue.Resource{Name: "gate.shared", Concurrency: 3, Costs: costs})
	if err != nil {
		t.Fatal(err)
	}
	costs["gate.resource.one"] = 3
	if _, err := worker.dispatch(context.Background(), context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 {
		t.Fatalf("claim groups=%#v", claims)
	}
	var pooled, unpooled *queueprovider.ClaimOptions
	for index := range claims {
		if claims[index].Resource == nil {
			unpooled = &claims[index]
		} else {
			pooled = &claims[index]
		}
	}
	if unpooled == nil || len(unpooled.Types) != 1 || unpooled.Types[0] != "gate.unpooled" {
		t.Fatalf("unpooled claim=%#v", unpooled)
	}
	if pooled == nil || pooled.Resource.Name != "gate.shared" || pooled.Resource.Concurrency != 3 || len(pooled.Types) != 2 || pooled.Types[0] != "gate.resource.one" || pooled.Types[1] != "gate.resource.two" || len(pooled.Resource.Costs) != 2 || pooled.Resource.Costs["gate.resource.one"] != 2 || pooled.Resource.Costs["gate.resource.two"] != 1 {
		t.Fatalf("pooled claim=%#v", pooled)
	}
}
