package outbox

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type retentionCountingCoordinator struct {
	publisherTestCoordinator
	passes atomic.Int64
	claims chan struct{}
}

func (coordinator *retentionCountingCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	select {
	case coordinator.claims <- struct{}{}:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (coordinator *retentionCountingCoordinator) RunRetention(context.Context, eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	coordinator.passes.Add(1)
	return eventprovider.RetentionResult{}, nil
}

func retentionPassesAcrossClaims(t *testing.T, configured events.Limits, every time.Duration) int64 {
	t.Helper()
	fixture := schematest.NewSubscribedIndexed(t)
	normalized, err := events.NormalizeLimits(configured)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &retentionCountingCoordinator{claims: make(chan struct{})}
	publisher, err := NewPublisher(coordinator, publisherTestResolver{fixture.Registry}, &captureTransport{}, Limits{
		ClaimGroups: normalized.ClaimRows, Concurrency: normalized.PublisherConcurrency,
		LeaseDuration: normalized.LeaseDuration, PublishTimeout: normalized.PublishTimeout,
		RetryBase: time.Millisecond, RetryCap: time.Second,
		ShutdownGrace: normalized.ShutdownGrace, MaxEncodedBytes: normalized.MaxEncodedEventBytes,
		MaxBatchBytes: normalized.MaxEncodedBatchBytes,
		RetentionAge:  normalized.RetentionAge, RetentionEvery: normalized.RetentionEvery,
		RetentionRows: normalized.RetentionDeleteRows,
	})
	if err != nil {
		t.Fatal(err)
	}
	if every != 0 {
		publisher.limits.RetentionEvery = every
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	for claim := 0; claim < 3; claim++ {
		<-coordinator.claims
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return coordinator.passes.Load()
}

func TestPublisherRunsNoRetentionWhenDisabled(t *testing.T) {
	if passes := retentionPassesAcrossClaims(t, events.Limits{RetentionEvery: events.RetentionDisabled}, 0); passes != 0 {
		t.Fatalf("disabled retention ran %d passes", passes)
	}
}

func TestPublisherRunsRetentionWhenEnabled(t *testing.T) {
	if passes := retentionPassesAcrossClaims(t, events.Limits{}, time.Microsecond); passes == 0 {
		t.Fatal("enabled retention never ran")
	}
}
