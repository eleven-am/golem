package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestCallerEventRecvDeadlineWrapsContextAndKeepsStreamOpen(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
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
		t.Fatal("model hub did not subscribe to the transport")
	}
	expired, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	_, err = stream.Recv(expired)
	if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionCancelled {
		t.Fatalf("expired Recv = %v, want %s", err, events.CodeSubscriptionCancelled)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, expired.Err()) {
		t.Fatalf("expired Recv = %v, want it to wrap context.DeadlineExceeded", err)
	}
	created := fixture.notice(t, golem.EventCreated, 1, "visible", false)
	fixture.publish(t, created)
	if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(1) {
		t.Fatalf("event after expired Recv = %x", event.validated.Metadata().EventID())
	}
}

func TestCallerEventCloseCancelsWithoutContextCause(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
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
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	receive, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_, err = stream.Recv(receive)
	if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionCancelled {
		t.Fatalf("Recv after Close = %v, want %s", err, events.CodeSubscriptionCancelled)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv after Close = %v, want no context cause", err)
	}
}

func TestCallerEventSubscribeContextCancellationCarriesCause(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	caller, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	owner, cancel := context.WithCancel(context.Background())
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](owner, caller, fixture.descriptor)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer stream.Close()
	cancel()
	receive, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_, err = stream.Recv(receive)
	if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionCancelled {
		t.Fatalf("Recv after subscribe context ended = %v, want %s", err, events.CodeSubscriptionCancelled)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv after subscribe context ended = %v, want it to wrap context.Canceled", err)
	}
}
