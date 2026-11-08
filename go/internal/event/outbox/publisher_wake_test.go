package outbox

import (
	"context"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type wakeClaimCoordinator struct {
	publisherTestCoordinator
	claims chan struct{}
}

func (coordinator *wakeClaimCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	select {
	case coordinator.claims <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

func TestPublisherWakeClaimsBeforeTheIdlePollElapses(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	coordinator := &wakeClaimCoordinator{claims: make(chan struct{})}
	publisher, err := NewPublisher(coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{}, Limits{
		LeaseDuration: time.Second, PublishTimeout: time.Second,
		RetryBase: time.Minute, RetryCap: time.Minute, ShutdownGrace: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	awaitClaim := func(label string) {
		t.Helper()
		select {
		case <-coordinator.claims:
		case <-ctx.Done():
			t.Fatalf("%s claim never happened before the one-minute idle poll", label)
		}
	}
	awaitClaim("first")
	publisher.Wake()
	publisher.Wake()
	awaitClaim("woken")
	publisher.Wake()
	awaitClaim("second woken")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
