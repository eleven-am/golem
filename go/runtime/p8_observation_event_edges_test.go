package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/event/outbox"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	"github.com/eleven-am/golem/go/internal/subscription"
	"github.com/eleven-am/golem/go/observe"
)

type p8CoverageCoordinator struct {
	mu       sync.Mutex
	lease    eventprovider.Lease
	issued   bool
	mode     string
	cancel   context.CancelFunc
	depth    eventprovider.DepthSnapshot
	retained eventprovider.RetentionResult
}

func (coordinator *p8CoverageCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	coordinator.mu.Lock()
	if !coordinator.issued && coordinator.lease.Delivery.CausationID != "" {
		coordinator.issued = true
		lease := coordinator.lease
		coordinator.mu.Unlock()
		return []eventprovider.Lease{lease}, nil
	}
	coordinator.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}
func (coordinator *p8CoverageCoordinator) ClaimWithDepth(ctx context.Context, options eventprovider.ClaimOptions) (eventprovider.ClaimSnapshot, error) {
	leases, err := coordinator.Claim(ctx, options)
	return eventprovider.ClaimSnapshot{Leases: leases, Depth: coordinator.depth}, err
}
func (*p8CoverageCoordinator) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (coordinator *p8CoverageCoordinator) Acknowledge(context.Context, string, string) (bool, error) {
	if coordinator.cancel != nil {
		coordinator.cancel()
	}
	return true, nil
}
func (coordinator *p8CoverageCoordinator) Retry(context.Context, string, string, time.Duration, string) (bool, error) {
	if coordinator.cancel != nil {
		coordinator.cancel()
	}
	return true, nil
}
func (coordinator *p8CoverageCoordinator) Block(context.Context, string, string, string) (bool, error) {
	if coordinator.cancel != nil {
		coordinator.cancel()
	}
	return true, nil
}
func (*p8CoverageCoordinator) Release(context.Context, string, string) (bool, error) {
	return true, nil
}
func (*p8CoverageCoordinator) Inspect(context.Context, string) (eventprovider.Delivery, error) {
	return eventprovider.Delivery{}, nil
}
func (*p8CoverageCoordinator) Resume(context.Context, string) (bool, error) { return false, nil }
func (*p8CoverageCoordinator) Retire(context.Context, string) (bool, error) { return false, nil }
func (coordinator *p8CoverageCoordinator) RunRetention(context.Context, eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	return coordinator.retained, nil
}

type p8CoveragePublisherTransport struct{ failure error }

func (transport p8CoveragePublisherTransport) Publish(context.Context, eventvalue.EventBatch) error {
	return transport.failure
}

func TestP8ObservationCoverageEventFaultEdges(t *testing.T) {
	collector := &p8ObservationCollector{}
	fixture := newP7EventRuntimeFixture(t)
	lease := p7ObserverLease(t, fixture)
	runPublisher := func(t *testing.T, lease eventprovider.Lease, transport outbox.PublisherTransport, operation observe.Operation) {
		ctx, cancel := context.WithCancel(context.Background())
		coordinator := &p8CoverageCoordinator{lease: lease, cancel: cancel, depth: eventprovider.DepthSnapshot{Pending: 7, Blocked: 5, Retired: 3}}
		publisher, err := outbox.NewPublisherObserved(coordinator, fixture.app.eventSchemas, transport, outbox.Limits{
			ClaimGroups: 1, Concurrency: 1, LeaseDuration: time.Second, PublishTimeout: time.Second,
			RetryBase: time.Millisecond, RetryCap: time.Millisecond, ShutdownGrace: time.Second,
		}, adaptEventObserver(collector, golem.SQLite))
		if err != nil {
			t.Fatal(err)
		}
		if err := publisher.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if values := collector.matching(observe.KindEvent, operation); len(values) == 0 {
			t.Fatalf("missing production event operation %s: %v", operation, collector.values)
		}
	}
	t.Run("publisher-retry", func(t *testing.T) {
		runPublisher(t, lease, p8CoveragePublisherTransport{failure: errors.New("controlled transport outage")}, observe.OperationEventPublisherRetry)
	})
	t.Run("publisher-block", func(t *testing.T) {
		invalid := lease
		invalid.Facts = eventprovider.CloneFacts(lease.Facts)
		invalid.Facts[0].TransactionOrdinal = 2
		runPublisher(t, invalid, p8CoveragePublisherTransport{}, observe.OperationEventPublisherBlock)
	})
	t.Run("retention", func(t *testing.T) {
		coordinator := &p8CoverageCoordinator{retained: eventprovider.RetentionResult{Causations: 2, Facts: 3}}
		operator, err := newRuntimeEventOperator(coordinator, func(context.Context, events.OperatorAuditRecord) {}, adaptEventObserver(collector, golem.SQLite))
		if err != nil {
			t.Fatal(err)
		}
		policy, err := events.NewRetentionPolicy(time.Now().UTC().Add(-time.Hour), 8)
		if err != nil {
			t.Fatal(err)
		}
		if result, err := operator.RunRetention(context.Background(), policy); err != nil || result.Facts() != 3 {
			t.Fatalf("retention result=%v err=%v", result, err)
		}
	})
	t.Run("transport-reconnect", func(t *testing.T) {
		first := &p8CoverageStream{result: events.Failure(events.CodeEventTransport)}
		second := &p8CoverageStream{block: true}
		var sourceMu sync.Mutex
		sources := []events.Stream{first, second}
		hub, err := subscription.NewModelHub[golem.EventID](subscription.Config[golem.EventID]{
			Generation: golem.SchemaDigest{1}, EventSchema: golem.EventSchemaDigest{1}, Model: golem.ModelID{2},
			Limits: events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
			Source: func(context.Context, events.Subscription) (events.Stream, error) {
				sourceMu.Lock()
				defer sourceMu.Unlock()
				if len(sources) == 0 {
					return nil, events.Failure(events.CodeSubscriptionSourceClosed)
				}
				result := sources[0]
				sources = sources[1:]
				return result, nil
			},
			Evaluate: func(_ context.Context, notice events.Notice, _ subscription.SubscriberKey) (subscription.Evaluation[golem.EventID], error) {
				return subscription.Deliver(notice.EventID()), nil
			},
			Clone:    func(value golem.EventID) (golem.EventID, error) { return value, nil },
			Observer: adaptEventObserver(collector, golem.SQLite),
		})
		if err != nil {
			t.Fatal(err)
		}
		key := p8CoverageSubscriberKey(t)
		stream, err := hub.Subscribe(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		deadline := time.Now().Add(time.Second)
		for len(collector.matching(observe.KindEvent, observe.OperationEventTransportReconnect)) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := hub.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if values := collector.matching(observe.KindEvent, observe.OperationEventTransportReconnect); len(values) != 1 {
			t.Fatalf("transport reconnect observations=%v", values)
		}
	})
	for _, operation := range []observe.Operation{observe.OperationEventPublisherRetry, observe.OperationEventPublisherBlock, observe.OperationEventRetention, observe.OperationEventTransportReconnect} {
		if len(collector.matching(observe.KindEvent, operation)) == 0 {
			t.Fatalf("missing controlled production occurrence %s", operation)
		}
	}
	for operation, want := range map[observe.Operation]p8ObservationSnapshot{
		observe.OperationEventDepthPending: {statements: 1, aggregate: 7},
		observe.OperationEventDepthBlocked: {statements: 0, aggregate: 5},
		observe.OperationEventDepthRetired: {statements: 0, aggregate: 3},
	} {
		values := collector.matching(observe.KindEvent, operation)
		if len(values) == 0 || values[0].statements != want.statements || values[0].aggregate != want.aggregate {
			t.Fatalf("depth operation %s observations=%v want statements=%d aggregate=%d", operation, values, want.statements, want.aggregate)
		}
	}
	p8AppendDynamicCoverage(t, collector.values)
}

type p8CoverageStream struct {
	result error
	block  bool
}

func (stream *p8CoverageStream) Recv(ctx context.Context) (events.Notice, error) {
	if stream.block {
		<-ctx.Done()
		return events.Notice{}, ctx.Err()
	}
	return events.Notice{}, stream.result
}
func (*p8CoverageStream) Close() error { return nil }

func p8CoverageSubscriberKey(t *testing.T) subscription.SubscriberKey {
	t.Helper()
	identity := func(domain string) subscription.CanonicalIdentity {
		value, err := subscription.NewCanonicalIdentity(domain, []byte("p8-observation-coverage"))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	key, err := subscription.NewSubscriberKey(subscription.SubscriberKeyInput{
		Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2}, Principal: identity("principal"), PolicyGeneration: identity("policy"),
		Filter: identity("filter"), Selection: identity("selection"), Dependencies: identity("dependencies"), EncoderShape: identity("encoder"), Membership: identity("membership"), Shareable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

var _ eventprovider.Coordinator = (*p8CoverageCoordinator)(nil)
var _ eventprovider.ClaimDepthCoordinator = (*p8CoverageCoordinator)(nil)
var _ outbox.PublisherTransport = p8CoveragePublisherTransport{}
var _ events.Stream = (*p8CoverageStream)(nil)
