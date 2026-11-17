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
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestEventPolicyHistoryOrdinaryPrefixLossCleansUpWithoutCrossActorDisclosure(t *testing.T) {
	fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		postMutationGrants: true, commentOwnerRead: true,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
	})
	if len(fixture.app.registry.PolicyCaptureFields(fixture.schema.Comment)) != 0 {
		t.Fatal("fixture must leave the ordinary Comment prefix outside the static capture inventory")
	}
	comments := mutationHistoryGraphComments(fixture.schema)
	// User=M, Post=V, Comment=W. W reaches M1 through sibling V2, not
	// through the V1 row whose title supplies M1's physical data fact.
	fixture.mutate(t, golem.EventCreated, 151, 1, "ready")
	fixture.mutate(t, golem.EventCreated, 152, 1, "not-ready")
	fixture.mutate(t, golem.EventCreated, 153, 2, "ready")
	if _, err := SystemCreate(fixture.ctx, fixture.app.System(), comments, mutationHistoryGraphCommentInput(fixture.schema, 1, 152, "retained W")); err != nil {
		t.Fatal(err)
	}
	callers := make([]*Caller[policyFactRuntimePrincipal, policyFactRuntimeActor], 2)
	streams := make([]golem.EventStream[golem.EventMetadata], 2)
	for index := range callers {
		caller, err := fixture.app.ForPrincipal(fixture.ctx, policyFactRuntimePrincipal{ID: golem.UUID{15: byte(index + 1)}})
		if err != nil {
			t.Fatal(err)
		}
		callers[index] = caller
		stream, err := CallerEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, policyFactRuntimeComment, golem.EventMetadata](fixture.ctx, caller, comments)
		if err != nil {
			t.Fatal(err)
		}
		streams[index] = stream
		t.Cleanup(func() { _ = stream.Close() })
		want := int64(0)
		if index == 0 {
			want = 1
		}
		if count, err := CallerCount(fixture.ctx, caller, comments); err != nil || count != want {
			t.Fatalf("initial source actor=%d count=%d want=%d err=%v", index+1, count, want, err)
		}
	}
	// A loses M1's data fact. Keep the already-bound source subscriptions
	// alive by delaying A's notice, as can happen across model-routed streams.
	fixture.mutate(t, golem.EventUpdated, 151, 1, "not-ready")
	if count, err := CallerCount(fixture.ctx, callers[0], fixture.users); err != nil || count != 0 {
		t.Fatalf("A did not remove the old owner's fact: count=%d err=%v", count, err)
	}
	if count, err := CallerCount(fixture.ctx, callers[1], fixture.users); err != nil || count != 1 {
		t.Fatalf("other owner must remain fact-readable: count=%d err=%v", count, err)
	}
	// B moves W's ordinary prefix from V2/M1 to V3/M2. A fresh full joined
	// predicate is now false on both routes for actor1, but that must not
	// prevent B from cleaning up actor1's previously visible W.
	var causation string
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeComment](fixture.schema.Comment, fixture.schema.CommentKey, golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: 1}))
		post := golem.GeneratedEqualField[policyFactRuntimeComment, golem.UUID](fixture.schema.CommentPostID)
		if _, err := SystemTxUpdate(fixture.ctx, transaction, comments, selector, golem.GeneratedUpdateInput[policyFactRuntimeComment](fixture.schema.Comment, golem.GeneratedSetFieldValue(fixture.schema.Comment, post, golem.UUID{15: 153}))); err != nil {
			return err
		}
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		if len(state.facts) != 1 {
			t.Fatalf("B facts=%d want1", len(state.facts))
		}
		causation = state.facts[0].CausationID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := mutationHistorySealStoredRows(t, fixture, causation)
	if len(rows) != 1 || rows[0].FactVersion != int64(mutationfact.FormatVersion) {
		// This ordinary source is intentionally outside the capture
		// inventory. Its before-image still uses V3 even in the historical
		// writer binary; the caller's fact policies were admitted as V4.
		t.Fatalf("uncaptured ordinary-prefix B changed its V3 fact format: %#v", rows)
	}
	fact, err := mutationfact.ValidateStoredRow(rows[0], fixture.app.eventSchemas)
	if err != nil {
		t.Fatal(err)
	}
	before, present := fact.PrivateDeleteSnapshot()
	if !present {
		t.Fatal("B has no recorded ordinary-prefix before image")
	}
	cell, present := before.Cell(policyir.FieldID(fixture.schema.CommentPostID))
	if !present {
		t.Fatal("B before image omits the old prefix")
	}
	value, _ := cell.PolicyValue()
	oldPost, _ := value.UUID()
	if oldPost != (golem.UUID{15: 152}).Bytes() {
		t.Fatal("B did not record V2 as its old prefix")
	}
	encoded, err := eventcodec.EncodeStoredRow(rows[0], fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventcodec.Decode(encoded.Encoded(), fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	notice, err := eventvalue.NewRoutedNotice(wire.EventID(), wire.GenerationDigest(), wire.ResolvedEventSchemaDigest(), wire.ModelID(), wire.Action(), wire.CausationID(), wire.TransactionOrdinal(), encoded.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := eventvalue.NewEventBatch(wire.CausationID(), []events.Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	// No periodic policy refresh or source notice may account for the cleanup.
	// Recv deadlines leave the subscriptions open; both must still be quiet
	// after the writes and before publishing the isolated B notice.
	if fixture.app.eventLimits.RevalidateEvery != 0 {
		t.Fatal("prefix-loss backstop must not rely on periodic policy revalidation")
	}
	select {
	case notice := <-fixture.transport.published:
		t.Fatalf("unexpected publication before B: event=%v", notice.EventID())
	default:
	}
	for index, stream := range streams {
		quiet, cancel := context.WithTimeout(fixture.ctx, 25*time.Millisecond)
		_, err := stream.Recv(quiet)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("actor%d changed before B's notice was evaluated: %v", index+1, err)
		}
	}
	// Publish only the actual committed B fact through the real transport and
	// hub. This isolates the ordinary source-event fallback from A's separate
	// fact-dependency invalidation; no evaluator or verdict is reimplemented.
	if err := fixture.transport.Publish(fixture.ctx, batch); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(fixture.ctx, 3*time.Second)
	metadata, deliveryErr := streams[0].Recv(ctx)
	cancel()
	code, _ := events.CodeOf(deliveryErr)
	reason, _ := events.ReasonOf(deliveryErr)
	if deliveryErr == nil {
		if metadata.Action() != golem.EventExited || metadata.EventID() != wire.EventID() || metadata.ModelID() != fixture.schema.Comment {
			t.Errorf("B did not clean up retained W: action=%v event=%v model=%v", metadata.Action(), metadata.EventID(), metadata.ModelID())
		}
	} else if code != events.CodeSubscriptionResync || reason != "" {
		// Unknown historical authority produces a generic cleanup signal.
		// Requiring a dependency reason would demand hidden attribution that
		// the ordinary source-event fallback deliberately does not expose.
		t.Errorf("B ordinary prefix loss left actor1's retained W without cleanup after A removed the old owner fact: %v", deliveryErr)
	}
	ctx, cancel = context.WithTimeout(fixture.ctx, 250*time.Millisecond)
	_, otherErr := streams[1].Recv(ctx)
	cancel()
	if !errors.Is(otherErr, context.DeadlineExceeded) {
		t.Errorf("B disclosed W activity to actor2 despite CommentID != actor.ID: %v", otherErr)
	}
}
