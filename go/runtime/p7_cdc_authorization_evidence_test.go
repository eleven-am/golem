package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
)

type p7AuthorizationCDCAdapter struct{}

func (p7AuthorizationCDCAdapter) Identity() events.CDCIdentity {
	return events.CDCIdentity{Name: "authorization-proof", Version: "1", Provider: golem.SQLite}
}
func (p7AuthorizationCDCAdapter) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}
func (p7AuthorizationCDCAdapter) Run(context.Context, events.CDCEmitter) error { return nil }

func TestP7CDCUsesSameFreshSubscriptionAuthorization(t *testing.T) {
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
		t.Fatal("transport subscription did not start")
	}
	encoder, err := newCDCEventEncoder(fixture.schema.Registry, fixture.app.eventSchemas, 0)
	if err != nil {
		t.Fatal(err)
	}
	emitter, err := eventcdc.NewEmitter(eventcdc.Config{
		Adapter: p7AuthorizationCDCAdapter{}, Transport: fixture.transport, Encoder: encoder,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := p7AuthorizationCDCRow(t, fixture, "visible")
	after := p7AuthorizationCDCRow(t, fixture, "visible")

	fixture.allow.Store(false)
	for len(fixture.resolved) != 0 {
		<-fixture.resolved
	}
	if err := emitter.Emit(ctx, events.CDCBatchInput{
		SourceTransactionID: "denied-cdc", RecordedAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), Cursor: []byte("one"),
		Changes: []events.CDCChangeInput{{Ordinal: 1, Model: fixture.schema.Post, Action: golem.EventUpdated, Before: &before, After: &after}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case allowed := <-fixture.resolved:
		if allowed {
			t.Fatal("CDC event reused stale authorized actor")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CDC event did not resolve a fresh principal")
	}
	if reason := receiveP7Suppression(t, fixture.suppressed); reason != events.SuppressionFiltered {
		t.Fatalf("denied CDC suppression=%q", reason)
	}

	fixture.allow.Store(true)
	if err := emitter.Emit(ctx, events.CDCBatchInput{
		SourceTransactionID: "granted-cdc", RecordedAt: time.Date(2026, 8, 7, 12, 0, 1, 0, time.UTC), Cursor: []byte("two"),
		Changes: []events.CDCChangeInput{{Ordinal: 1, Model: fixture.schema.Post, Action: golem.EventUpdated, Before: &before, After: &after}},
	}); err != nil {
		t.Fatal(err)
	}
	if event := receiveP7Event(t, stream); event.validated.Metadata().Action() != golem.EventUpdated {
		t.Fatalf("granted CDC event action=%q", event.validated.Metadata().Action())
	}
}

func p7AuthorizationCDCRow(t testing.TB, fixture p7EventRuntimeFixture, title string) golem.RuntimeModelRow {
	t.Helper()
	row, err := golem.RuntimeCDCModelRow(fixture.schema.Post,
		golem.RuntimePresentReadCell(fixture.schema.PostID, golem.UUID{15: 9}, nil),
		golem.RuntimePresentReadCell(fixture.schema.AuthorID, golem.UUID{15: 1}, nil),
		golem.RuntimePresentReadCell(fixture.schema.PostTitle, title, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

var _ events.CDCAdapter = p7AuthorizationCDCAdapter{}
