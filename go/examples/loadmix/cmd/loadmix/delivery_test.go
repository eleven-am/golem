package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

type fakeEvent struct {
	row golem.UUID
	id  golem.EventID
}

type scriptedStream struct {
	events []fakeEvent
	end    error
}

func (stream *scriptedStream) Recv(context.Context) (fakeEvent, error) {
	if len(stream.events) == 0 {
		return fakeEvent{}, stream.end
	}
	next := stream.events[0]
	stream.events = stream.events[1:]
	return next, nil
}

func (stream *scriptedStream) Close() error { return nil }

func keyOf(event fakeEvent) eventKey { return eventKey{row: event.row, id: event.id} }

func eventID(value byte) golem.EventID {
	var id golem.EventID
	id[0] = value
	return id
}

type fixture struct {
	tracker  *tracker
	faults   atomic.Int64
	alice    golem.UUID
	bob      golem.UUID
	aliceRow golem.UUID
	bobRow   golem.UUID
}

func newFixture() *fixture {
	result := &fixture{alice: uuidFrom(1, 1), bob: uuidFrom(1, 2), aliceRow: uuidFrom(2, 1), bobRow: uuidFrom(2, 2)}
	result.tracker = newTracker(func() { result.faults.Add(1) }, nil)
	result.tracker.register(result.aliceRow, result.alice)
	result.tracker.register(result.bobRow, result.bob)
	return result
}

func (fixture *fixture) run(owner golem.UUID, stream *scriptedStream) *subscription {
	subscription := fixture.tracker.subscribe(owner, ClassArticle)
	drain(context.Background(), fixture.tracker, subscription, golem.EventStream[fakeEvent](stream), keyOf)
	return subscription
}

func TestTerminationIsRecordedByCodeAndFailsTheRun(t *testing.T) {
	fixture := newFixture()
	fixture.run(fixture.alice, &scriptedStream{end: events.Failure(events.CodeSubscriptionOverflow)})
	report := fixture.tracker.report()
	if report.Terminations[string(events.CodeSubscriptionOverflow)] != 1 {
		t.Fatalf("terminations = %v, want one %s", report.Terminations, events.CodeSubscriptionOverflow)
	}
	if fixture.faults.Load() != 1 {
		t.Fatalf("faults = %d, want 1", fixture.faults.Load())
	}
	if violations := report.Violations(); len(violations) != 1 || !strings.Contains(violations[0], string(events.CodeSubscriptionOverflow)) {
		t.Fatalf("violations = %v, want the termination named", violations)
	}
}

func TestTerminationAfterShutdownIsNotAFailure(t *testing.T) {
	fixture := newFixture()
	fixture.tracker.stopListening()
	fixture.run(fixture.alice, &scriptedStream{end: context.Canceled})
	if report := fixture.tracker.report(); len(report.Terminations) != 0 || len(report.Violations()) != 0 {
		t.Fatalf("report = %+v, want no termination", report)
	}
}

func TestForeignDeliveryIsNotCountedAndFailsAtOnce(t *testing.T) {
	fixture := newFixture()
	subscription := fixture.run(fixture.alice, &scriptedStream{
		events: []fakeEvent{{row: fixture.bobRow, id: eventID(1)}},
		end:    context.Canceled,
	})
	if subscription.delivered() != 0 {
		t.Fatalf("delivered = %d, a foreign event must not count as delivered", subscription.delivered())
	}
	report := fixture.tracker.report()
	if report.Foreign != 1 {
		t.Fatalf("foreign = %d, want 1", report.Foreign)
	}
	if fixture.faults.Load() == 0 {
		t.Fatal("a foreign delivery must fault the run at once")
	}
}

func TestDeliveredIsCountedByEventID(t *testing.T) {
	fixture := newFixture()
	subscription := fixture.run(fixture.alice, &scriptedStream{
		events: []fakeEvent{{row: fixture.aliceRow, id: eventID(1)}, {row: fixture.aliceRow, id: eventID(1)}, {row: fixture.aliceRow, id: eventID(2)}},
		end:    context.Canceled,
	})
	if subscription.delivered() != 2 {
		t.Fatalf("delivered = %d, want 2 distinct event IDs", subscription.delivered())
	}
	if report := fixture.tracker.report(); report.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", report.Duplicates)
	}
}

func TestShortfallIsNamedAndSettleWaitsForIt(t *testing.T) {
	fixture := newFixture()
	subscription := fixture.tracker.subscribe(fixture.alice, ClassArticle)
	subscription.expected.Add(3)
	if fixture.tracker.settled() {
		t.Fatal("settled before the expected events arrived")
	}
	fixture.tracker.delivery(subscription, eventKey{row: fixture.aliceRow, id: eventID(1)})
	report := fixture.tracker.report()
	if report.Shortfall != 2 || report.ShortSubscriptions != 1 {
		t.Fatalf("report = %+v, want a shortfall of 2 on one subscription", report)
	}
	violations := report.Violations()
	if len(violations) != 1 || !strings.Contains(violations[0], "shortfall 2 of 3") {
		t.Fatalf("violations = %v, want the shortfall named", violations)
	}
	fixture.tracker.delivery(subscription, eventKey{row: fixture.aliceRow, id: eventID(2)})
	fixture.tracker.delivery(subscription, eventKey{row: fixture.aliceRow, id: eventID(3)})
	if !fixture.tracker.settled() || len(fixture.tracker.report().Violations()) != 0 {
		t.Fatal("a complete delivery must settle cleanly")
	}
}
