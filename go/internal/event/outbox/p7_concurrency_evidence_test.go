package outbox

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestPartialTransportAcceptanceRetriesWholeBatch(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	lease := p7EvidenceLease(t, fixture, 31, time.Unix(31, 0).UTC(), 3)
	coordinator := &publisherTestCoordinator{renewed: true}
	transport := &captureTransport{errors: []error{fmt.Errorf("ambiguous partial broker acceptance"), nil}}
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transport)
	if err := publisher.publishLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if err := publisher.publishLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if len(transport.batches) != 2 || len(transport.batches[0].Events()) != 3 || len(transport.batches[1].Events()) != 3 {
		t.Fatalf("whole causal retries=%d widths=%d/%d", len(transport.batches), len(transport.batches[0].Events()), len(transport.batches[1].Events()))
	}
	assertIdenticalBatches(t, transport.batches[0], transport.batches[1])
	if coordinator.retryCalls != 1 || coordinator.ackCalls != 1 || coordinator.blockCalls != 0 {
		t.Fatalf("retry=%d ack=%d block=%d", coordinator.retryCalls, coordinator.ackCalls, coordinator.blockCalls)
	}
}

func TestTransientFailureNeverDropsAtArbitraryAttemptCount(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	lease := p7EvidenceLease(t, fixture, 32, time.Unix(32, 0).UTC(), 2)
	lease.Delivery.AttemptCount = 9_000_000_000
	coordinator := &publisherTestCoordinator{renewed: true}
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transportFunc(func(context.Context, eventvalue.EventBatch) error {
		return fmt.Errorf("private transient transport outage")
	}))
	if err := publisher.publishLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if coordinator.retryCalls != 1 || coordinator.ackCalls != 0 || coordinator.blockCalls != 0 {
		t.Fatalf("high-attempt transient transition retry=%d ack=%d block=%d", coordinator.retryCalls, coordinator.ackCalls, coordinator.blockCalls)
	}
}

func TestConcurrentCausationsMayInterleaveWithoutCorruption(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	first := p7EvidenceLease(t, fixture, 41, time.Unix(41, 0).UTC(), 3)
	second := p7EvidenceLease(t, fixture, 42, time.Unix(42, 0).UTC(), 2)
	coordinator := &p7OrderCoordinator{publisherTestCoordinator: publisherTestCoordinator{renewed: true}, acknowledgements: make(chan string, 2)}
	transport := newP7InterleavingTransport(golem.CausationID{15: 41})
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transport)
	publisher.limits.Concurrency = 2
	done := make(chan error, 1)
	go func() { done <- publisher.runClaimed(context.Background(), []eventprovider.Lease{first, second}) }()
	select {
	case cause := <-coordinator.acknowledgements:
		if cause != second.Delivery.CausationID {
			t.Fatalf("blocked causation acknowledged before independent work: %s", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("independent causation did not publish while first was blocked")
	}
	close(transport.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, batch := range transport.snapshot() {
		events := batch.Events()
		if len(events) == 0 {
			t.Fatal("transport received empty causation")
		}
		for index, event := range events {
			if event.CausationID() != batch.CausationID() || event.TransactionOrdinal() != uint32(index+1) {
				t.Fatalf("causal batch corrupted at index %d", index)
			}
		}
	}
}

func TestRecordedAtIsNeverUsedAsCommitTimestampOrGlobalOrder(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	older := p7EvidenceLease(t, fixture, 51, time.Unix(1, 0).UTC(), 1)
	newer := p7EvidenceLease(t, fixture, 52, time.Unix(9_999, 0).UTC(), 1)
	coordinator := &p7OrderCoordinator{publisherTestCoordinator: publisherTestCoordinator{renewed: true}, acknowledgements: make(chan string, 2)}
	transport := newP7InterleavingTransport(golem.CausationID{15: 51})
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transport)
	publisher.limits.Concurrency = 2
	done := make(chan error, 1)
	go func() { done <- publisher.runClaimed(context.Background(), []eventprovider.Lease{older, newer}) }()
	select {
	case cause := <-coordinator.acknowledgements:
		if cause != newer.Delivery.CausationID {
			t.Fatalf("recordedAt imposed global acknowledgement order: %s", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("newer causation could not complete independently of older recordedAt")
	}
	close(transport.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type p7OrderCoordinator struct {
	publisherTestCoordinator
	acknowledgements chan string
}

func (coordinator *p7OrderCoordinator) Acknowledge(_ context.Context, causation, _ string) (bool, error) {
	coordinator.mu.Lock()
	coordinator.ackCalls++
	coordinator.mu.Unlock()
	coordinator.acknowledgements <- causation
	return true, nil
}

type p7InterleavingTransport struct {
	blocked golem.CausationID
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	batches []eventvalue.EventBatch
}

func newP7InterleavingTransport(blocked golem.CausationID) *p7InterleavingTransport {
	return &p7InterleavingTransport{blocked: blocked, entered: make(chan struct{}), release: make(chan struct{})}
}

func (transport *p7InterleavingTransport) Publish(ctx context.Context, batch eventvalue.EventBatch) error {
	transport.mu.Lock()
	transport.batches = append(transport.batches, batch)
	transport.mu.Unlock()
	if batch.CausationID() == transport.blocked {
		transport.once.Do(func() { close(transport.entered) })
		select {
		case <-transport.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-transport.entered:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (transport *p7InterleavingTransport) snapshot() []eventvalue.EventBatch {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]eventvalue.EventBatch(nil), transport.batches...)
}

func p7EvidenceLease(t testing.TB, fixture schematest.Fixture, causeByte byte, recordedAt time.Time, width int) eventprovider.Lease {
	t.Helper()
	causation := mutationfact.CausationID{15: causeByte}
	facts := make([]eventprovider.FactRow, width)
	causationText := ""
	for ordinal := 1; ordinal <= width; ordinal++ {
		row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{14: causeByte, 15: byte(ordinal)})),
		})
		if err != nil {
			t.Fatal(err)
		}
		requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		fact, err := mutationfact.New(fixture.Registry, mutationfact.EventID{14: causeByte, 15: byte(ordinal)}, requirement, causation, uint32(ordinal), nil, &row)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := fact.OutboxRow(recordedAt.Add(time.Duration(ordinal-1) * time.Microsecond))
		if err != nil {
			t.Fatal(err)
		}
		causationText = stored.CausationID
		facts[ordinal-1] = eventprovider.FactRow{
			EventID: stored.EventID, FactVersion: stored.FactVersion, CodecIdentity: stored.CodecIdentity,
			GenerationFingerprint: stored.GenerationFingerprint, ModelID: stored.ModelID, Action: stored.Action,
			BeforeIdentity: stored.BeforeIdentity, AfterIdentity: stored.AfterIdentity, CausationID: stored.CausationID,
			TransactionOrdinal: stored.TransactionOrdinal, Metadata: stored.Metadata, DeleteSnapshot: stored.DeleteSnapshot,
			RecordedAt: stored.RecordedAt,
		}
	}
	return eventprovider.Lease{Delivery: eventprovider.Delivery{
		CausationID: causationText, Status: eventprovider.StatusLeased,
		LeaseToken: fmt.Sprintf("00000000-0000-4000-8000-%012d", causeByte), AttemptCount: 1,
	}, Facts: facts}
}
