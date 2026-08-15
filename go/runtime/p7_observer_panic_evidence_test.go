package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/event/outbox"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type p7AlwaysPanicObserver struct{}

func (p7AlwaysPanicObserver) ObserveEvent(context.Context, events.Observation) {
	panic("hostile observer secret")
}

func TestObserverPanicDoesNotStopPublisherOrSubscriber(t *testing.T) {
	t.Run("publisher-acknowledges", func(t *testing.T) {
		fixture := newP7EventRuntimeFixture(t)
		lease := p7ObserverLease(t, fixture)
		ctx, cancel := context.WithCancel(context.Background())
		coordinator := &p7OneLeaseCoordinator{lease: lease, cancel: cancel}
		transport := &p7CountingPublisherTransport{}
		publisher, err := outbox.NewPublisherObserved(coordinator, fixture.app.eventSchemas, transport, outbox.Limits{
			ClaimGroups: 1, Concurrency: 1, LeaseDuration: time.Second, PublishTimeout: time.Second,
			RetryBase: time.Millisecond, RetryCap: time.Second, ShutdownGrace: time.Second,
		}, p7AlwaysPanicObserver{})
		if err != nil {
			t.Fatal(err)
		}
		if err := publisher.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if coordinator.acks.Load() != 1 || transport.publishes.Load() != 1 {
			t.Fatalf("observer panic altered publication: publishes=%d acknowledgements=%d", transport.publishes.Load(), coordinator.acks.Load())
		}
	})

	t.Run("subscriber-delivers", func(t *testing.T) {
		fixture := newP7EventRuntimeFixture(t)
		fixture.app.eventObserver = p7AlwaysPanicObserver{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		select {
		case <-fixture.transport.subscribed:
		case <-time.After(time.Second):
			t.Fatal("subscriber source did not start")
		}
		fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1910, "visible", false))
		if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(1910) {
			t.Fatalf("observer panic altered subscriber delivery: %x", event.validated.Metadata().EventID())
		}
	})
}

type p7CountingPublisherTransport struct{ publishes atomic.Int64 }

func (transport *p7CountingPublisherTransport) Publish(context.Context, eventvalue.EventBatch) error {
	transport.publishes.Add(1)
	return nil
}

type p7OneLeaseCoordinator struct {
	mu     sync.Mutex
	lease  eventprovider.Lease
	issued bool
	cancel context.CancelFunc
	acks   atomic.Int64
}

func (coordinator *p7OneLeaseCoordinator) Claim(ctx context.Context, _ eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	coordinator.mu.Lock()
	if !coordinator.issued {
		coordinator.issued = true
		lease := coordinator.lease
		coordinator.mu.Unlock()
		return []eventprovider.Lease{lease}, nil
	}
	coordinator.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*p7OneLeaseCoordinator) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (coordinator *p7OneLeaseCoordinator) Acknowledge(context.Context, string, string) (bool, error) {
	coordinator.acks.Add(1)
	coordinator.cancel()
	return true, nil
}
func (*p7OneLeaseCoordinator) Retry(context.Context, string, string, time.Duration, string) (bool, error) {
	return true, nil
}
func (*p7OneLeaseCoordinator) Block(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (*p7OneLeaseCoordinator) Release(context.Context, string, string) (bool, error) {
	return true, nil
}
func (*p7OneLeaseCoordinator) Inspect(context.Context, string) (eventprovider.Delivery, error) {
	return eventprovider.Delivery{}, nil
}
func (*p7OneLeaseCoordinator) Resume(context.Context, string) (bool, error) { return false, nil }
func (*p7OneLeaseCoordinator) Retire(context.Context, string) (bool, error) { return false, nil }
func (*p7OneLeaseCoordinator) RunRetention(context.Context, eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	return eventprovider.RetentionResult{}, nil
}

func p7ObserverLease(t testing.TB, fixture p7EventRuntimeFixture) eventprovider.Lease {
	t.Helper()
	row, err := mutationdecode.NewRow(fixture.schema.Registry, policyir.ModelID(fixture.schema.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue([16]byte{15: 9})),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.schema.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fact, err := mutationfact.New(fixture.schema.Registry, mutationfact.EventID{15: 91}, requirement, mutationfact.CausationID{15: 92}, 1, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fact.OutboxRow(time.Unix(1_800_000_000, 123456000).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return eventprovider.Lease{Delivery: eventprovider.Delivery{
		CausationID: stored.CausationID, Status: eventprovider.StatusLeased,
		LeaseToken: "00000000-0000-4000-8000-000000000091", AttemptCount: 1,
	}, Facts: []eventprovider.FactRow{{
		EventID: stored.EventID, FactVersion: stored.FactVersion, CodecIdentity: stored.CodecIdentity,
		GenerationFingerprint: stored.GenerationFingerprint, ModelID: stored.ModelID, Action: stored.Action,
		BeforeIdentity: stored.BeforeIdentity, AfterIdentity: stored.AfterIdentity, CausationID: stored.CausationID,
		TransactionOrdinal: stored.TransactionOrdinal, Metadata: stored.Metadata, DeleteSnapshot: stored.DeleteSnapshot,
		RecordedAt: stored.RecordedAt,
	}}}
}

var _ eventprovider.Coordinator = (*p7OneLeaseCoordinator)(nil)
var _ outbox.PublisherTransport = (*p7CountingPublisherTransport)(nil)
