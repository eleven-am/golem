// Package transporttest provides the common behavioral conformance harness for
// every Golem EventTransport. It is part of the Golem module so it can create
// sealed fixtures without adding public Notice or Subscription constructors.
package transporttest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
)

type Factory func(testing.TB) (events.EventTransport, error)

type ExpectedCapabilities struct {
	Identity string
	Scope    events.TransportScope
	Durable  bool
}

// Run executes the common causal-batch, replay, routing, cancellation, and
// capability contract. A concrete adapter may add stronger durability/restart
// tests, but passing only its Go interfaces is never a conformance claim.
func Run(t *testing.T, factory Factory, expected ExpectedCapabilities) {
	t.Helper()
	t.Run("capabilities", func(t *testing.T) {
		transport := open(t, factory)
		capabilities := events.CapabilitiesOf(transport)
		if capabilities.Identity() != expected.Identity || capabilities.Scope() != expected.Scope || capabilities.Durable() != expected.Durable {
			t.Fatalf("capabilities=(%q,%q,%t), want (%q,%q,%t)", capabilities.Identity(), capabilities.Scope(), capabilities.Durable(), expected.Identity, expected.Scope, expected.Durable)
		}
	})
	t.Run("whole-batch-order-and-routing", func(t *testing.T) {
		transport := open(t, factory)
		generation := golem.SchemaDigest{1}
		firstModel, secondModel := golem.ModelID{2}, golem.ModelID{3}
		causation := golem.CausationID{4}
		first := notice(t, golem.EventID{1}, generation, firstModel, causation, 1, []byte("first"))
		second := notice(t, golem.EventID{2}, generation, firstModel, causation, 2, []byte("second"))
		third := notice(t, golem.EventID{3}, generation, secondModel, causation, 3, []byte("third"))
		batch := causalBatch(t, causation, []events.Notice{first, second, third})

		firstStream := subscribe(t, transport, generation, firstModel)
		defer firstStream.Close()
		secondStream := subscribe(t, transport, generation, secondModel)
		defer secondStream.Close()
		foreignStream := subscribe(t, transport, golem.SchemaDigest{9}, firstModel)
		defer foreignStream.Close()
		if err := transport.Publish(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
		assertNotice(t, receive(t, firstStream), first)
		assertNotice(t, receive(t, firstStream), second)
		assertNotice(t, receive(t, secondStream), third)
		assertNoNotice(t, foreignStream)
	})
	t.Run("duplicate-retry-preserves-identities-and-bytes", func(t *testing.T) {
		transport := open(t, factory)
		generation, model, causation := golem.SchemaDigest{5}, golem.ModelID{6}, golem.CausationID{7}
		encoded := []byte{9, 8, 7}
		original := notice(t, golem.EventID{8}, generation, model, causation, 1, encoded)
		encoded[0] = 0
		batch := causalBatch(t, causation, []events.Notice{original})
		stream := subscribe(t, transport, generation, model)
		defer stream.Close()
		for attempt := 0; attempt < 2; attempt++ {
			if err := transport.Publish(context.Background(), batch); err != nil {
				t.Fatalf("publish retry %d: %v", attempt, err)
			}
		}
		for attempt := 0; attempt < 2; attempt++ {
			assertNotice(t, receive(t, stream), original)
		}
	})
	t.Run("subscription-owner-cancellation", func(t *testing.T) {
		transport := open(t, factory)
		owner, cancel := context.WithCancel(context.Background())
		subscription, err := eventvalue.NewSubscription(golem.SchemaDigest{1}, golem.ModelID{2})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := transport.Subscribe(owner, subscription)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if _, err := stream.Recv(ctx); err == nil {
			t.Fatal("cancelled subscription returned no error")
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if err := stream.Close(); err != nil {
			t.Fatalf("second Close must be safe: %v", err)
		}
	})
}

func open(t testing.TB, factory Factory) events.EventTransport {
	t.Helper()
	if factory == nil {
		t.Fatal("transport factory is nil")
	}
	transport, err := factory(t)
	if err != nil {
		t.Fatal(err)
	}
	if transport == nil {
		t.Fatal("transport factory returned nil")
	}
	return transport
}

func notice(t testing.TB, eventID golem.EventID, generation golem.SchemaDigest, model golem.ModelID, causation golem.CausationID, ordinal uint32, encoded []byte) events.Notice {
	t.Helper()
	result, err := eventvalue.NewNotice(eventID, generation, model, golem.EventCreated, causation, ordinal, encoded)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func causalBatch(t testing.TB, causation golem.CausationID, notices []events.Notice) events.EventBatch {
	t.Helper()
	result, err := eventvalue.NewEventBatch(causation, notices)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func subscribe(t testing.TB, transport events.EventTransport, generation golem.SchemaDigest, model golem.ModelID) events.Stream {
	t.Helper()
	subscription, err := eventvalue.NewSubscription(generation, model)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := transport.Subscribe(context.Background(), subscription)
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func receive(t testing.TB, stream events.Stream) events.Notice {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertNoNotice(t testing.TB, stream events.Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if notice, err := stream.Recv(ctx); err == nil {
		t.Fatalf("foreign subscription received event %x", notice.EventID())
	}
}

func assertNotice(t testing.TB, got, want events.Notice) {
	t.Helper()
	if !got.Valid() || got.EventID() != want.EventID() || got.GenerationDigest() != want.GenerationDigest() || got.EventSchemaDigest() != want.EventSchemaDigest() || got.ModelID() != want.ModelID() || got.Action() != want.Action() || got.CausationID() != want.CausationID() || got.TransactionOrdinal() != want.TransactionOrdinal() || !bytes.Equal(got.Encoded(), want.Encoded()) {
		t.Fatalf("transport changed sealed notice: got event=%x ordinal=%d", got.EventID(), got.TransactionOrdinal())
	}
	owned := got.Encoded()
	owned[0] ^= 0xff
	if !bytes.Equal(got.Encoded(), want.Encoded()) {
		t.Fatal("transport notice exposes aliased encoded bytes")
	}
}
