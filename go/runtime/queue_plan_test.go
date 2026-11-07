package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/queue"
)

func TestQueuePlanEntryPointsRefuseAnUnconfiguredApplication(t *testing.T) {
	fixture := openTransactionFixture(t)
	if _, err := fixture.app.ReconfigureQueue(context.Background(), queue.Plan{Concurrency: 2}); func() bool {
		code, ok := queue.CodeOf(err)
		return !ok || code != queue.CodeConfigInvalid
	}() {
		t.Fatalf("ReconfigureQueue on an unconfigured application reported %v", err)
	}
	if _, err := fixture.app.QueuePlan(); func() bool {
		code, ok := queue.CodeOf(err)
		return !ok || code != queue.CodeConfigInvalid
	}() {
		t.Fatalf("QueuePlan on an unconfigured application reported %v", err)
	}
}

func TestReconfigureQueueRaisesALiveWorkerWithoutRestart(t *testing.T) {
	registry := queue.NewRegistry()
	admitted := make(chan struct{}, 16)
	release := make(chan struct{})
	var current atomic.Int64
	jobType, err := queue.Register(registry, queue.Definition[queueGatePayload]{
		Type: "gate.analyze", MaxConcurrent: 2,
		Handle: func(context.Context, queue.Job[queueGatePayload]) error {
			current.Add(1)
			admitted <- struct{}{}
			<-release
			current.Add(-1)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	limits := queue.DefaultLimits()
	limits.Concurrency = 16
	limits.ClaimBatch = 16
	fixture := openTransactionFixtureWith(t, &QueueConfig{
		Registry: registry, Limits: limits,
		Resources: []queue.Resource{{Name: "gate.analyze_pool", Concurrency: 2, Costs: map[string]int{"gate.analyze": 1}}},
	})
	initial, err := fixture.app.QueuePlan()
	if err != nil || initial.Generation != 1 || initial.Capacities["gate.analyze_pool"] != 2 || initial.MaxConcurrent["gate.analyze"] != 2 {
		t.Fatalf("initial plan = %#v, %v", initial, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan error, 1)
	go func() { running <- fixture.app.RunQueueWorker(ctx) }()
	for range 10 {
		if _, err := fixture.app.Enqueue(ctx, newQueueGatePending(t, jobType)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		awaitAdmitted(t, admitted)
	}
	status, err := fixture.app.ReconfigureQueue(ctx, queue.Plan{
		MaxConcurrent: map[string]int{"gate.analyze": 8}, Capacities: map[string]int{"gate.analyze_pool": 8},
	})
	if err != nil || status.Generation != 2 {
		t.Fatalf("ReconfigureQueue = %#v, %v", status, err)
	}
	if reported, _ := fixture.app.QueuePlan(); reported.Generation != 2 || reported.Capacities["gate.analyze_pool"] != 8 {
		t.Fatalf("QueuePlan after the raise = %#v", reported)
	}
	if _, err := fixture.app.ReconfigureQueue(ctx, queue.Plan{Capacities: map[string]int{"gate.analyze_pool": 0}}); err == nil {
		t.Fatal("a malformed plan was accepted")
	}
	if reported, _ := fixture.app.QueuePlan(); reported.Generation != 2 {
		t.Fatalf("a refused plan changed the generation to %d", reported.Generation)
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 8 {
		awaitAdmitted(t, admitted)
	}
	if current.Load() != 8 {
		t.Fatalf("%d jobs ran at once after the raise", current.Load())
	}
	for range 8 {
		release <- struct{}{}
	}
	cancel()
	if err := <-running; err != nil {
		t.Fatal(err)
	}
}

func awaitAdmitted(t *testing.T, admitted <-chan struct{}) {
	t.Helper()
	select {
	case <-admitted:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for a job to be admitted")
	}
}
