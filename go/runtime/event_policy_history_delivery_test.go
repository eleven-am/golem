package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestEventPolicyHistoryDeliveryUsesCommittedOwnerAfterLaterReparent(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	callers := make([]*Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], 2)
	streams := make([]golem.EventStream[golem.EventMetadata], 2)
	for index := range callers {
		callers[index] = eventHistoryScopeCaller(t, fixture, byte(index+1))
		stream, err := CallerEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, policyFactRuntimeUser, golem.EventMetadata](fixture.ctx, callers[index], fixture.users)
		if err != nil {
			t.Fatal(err)
		}
		streams[index] = stream
		t.Cleanup(func() { _ = stream.Close() })
	}
	// Both real User subscriptions precede A. Its graph commits to User 1,
	// while B moves the still-ready Post to User 2 before A is delivered.
	evidence := eventHistoryRouteReadyPost(t, fixture)
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 161), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
		t.Fatal(err)
	}
	for index, caller := range callers {
		want := int64(index)
		if count, err := CallerCount(fixture.ctx, caller, fixture.users); err != nil || count != want {
			t.Fatalf("actor%d current CanRead count=%d want=%d err=%v", index+1, count, want, err)
		}
	}
	assertEventHistoryScope(t, fixture, callers[0], evidence, eventHistoryScopeVerdict{before: true, after: true, current: true})
	if fixture.app.eventLimits.RevalidateEvery != 0 {
		t.Fatal("delivery acceptance must not rely on periodic policy revalidation")
	}
	select {
	case notice := <-fixture.transport.published:
		t.Fatalf("unexpected publication before isolated A: %v", notice.EventID())
	default:
	}
	for index, stream := range streams {
		ctx, cancel := context.WithTimeout(fixture.ctx, 25*time.Millisecond)
		_, err := stream.Recv(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("actor%d changed before A was published: %v", index+1, err)
		}
	}
	rows := mutationHistorySealStoredRows(t, fixture, golem.UUID(evidence.value.CausationID()).String())
	if len(rows) != 1 || rows[0].FactVersion != int64(mutationfact.HistoryFactVersion) {
		t.Fatal("A must be the actual one-fact sealed V4 causation")
	}
	encoded, err := eventcodec.EncodeStoredRow(rows[0], fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventcodec.Decode(encoded.Encoded(), fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := wire.Fact().PolicyHistoryCompanion(); present {
		t.Fatal("transport must resolve the stored graph rather than carry it")
	}
	notice, err := eventvalue.NewRoutedNotice(wire.EventID(), wire.GenerationDigest(), wire.ResolvedEventSchemaDigest(), wire.ModelID(), wire.Action(), wire.CausationID(), wire.TransactionOrdinal(), wire.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := eventvalue.NewEventBatch(wire.CausationID(), []events.Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	// No publisher is started: only A enters the production transport/hub path.
	if err := fixture.transport.Publish(fixture.ctx, batch); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(fixture.ctx, 3*time.Second)
	_, deliveryErr := streams[0].Recv(ctx)
	cancel()
	var failure *events.Error
	if !errors.As(deliveryErr, &failure) || failure.Code() != events.CodeSubscriptionResync || failure.Reason() != events.ReasonDependencyChanged {
		t.Fatalf("certified A activity did not invalidate its historical owner: %v", deliveryErr)
	}
	ctx, cancel = context.WithTimeout(fixture.ctx, 250*time.Millisecond)
	_, otherErr := streams[1].Recv(ctx)
	cancel()
	if !errors.Is(otherErr, context.DeadlineExceeded) {
		t.Fatalf("A followed B's current relationship and disclosed activity to actor2: %v", otherErr)
	}
	select {
	case published := <-fixture.transport.published:
		if published.EventID() != wire.EventID() {
			t.Fatal("a notice other than A entered the transport")
		}
	default:
		t.Fatal("transport did not record A's accepted publication")
	}
	select {
	case extra := <-fixture.transport.published:
		t.Fatalf("unexpected additional publication: %v", extra.EventID())
	default:
	}
}

func TestEventPolicyHistoryDeliveryDeletesDirectOwnerWithoutPhysicalDependencies(t *testing.T) {
	fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		userNameScope: true, unusedFact: true,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
	})
	callers := make([]*Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], 2)
	streams := make([]golem.EventStream[golem.EventMetadata], 2)
	for index := range callers {
		callers[index] = eventHistoryScopeCaller(t, fixture, byte(index+1))
		row, filter, err := readplan.CallerReach(callers[index].policies, policyir.ModelID(fixture.schema.User), nil, fixture.app.readLimits.plan.MaxRelationDepth)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := observation.New(row, filter)
		if err != nil {
			t.Fatal(err)
		}
		physical, err := factEventDependencies(plan, fixture.app.registry)
		if err != nil || len(physical) != 0 {
			t.Fatalf("unused root fact unexpectedly created physical dependencies: count=%d err=%v", len(physical), err)
		}
		stream, err := CallerEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, policyFactRuntimeUser, golem.EventMetadata](fixture.ctx, callers[index], fixture.users)
		if err != nil {
			t.Fatal(err)
		}
		streams[index] = stream
		t.Cleanup(func() { _ = stream.Close() })
	}
	if count, err := CallerCount(fixture.ctx, callers[0], fixture.users); err != nil || count != 1 {
		t.Fatalf("actor1 must initially retain User1 without any Posts: count=%d err=%v", count, err)
	}
	if fixture.app.eventLimits.RevalidateEvery != 0 {
		t.Fatal("direct-owner cleanup must not rely on periodic policy revalidation")
	}
	// The unused fact creates no physical dependency subscription. A must be
	// handled by the ordinary User event evaluator, not a private matcher call.
	evidence := eventHistoryScopeCapture(t, fixture, func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		_, err := SystemTxDelete(fixture.ctx, transaction, fixture.users, selector)
		return err
	})
	if count, err := CallerCount(fixture.ctx, callers[0], fixture.users); err != nil || count != 0 {
		t.Fatalf("A did not delete retained User1: count=%d err=%v", count, err)
	}
	select {
	case notice := <-fixture.transport.published:
		t.Fatalf("unexpected publication before isolated A: %v", notice.EventID())
	default:
	}
	for index, stream := range streams {
		ctx, cancel := context.WithTimeout(fixture.ctx, 25*time.Millisecond)
		_, err := stream.Recv(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("actor%d changed before A was published: %v", index+1, err)
		}
	}
	rows := mutationHistorySealStoredRows(t, fixture, golem.UUID(evidence.value.CausationID()).String())
	if len(rows) != 1 || rows[0].FactVersion != int64(mutationfact.HistoryFactVersion) {
		t.Fatal("A must be the actual one-fact sealed V4 deletion")
	}
	encoded, err := eventcodec.EncodeStoredRow(rows[0], fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventcodec.Decode(encoded.Encoded(), fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if wire.ModelID() != fixture.schema.User || wire.Action() != golem.EventDeleted {
		t.Fatal("A's stored notice is not the User deletion")
	}
	if _, present := wire.Fact().PolicyHistoryCompanion(); present {
		t.Fatal("transport must not carry the private graph")
	}
	notice, err := eventvalue.NewRoutedNotice(wire.EventID(), wire.GenerationDigest(), wire.ResolvedEventSchemaDigest(), wire.ModelID(), wire.Action(), wire.CausationID(), wire.TransactionOrdinal(), wire.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := eventvalue.NewEventBatch(wire.CausationID(), []events.Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.transport.Publish(fixture.ctx, batch); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(fixture.ctx, 3*time.Second)
	metadata, deliveryErr := streams[0].Recv(ctx)
	cancel()
	if deliveryErr == nil {
		if metadata.Action() != golem.EventDeleted || metadata.EventID() != wire.EventID() || metadata.ModelID() != fixture.schema.User {
			t.Errorf("A did not deliver its exact deletion: action=%v event=%v model=%v", metadata.Action(), metadata.EventID(), metadata.ModelID())
		}
	} else {
		var failure *events.Error
		if !errors.As(deliveryErr, &failure) || failure.Code() != events.CodeSubscriptionResync || failure.Reason() != "" || failure.Detail() != "" || errors.Unwrap(failure) != nil {
			t.Errorf("direct owner deletion left retained User1 without unattributed cleanup: %v", deliveryErr)
		}
	}
	ctx, cancel = context.WithTimeout(fixture.ctx, 250*time.Millisecond)
	_, otherErr := streams[1].Recv(ctx)
	cancel()
	if !errors.Is(otherErr, context.DeadlineExceeded) {
		t.Fatalf("User1 deletion disclosed activity to actor2: %v", otherErr)
	}
	select {
	case published := <-fixture.transport.published:
		if published.EventID() != wire.EventID() {
			t.Fatal("a notice other than A entered the transport")
		}
	default:
		t.Fatal("transport did not record A's accepted publication")
	}
	select {
	case extra := <-fixture.transport.published:
		t.Fatalf("unexpected additional publication: %v", extra.EventID())
	default:
	}
}
