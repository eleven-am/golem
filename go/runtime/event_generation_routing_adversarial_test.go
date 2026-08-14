package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func (transport *p7SignalledTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

func TestPendingV2FactSurvivesGraphQLOnlyRegeneration(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	historicalGeneration := golem.SchemaDigest{31: 99}
	historicalBundle := golem.GeneratedSchemaBundle(
		historicalGeneration,
		fixture.schema.Bundle.GeneratorVersion(),
		fixture.schema.Bundle.TemplateABIVersion(),
		fixture.schema.Bundle.Model(),
		fixture.schema.Bundle.Contract(),
		fixture.schema.Bundle.Providers()...,
	)
	history, err := newEventSchemaHistory(fixture.schema.Registry, []golem.SchemaBundle{historicalBundle})
	if err != nil {
		t.Fatal(err)
	}
	historicalRegistry, _, resolved := history.ResolveFactSchema(mutationfact.SchemaReference{
		FormatVersion: mutationfact.FormatVersionV1,
		Generation:    historicalGeneration,
	})
	if !resolved || historicalRegistry == nil {
		t.Fatal("historical generation was not registered")
	}
	fixture.app.eventSchemas = history

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor, golem.EventSelect[p7EventPost](fixture.title))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("current-generation hub did not subscribe")
	}

	notice := historicalCompatibleV2Notice(t, fixture, historicalRegistry, history)
	fixture.publish(t, notice)
	receiveCtx, receiveCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer receiveCancel()
	got, err := stream.Recv(receiveCtx)
	if err != nil {
		t.Fatalf("logically compatible historical V2 event did not reach current subscriber: %v", err)
	}
	if got.validated.Metadata().EventID() != notice.EventID() {
		t.Fatal("historical V2 event identity changed")
	}
}

func historicalCompatibleV2Notice(t testing.TB, fixture p7EventRuntimeFixture, registry *schema.Registry, history *eventSchemaHistory) events.Notice {
	t.Helper()
	title, err := policyir.StringValue("visible")
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewCompleteRow(registry, policyir.ModelID(fixture.schema.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue([16]byte{15: 9})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue([16]byte{15: 1})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), title),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.schema.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(fixture.digest))
	if err != nil {
		t.Fatal(err)
	}
	eventID := p7OracleID(1900)
	fact, err := mutationfact.NewV2(registry, golem.SchemaDigest(fixture.digest), mutationfact.EventID(eventID), requirement, mutationfact.CausationID(eventID), 1, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fact.OutboxRow(time.Unix(1900, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := eventcodec.EncodeStoredRow(stored, history, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.GenerationDigest() == fixture.schema.Registry.GenerationDigest() || envelope.ResolvedEventSchemaDigest() != fixture.digest {
		t.Fatal("fixture is not a historical-generation/logically-compatible event")
	}
	notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	return notice
}
