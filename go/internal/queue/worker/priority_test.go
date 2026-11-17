package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/queue"
)

func TestPriorityGivesNormalAnOpportunityAfterThreeInteractiveAdmissions(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	started := make(chan queue.Priority, 4)
	release := make(chan struct{})
	jobType := register(t, registry, queue.Definition[gatePayload]{Type: "gate.priority.fair", Handle: func(ctx context.Context, job queue.Job[gatePayload]) error {
		started <- job.Priority
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	fixture.enqueue(t, newPending(t, jobType))
	for range 8 {
		fixture.enqueue(t, newPending(t, jobType, queue.WithPriority(queue.PriorityInteractive)))
	}
	limits := gateLimits()
	limits.Concurrency, limits.ClaimBatch = 1, 1
	worker, err := New(fixture.store, registry, limits, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var handlers sync.WaitGroup
	defer func() { cancel(); handlers.Wait() }()
	for turn, want := range []queue.Priority{queue.PriorityInteractive, queue.PriorityInteractive, queue.PriorityInteractive, queue.PriorityNormal} {
		if count, err := worker.dispatch(ctx, ctx, nil, &handlers); err != nil || count != 1 {
			t.Fatalf("turn %d: count=%d error=%v", turn, count, err)
		}
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("turn %d: priority=%d want=%d", turn, got, want)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("handler did not start")
		}
		if count, err := worker.dispatch(ctx, ctx, nil, &handlers); err != nil || count != 0 {
			t.Fatalf("occupied turn: count=%d error=%v", count, err)
		}
		release <- struct{}{}
		handlers.Wait()
	}
}

func TestReservedInteractiveLeavesNormalSlotFreeWithoutPreemption(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	started := make(chan queue.Priority, 2)
	release := make(chan struct{})
	jobType := register(t, registry, queue.Definition[gatePayload]{Type: "gate.priority.reserved", Handle: func(ctx context.Context, job queue.Job[gatePayload]) error {
		started <- job.Priority
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	for range 3 {
		fixture.enqueue(t, newPending(t, jobType))
	}
	limits := gateLimits()
	limits.Concurrency, limits.ReservedInteractive = 2, 1
	worker, err := New(fixture.store, registry, limits, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var handlers sync.WaitGroup
	defer func() { cancel(); handlers.Wait() }()
	for _, want := range []queue.Priority{queue.PriorityNormal, queue.PriorityInteractive} {
		if want == queue.PriorityInteractive {
			fixture.enqueue(t, newPending(t, jobType, queue.WithPriority(want)))
		}
		if count, err := worker.dispatch(ctx, ctx, nil, &handlers); err != nil || count != 1 {
			t.Fatalf("priority %d: count=%d error=%v", want, count, err)
		}
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("priority=%d want=%d", got, want)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("handler did not start")
		}
	}
	if count, err := worker.dispatch(ctx, ctx, nil, &handlers); err != nil || count != 0 {
		t.Fatalf("full worker admitted work: count=%d error=%v", count, err)
	}
	if _, err := worker.Apply(ctx, queue.Plan{Concurrency: 1}); err == nil {
		t.Fatal("live plan removed every unreserved slot")
	}
	close(release)
	handlers.Wait()
}

func TestPriorityBatchedAdmissionsPreserveThreeToOneOpportunities(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	started := make(chan queue.Priority, 8)
	release := make(chan struct{})
	jobType := register(t, registry, queue.Definition[gatePayload]{Type: "gate.priority.batch", Handle: func(ctx context.Context, job queue.Job[gatePayload]) error {
		started <- job.Priority
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	for range 8 {
		fixture.enqueue(t, newPending(t, jobType))
		fixture.enqueue(t, newPending(t, jobType, queue.WithPriority(queue.PriorityInteractive)))
	}
	limits := gateLimits()
	limits.Concurrency, limits.ClaimBatch = 8, 8
	worker, err := New(fixture.store, registry, limits, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var handlers sync.WaitGroup
	defer func() { cancel(); handlers.Wait() }()
	for round := 0; round < 2; round++ {
		if count, err := worker.dispatch(ctx, ctx, nil, &handlers); err != nil || count != 4 {
			t.Fatalf("mixed-work round %d: admitted=%d want=4 error=%v", round, count, err)
		}
		counts := map[queue.Priority]int{}
		for range 4 {
			select {
			case priority := <-started:
				counts[priority]++
			case <-time.After(20 * time.Second):
				t.Fatal("claimed handler did not start")
			}
		}
		if counts[queue.PriorityInteractive] != 3 || counts[queue.PriorityNormal] != 1 {
			t.Fatalf("mixed-work round %d: priority counts=%v", round, counts)
		}
	}
	close(release)
	handlers.Wait()
}
