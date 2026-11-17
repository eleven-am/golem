package queue_test

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/queue"
)

func TestPriorityOptionsAndReservationBounds(t *testing.T) {
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[string]{Type: "priority.public", Handle: func(context.Context, queue.Job[string]) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, priority := range []queue.Priority{queue.PriorityNormal, queue.PriorityInteractive} {
		pending, err := jobType.New("payload", queue.WithPriority(priority))
		if err != nil || pending.Priority() != priority {
			t.Fatalf("priority=%d pending=%v error=%v", priority, pending.Priority(), err)
		}
	}
	for _, priority := range []queue.Priority{-1, 2, 1000} {
		if _, err := jobType.New("payload", queue.WithPriority(priority)); err == nil {
			t.Fatalf("accepted priority %d", priority)
		}
		if _, err := queue.ResolvePromotion(priority); err == nil {
			t.Fatalf("accepted promotion priority %d", priority)
		}
	}
	for _, limits := range []queue.Limits{{Concurrency: 1, ReservedInteractive: 1}, {Concurrency: 2, ReservedInteractive: 3}, {ReservedInteractive: -1}} {
		if err := limits.Validate(); err == nil {
			t.Fatalf("accepted reservation %#v", limits)
		}
	}
	if err := (queue.Limits{Concurrency: 2, ReservedInteractive: 1}).Validate(); err != nil {
		t.Fatal(err)
	}
}
