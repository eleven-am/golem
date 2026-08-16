package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/queue"
)

type queueGatePayload struct {
	Title string `json:"title"`
}

func openQueueFixture(t *testing.T) (transactionFixture, queue.Type[queueGatePayload]) {
	t.Helper()
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[queueGatePayload]{
		Type:   "gate.index_post",
		Handle: func(context.Context, queue.Job[queueGatePayload]) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return openTransactionFixtureWith(t, &QueueConfig{Registry: registry, Limits: queue.DefaultLimits()}), jobType
}

func newQueueGatePending(t *testing.T, jobType queue.Type[queueGatePayload]) queue.Pending {
	t.Helper()
	pending, err := jobType.New(queueGatePayload{Title: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

// TestTransactionalEnqueueJoinsTheCallerTransaction proves the job row commits
// and rolls back with the domain write rather than escaping to the pool.
func TestTransactionalEnqueueJoinsTheCallerTransaction(t *testing.T) {
	fixture, jobType := openQueueFixture(t)
	ctx := context.Background()
	caller, err := fixture.app.ForPrincipal(ctx, testPrincipal{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	operator := fixture.app.QueueOperator()
	if operator == nil {
		t.Fatal("a configured queue exposes no operator")
	}

	refusal := errors.New("caller refused the transaction")
	var rolledBack queue.JobID
	if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		identity, enqueueErr := CallerTxEnqueue(ctx, transaction, newQueueGatePending(t, jobType))
		if enqueueErr != nil {
			return enqueueErr
		}
		rolledBack = identity
		return refusal
	}); !errors.Is(err, refusal) {
		t.Fatalf("rolled-back transaction reported %v", err)
	}
	if _, err := operator.Inspect(ctx, rolledBack); func() bool {
		code, ok := queue.CodeOf(err)
		return !ok || code != queue.CodeJobNotFound
	}() {
		t.Fatalf("a rolled-back enqueue survived: %v", err)
	}

	var committed queue.JobID
	if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[testPrincipal, testActor]) error {
		identity, enqueueErr := CallerTxEnqueue(ctx, transaction, newQueueGatePending(t, jobType))
		committed = identity
		return enqueueErr
	}); err != nil {
		t.Fatal(err)
	}
	status, err := operator.Inspect(ctx, committed)
	if err != nil || status.State != queue.StatePending || status.Type != "gate.index_post" {
		t.Fatalf("committed enqueue produced %#v error=%v", status, err)
	}

	system := fixture.app.System()
	var systemJob queue.JobID
	if err := SystemTransaction(ctx, system, func(transaction *SystemTx[testPrincipal, testActor]) error {
		identity, enqueueErr := SystemTxEnqueue(ctx, transaction, newQueueGatePending(t, jobType))
		systemJob = identity
		return enqueueErr
	}); err != nil {
		t.Fatal(err)
	}
	if status, err := operator.Inspect(ctx, systemJob); err != nil || status.State != queue.StatePending {
		t.Fatalf("system enqueue produced %#v error=%v", status, err)
	}
}

// TestQueueEntryPointsRefuseAnUnconfiguredApplication proves an application
// that never enabled the queue carries no durable job surface.
func TestQueueEntryPointsRefuseAnUnconfiguredApplication(t *testing.T) {
	fixture := openTransactionFixture(t)
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[queueGatePayload]{
		Type:   "gate.absent",
		Handle: func(context.Context, queue.Job[queueGatePayload]) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := jobType.New(queueGatePayload{Title: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := fixture.app.Enqueue(ctx, pending); func() bool {
		code, ok := queue.CodeOf(err)
		return !ok || code != queue.CodeConfigInvalid
	}() {
		t.Fatalf("Enqueue on an unconfigured application reported %v", err)
	}
	if err := fixture.app.RunQueueWorker(ctx); func() bool {
		code, ok := queue.CodeOf(err)
		return !ok || code != queue.CodeConfigInvalid
	}() {
		t.Fatalf("RunQueueWorker on an unconfigured application reported %v", err)
	}
	if fixture.app.QueueOperator() != nil {
		t.Fatal("an unconfigured application exposes a queue operator")
	}
}

// TestRunQueueWorkerRefusesASecondConcurrentRun proves the worker lifecycle
// matches the event publisher's.
func TestRunQueueWorkerRefusesASecondConcurrentRun(t *testing.T) {
	fixture, jobType := openQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	running := make(chan error, 1)
	go func() { running <- fixture.app.RunQueueWorker(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for !fixture.app.queueRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	second := fixture.app.RunQueueWorker(ctx)
	if _, err := fixture.app.Enqueue(ctx, newQueueGatePending(t, jobType)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-running; err != nil {
		t.Fatal(err)
	}
	if code, ok := queue.CodeOf(second); !ok || code != queue.CodeWorkerRunning {
		t.Fatalf("a second concurrent RunQueueWorker reported %v", second)
	}
}
