package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

// A type cap limits occupancy; it must not put that type permanently behind
// uncapped types whenever the worker has a single available handler slot.
func TestDispatchRotatesCappedCohortUnderContinuousUncappedWork(t *testing.T) {
	fixture := newHarness(t)
	registry := queue.NewRegistry()
	started := make(chan queue.JobID, 2)
	release := make(chan struct{})
	handle := func(ctx context.Context, job queue.Job[gatePayload]) error {
		started <- job.ID
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	capped := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.rotation.capped", MaxConcurrent: 1, Handle: handle,
	})
	uncapped := register(t, registry, queue.Definition[gatePayload]{
		Type: "gate.rotation.uncapped", Handle: handle,
	})
	// The capped job is already due and has no competing lease, resource, or
	// exclusive key. Uncapped work remains available throughout both turns.
	cappedID := fixture.enqueue(t, newPending(t, capped))
	var uncappedIDs []string
	for range 4 {
		uncappedIDs = append(uncappedIDs, fixture.enqueue(t, newPending(t, uncapped)))
	}
	limits := gateLimits()
	limits.Concurrency = 1
	limits.ClaimBatch = 1
	worker, err := New(fixture.store, registry, limits, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var handlers sync.WaitGroup
	defer func() {
		cancel()
		handlers.Wait()
	}()

	var admitted []string
	for turn := 0; turn < 2; turn++ {
		claimed, err := worker.dispatch(ctx, ctx, nil, &handlers)
		if err != nil || claimed != 1 {
			t.Fatalf("turn %d: claimed=%d, error=%v", turn, claimed, err)
		}
		select {
		case id := <-started:
			admitted = append(admitted, string(id))
		case <-time.After(20 * time.Second):
			t.Fatalf("turn %d: claimed handler did not start", turn)
		}
		// Keep the slot occupied until dispatch has returned: otherwise a fast
		// uncapped handler could finish mid-dispatch and conceal starvation.
		// Run retries dispatch after a successful claim before waiting for a
		// completion; that full-capacity turn must not consume the rotation.
		if claimed, err := worker.dispatch(ctx, ctx, nil, &handlers); err != nil || claimed != 0 {
			t.Fatalf("turn %d while occupied: claimed=%d, error=%v", turn, claimed, err)
		}
		release <- struct{}{}
		handlers.Wait()
	}
	remaining := 0
	for _, id := range uncappedIDs {
		if fixture.inspect(t, id).State == queueprovider.StatePending {
			remaining++
		}
	}
	if remaining < 2 {
		t.Fatalf("uncapped work was not continuously available: %d pending", remaining)
	}
	if admitted[0] != cappedID && admitted[1] != cappedID {
		record := fixture.inspect(t, cappedID)
		t.Fatalf("eligible capped job received no rotating opportunity in two turns: admitted=%v, capped state=%s attempts=%d, uncapped pending=%d", admitted, record.State, record.AttemptCount, remaining)
	}
	record := fixture.inspect(t, cappedID)
	if record.State != queueprovider.StateSucceeded || record.AttemptCount != 1 {
		t.Fatalf("capped handler did not complete its first claim: %#v", record)
	}
}
