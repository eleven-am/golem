package outbox

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type incompatibleDeliveryResolver struct{ publisherTestResolver }

func (incompatibleDeliveryResolver) CanDeliverEventSchema(golem.ModelID, golem.EventSchemaDigest) bool {
	return false
}

func TestIncompatibleHistoricalSchemaBlocksWithoutTransportOrAck(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	lease := publisherValidLease(t, fixture)
	coordinator := &publisherTestCoordinator{renewed: true}
	transport := &captureTransport{}
	publisher := publisherForTest(t, coordinator, incompatibleDeliveryResolver{publisherTestResolver{fixture.Registry}}, transport)

	if err := publisher.publishLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if len(transport.batches) != 0 || coordinator.ackCalls != 0 || coordinator.blockCalls != 1 {
		t.Fatalf("incompatible historical schema transport=%d ack=%d block=%d", len(transport.batches), coordinator.ackCalls, coordinator.blockCalls)
	}
}

type p7SwitchableHistoricalResolver struct {
	publisherTestResolver
	available bool
}

func (resolver *p7SwitchableHistoricalResolver) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	if !resolver.available {
		return nil, golem.SchemaDigest{}, false
	}
	return resolver.publisherTestResolver.ResolveFactSchema(reference)
}

type p7ResumableCoordinator struct {
	publisherTestCoordinator
	blocked   bool
	resumed   string
	blockCode string
}

func (coordinator *p7ResumableCoordinator) Block(_ context.Context, causation, _ string, failureCode string) (bool, error) {
	coordinator.blockCalls++
	coordinator.blocked = true
	coordinator.resumed = causation
	coordinator.blockCode = failureCode
	return true, nil
}

func (coordinator *p7ResumableCoordinator) Resume(_ context.Context, causation string) (bool, error) {
	if !coordinator.blocked || causation != coordinator.resumed {
		return false, nil
	}
	coordinator.blocked = false
	return true, nil
}

func TestMissingHistoricalSchemaBlocksWithoutAckAndResumes(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	lease := publisherValidLease(t, fixture)
	coordinator := &p7ResumableCoordinator{publisherTestCoordinator: publisherTestCoordinator{renewed: true}}
	transport := &captureTransport{}
	resolver := &p7SwitchableHistoricalResolver{publisherTestResolver: publisherTestResolver{fixture.Registry}}
	publisher, err := NewPublisher(coordinator, resolver, transport, Limits{})
	if err != nil {
		t.Fatal(err)
	}

	if err := publisher.publishLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if coordinator.blockCalls != 1 || coordinator.blockCode != "schema-unavailable" || coordinator.ackCalls != 0 || len(transport.batches) != 0 {
		t.Fatalf("missing schema block=%d code=%q ack=%d transport=%d", coordinator.blockCalls, coordinator.blockCode, coordinator.ackCalls, len(transport.batches))
	}

	changed, err := coordinator.Resume(context.Background(), lease.Delivery.CausationID)
	if err != nil || !changed {
		t.Fatalf("resume changed=%t error=%v", changed, err)
	}
	resolver.available = true
	if err := publisher.publishLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if coordinator.blockCalls != 1 || coordinator.ackCalls != 1 || len(transport.batches) != 1 {
		t.Fatalf("resumed block=%d ack=%d transport=%d", coordinator.blockCalls, coordinator.ackCalls, len(transport.batches))
	}
}

var _ eventprovider.Coordinator = (*p7ResumableCoordinator)(nil)
