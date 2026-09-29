package subscription_test

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	"github.com/eleven-am/golem/go/internal/subscription/subscriptiontest"
)

func TestMemoryTransportOverflowNeverDropsAnUpdateSilently(t *testing.T) {
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 1})
	if err != nil {
		t.Fatal(err)
	}
	subscriptiontest.AssertOverflowEndsSubscribersWithResync(t, transport.Subscribe, publishMemoryNotice(transport))
}

func TestMemoryTransportConnectFailureNeverSkipsAnUpdateSilently(t *testing.T) {
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	subscriptiontest.AssertTransientConnectFailureNeverSkipsSilently(t, transport.Subscribe, publishMemoryNotice(transport))
}

func publishMemoryNotice(transport events.EventTransport) func(testing.TB, byte) {
	return func(t testing.TB, value byte) {
		t.Helper()
		notice, err := eventvalue.NewNotice(golem.EventID{value}, subscriptiontest.Generation, subscriptiontest.Model, golem.EventCreated, golem.CausationID{value}, 1, []byte{value})
		if err != nil {
			t.Fatal(err)
		}
		batch, err := eventvalue.NewEventBatch(golem.CausationID{value}, []events.Notice{notice})
		if err != nil {
			t.Fatal(err)
		}
		if err := transport.Publish(context.Background(), batch); err != nil {
			t.Fatalf("publish %d: %v", value, err)
		}
	}
}
