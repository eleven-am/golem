package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
)

func (fixture deleteEvalFixture) emitCDCDelete(t *testing.T, transaction string, comment, post byte) {
	t.Helper()
	encoder, err := newCDCEventEncoder(fixture.schema.Registry, fixture.app.eventSchemas, 0)
	if err != nil {
		t.Fatal(err)
	}
	emitter, err := eventcdc.NewEmitter(eventcdc.Config{Adapter: p7AuthorizationCDCAdapter{}, Transport: fixture.transport, Encoder: encoder})
	if err != nil {
		t.Fatal(err)
	}
	before, err := golem.RuntimeCDCModelRow(fixture.schema.Comment,
		golem.RuntimePresentReadCell(fixture.schema.CommentID, deleteEvalUUID(comment), nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentPostID, deleteEvalUUID(post), nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentBody, "hello", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := emitter.Emit(context.Background(), events.CDCBatchInput{
		SourceTransactionID: transaction, RecordedAt: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), Cursor: []byte(transaction),
		Changes: []events.CDCChangeInput{{Ordinal: 1, Model: fixture.schema.Comment, Action: golem.EventDeleted, Before: &before}},
	}); err != nil {
		t.Fatal(err)
	}
}

func expectCDCDeleteDelivered(t *testing.T, stream golem.EventStream[p7EventOracleValue]) {
	t.Helper()
	event := receiveP7Event(t, stream)
	if event.validated.Metadata().Action() != golem.EventDeleted {
		t.Fatalf("received %q, want a delete", event.validated.Metadata().Action())
	}
}

func TestCDCDeleteNegatedPolicyOverParentUpdatedInSourceTransactionResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return golem.Not(fields.commentPost.Is(fields.postTitle.Eq("published"))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		if fixture.liveCommentVisible(t, "alice", deleteEvalOnPub) {
			t.Fatal("live read shows a comment under the published post")
		}
		fixture.retitlePost(t, deleteEvalPublished, "draft")
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.emitCDCDelete(t, "cdc-leak", deleteEvalOnPub, deleteEvalPublished)
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
	})
}

func TestCDCDeletePolicyOverParentUpdatedInSourceTransactionResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		if !fixture.liveCommentVisible(t, "alice", deleteEvalOnPub) {
			t.Fatal("live read hides a comment under the published post")
		}
		fixture.retitlePost(t, deleteEvalPublished, "draft")
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.emitCDCDelete(t, "cdc-under", deleteEvalOnPub, deleteEvalPublished)
		expectDeleteResync(t, stream)
		if fixture.suppressions(events.SuppressionUnauthorized) != 0 {
			t.Fatal("a CDC delete over a relational policy was suppressed as unauthorized")
		}
	})
}

func TestCDCDeleteScalarPolicyIsExactWithoutTheOutbox(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPID.Eq(deleteEvalUUID(deleteEvalPublished)), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.exec(t, `DROP TABLE `+fixture.outboxTable(t))
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.emitCDCDelete(t, "cdc-denied", deleteEvalOnDraft, deleteEvalDraft)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.emitCDCDelete(t, "cdc-allowed", deleteEvalOnPub, deleteEvalPublished)
		expectCDCDeleteDelivered(t, stream)
		if fixture.suppressions(events.SuppressionDeletionUnverifiable) != 0 {
			t.Fatal("a scalar-only CDC delete was reported unverifiable")
		}
	})
}
