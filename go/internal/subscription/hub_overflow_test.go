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
	subscriptiontest.AssertOverflowEndsSubscribersWithResync(t, transport.Subscribe, func(t testing.TB, value byte) {
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
	})
}
