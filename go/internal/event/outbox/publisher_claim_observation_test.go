package outbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type claimObservationCoordinator struct {
	publisherTestCoordinator
	lease     eventprovider.Lease
	calls     atomic.Int64
	requested chan int
	settled   chan struct{}
}

func (coordinator *claimObservationCoordinator) Claim(ctx context.Context, options eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	switch coordinator.calls.Add(1) {
	case 1:
		coordinator.requested <- options.Groups
		return nil, errors.New("temporary claim outage")
	case 2:
		coordinator.requested <- options.Groups
		return []eventprovider.Lease{coordinator.lease}, nil
	}
	close(coordinator.settled)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPublisherReportsTheClaimSizeItRequests(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &claimObservationCoordinator{
		publisherTestCoordinator: publisherTestCoordinator{renewed: true},
		lease:                    publisherValidLease(t, fixture),
		requested:                make(chan int, 2),
		settled:                  make(chan struct{}),
	}
	observer := &publisherCaptureObserver{}
	publisher, err := NewPublisherObserved(coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{}, Limits{
		ClaimGroups: 4, Concurrency: 2, LeaseDuration: time.Second, PublishTimeout: time.Second,
		RetryBase: time.Millisecond, RetryCap: time.Second, ShutdownGrace: 20 * time.Millisecond,
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	<-coordinator.settled
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	requested := []int{<-coordinator.requested, <-coordinator.requested}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	var limits []int
	for _, observation := range observer.observations {
		if observation.Kind() == events.ObservationPublisherClaim {
			limits = append(limits, observation.QueueLimit())
		}
	}
	if len(limits) != 2 || limits[0] != requested[0] || limits[1] != requested[1] || requested[1] != 2 {
		t.Fatalf("claim observation limits=%v requested=%v", limits, requested)
	}
}
