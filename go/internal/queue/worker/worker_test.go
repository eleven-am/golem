package worker

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
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
	return stored.ID
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
	t.Helper()
	worker, err := New(store, registry, limits)
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
	renew func(context.Context, string, string, time.Duration) (queueprovider.Renewal, error)
	claim func(context.Context, queueprovider.ClaimOptions) ([]queueprovider.Record, error)
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
		control, err := NewOperator(fixture.store)
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
		_, stop := fixture.start(t, fixture.store, registry, gateLimits())
		awaitSignal(t, started, "the handler to start")
		control, err := NewOperator(fixture.store)
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
		control, err := NewOperator(fixture.store)
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
		worker, err := New(store, registry, gateLimits())
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
		worker, err := New(fixture.store, registry, gateLimits())
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
}

// TestOperatorSurface proves each operator action touches only the state it
// owns.
func TestOperatorSurface(t *testing.T) {
	fixture := newHarness(t)
	control, err := NewOperator(fixture.store)
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
	if changed, err := control.Requeue(ctx, queue.JobID(failed)); err != nil || !changed {
		t.Fatalf("requeue changed=%t error=%v", changed, err)
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
			worker, err := New(row.store, row.registry, row.limits)
			if worker != nil {
				t.Fatalf("refused configuration produced a worker")
			}
			if code, ok := queue.CodeOf(err); !ok || code != queue.CodeConfigInvalid {
				t.Fatalf("refusal reported %v", err)
			}
		})
	}
	worker, err := New(fixture.store, populated, gateLimits())
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
