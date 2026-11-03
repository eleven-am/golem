package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	eventoutbox "github.com/eleven-am/golem/go/internal/event/outbox"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

var deleteEvalCDCRecordedAt = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type deleteEvalCDCRecorder struct {
	inner      eventcdc.Recorder
	discard    bool
	after      func(context.Context) error
	mu         sync.Mutex
	causations []string
}

func (recorder *deleteEvalCDCRecorder) RecordCDC(ctx context.Context, causation golem.CausationID, changes []events.CDCChangeInput, build func([]int64) ([]mutationfact.OutboxRow, error)) error {
	recorder.mu.Lock()
	recorder.causations = append(recorder.causations, golem.UUID(causation).String())
	recorder.mu.Unlock()
	if recorder.discard {
		versions := make([]int64, len(changes))
		for index := range versions {
			versions[index] = 1
		}
		_, err := build(versions)
		return err
	}
	if err := recorder.inner.RecordCDC(ctx, causation, changes, build); err != nil {
		return err
	}
	if recorder.after != nil {
		return recorder.after(ctx)
	}
	return nil
}

func (recorder *deleteEvalCDCRecorder) last(t *testing.T) string {
	t.Helper()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.causations) == 0 {
		t.Fatal("no CDC batch reached the recorder")
	}
	return recorder.causations[len(recorder.causations)-1]
}

type deleteEvalCountingTransport struct {
	events.EventTransport
	mu         sync.Mutex
	causations []golem.CausationID
}

func (transport *deleteEvalCountingTransport) Publish(ctx context.Context, batch events.EventBatch) error {
	transport.mu.Lock()
	transport.causations = append(transport.causations, batch.CausationID())
	transport.mu.Unlock()
	return transport.EventTransport.Publish(ctx, batch)
}

func (transport *deleteEvalCountingTransport) published(causation string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	count := 0
	for _, published := range transport.causations {
		if golem.UUID(published).String() == causation {
			count++
		}
	}
	return count
}

func (fixture deleteEvalFixture) cdcRecorder(t *testing.T) *cdcOutboxRecorder {
	t.Helper()
	recorder, err := fixture.app.cdcOutboxRecorder()
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func (fixture deleteEvalFixture) cdcEmitter(t *testing.T, transport events.EventTransport, recorder eventcdc.Recorder) *eventcdc.Emitter {
	t.Helper()
	encoder, err := newCDCEventEncoder(fixture.schema.Registry, fixture.app.eventSchemas, 0)
	if err != nil {
		t.Fatal(err)
	}
	emitter, err := eventcdc.NewEmitter(eventcdc.Config{Adapter: p7AuthorizationCDCAdapter{}, Transport: transport, Encoder: encoder, Recorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	return emitter
}

func (fixture deleteEvalFixture) cdcCommentRow(t *testing.T, comment, post byte, body string) golem.RuntimeModelRow {
	t.Helper()
	row, err := golem.RuntimeCDCModelRow(fixture.schema.Comment,
		golem.RuntimePresentReadCell(fixture.schema.CommentID, deleteEvalUUID(comment), nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentPostID, deleteEvalUUID(post), nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentBody, body, nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentRowVersion, int64(1), nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (fixture deleteEvalFixture) cdcPostRow(t *testing.T, post, author byte, title string) golem.RuntimeModelRow {
	t.Helper()
	row, err := golem.RuntimeCDCModelRow(fixture.schema.Post,
		golem.RuntimePresentReadCell(fixture.schema.PostID, deleteEvalUUID(post), nil),
		golem.RuntimePresentReadCell(fixture.schema.AuthorID, deleteEvalUUID(author), nil),
		golem.RuntimePresentReadCell(fixture.schema.PostTitle, title, nil),
		golem.RuntimePresentReadCell(fixture.schema.PostRowVersion, int64(1), nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (fixture deleteEvalFixture) cdcCommentDelete(t *testing.T, comment, post byte) events.CDCChangeInput {
	t.Helper()
	before := fixture.cdcCommentRow(t, comment, post, "hello")
	return events.CDCChangeInput{Model: fixture.schema.Comment, Action: golem.EventDeleted, Before: &before}
}

func (fixture deleteEvalFixture) cdcPostUpdate(t *testing.T, post, author byte, from, to string) events.CDCChangeInput {
	t.Helper()
	before, after := fixture.cdcPostRow(t, post, author, from), fixture.cdcPostRow(t, post, author, to)
	return events.CDCChangeInput{Model: fixture.schema.Post, Action: golem.EventUpdated, Before: &before, After: &after}
}

func (fixture deleteEvalFixture) cdcPostDelete(t *testing.T, post, author byte, title string) events.CDCChangeInput {
	t.Helper()
	before := fixture.cdcPostRow(t, post, author, title)
	return events.CDCChangeInput{Model: fixture.schema.Post, Action: golem.EventDeleted, Before: &before}
}

func deleteEvalCDCBatch(transaction string, changes ...events.CDCChangeInput) events.CDCBatchInput {
	ordered := make([]events.CDCChangeInput, len(changes))
	for index, change := range changes {
		change.Ordinal = uint32(index + 1)
		ordered[index] = change
	}
	return events.CDCBatchInput{SourceTransactionID: transaction, RecordedAt: deleteEvalCDCRecordedAt, Cursor: []byte(transaction), Changes: ordered}
}

func (fixture deleteEvalFixture) emitCDC(t *testing.T, transaction string, changes ...events.CDCChangeInput) string {
	t.Helper()
	recorder := &deleteEvalCDCRecorder{inner: fixture.cdcRecorder(t)}
	if err := fixture.cdcEmitter(t, fixture.transport, recorder).Emit(context.Background(), deleteEvalCDCBatch(transaction, changes...)); err != nil {
		t.Fatal(err)
	}
	return recorder.last(t)
}

func (fixture deleteEvalFixture) emitCDCDelete(t *testing.T, transaction string, comment, post byte) string {
	t.Helper()
	return fixture.emitCDC(t, transaction, fixture.cdcCommentDelete(t, comment, post))
}

func (fixture deleteEvalFixture) deliveryTable(t *testing.T) string {
	t.Helper()
	namespace, ok := fixture.app.registry.PhysicalSystemNamespace(fixture.app.eventProvider)
	if !ok {
		t.Fatal("system namespace is unavailable")
	}
	return `"` + string(namespace) + `"."_golem_outbox_delivery"`
}

func (fixture deleteEvalFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := fixture.app.database.GetContext(context.Background(), &count, fixture.app.database.Rebind(query), args...); err != nil {
		t.Fatal(err)
	}
	return count
}

func (fixture deleteEvalFixture) recordedFacts(t *testing.T, causation string) int {
	t.Helper()
	return fixture.count(t, `SELECT COUNT(*) FROM `+fixture.outboxTable(t)+` WHERE "causation_id" = ?`, causation)
}

func (fixture deleteEvalFixture) expectRecordedDelivered(t *testing.T, causation string, facts int) {
	t.Helper()
	if got := fixture.recordedFacts(t, causation); got != facts {
		t.Fatalf("recorded %d CDC facts for %s, want %d", got, causation, facts)
	}
	delivery, err := fixture.app.eventDelivery.Inspect(context.Background(), causation)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != eventprovider.StatusDelivered || delivery.AttemptCount != 0 || delivery.DeliveredAt == nil || delivery.LeaseToken != "" || delivery.LeaseUntil != nil ||
		!delivery.FirstRecordedAt.Equal(deleteEvalCDCRecordedAt) || delivery.ImmutableFactRows != facts {
		t.Fatalf("CDC delivery state=%#v", delivery)
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
		causation := fixture.emitCDC(t, "cdc-leak",
			fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "draft"),
			fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished),
		)
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
		fixture.expectRecordedDelivered(t, causation, 2)
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
		causation := fixture.emitCDC(t, "cdc-under",
			fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "draft"),
			fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished),
		)
		expectDeleteResync(t, stream)
		if fixture.suppressions(events.SuppressionUnauthorized) != 0 {
			t.Fatal("a CDC delete over a relational policy was suppressed as unauthorized")
		}
		fixture.expectRecordedDelivered(t, causation, 2)
	})
}

func TestCDCDeleteOverUnchangedParentIsDecidedExactly(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.removeComment(t, deleteEvalOnDraft)
		denied := fixture.emitCDCDelete(t, "cdc-unchanged-denied", deleteEvalOnDraft, deleteEvalDraft)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		fixture.removeComment(t, deleteEvalOnPub)
		allowed := fixture.emitCDCDelete(t, "cdc-unchanged-allowed", deleteEvalOnPub, deleteEvalPublished)
		expectCDCDeleteDelivered(t, stream)
		expectNoUnverifiable(t, fixture)
		fixture.expectRecordedDelivered(t, denied, 1)
		fixture.expectRecordedDelivered(t, allowed, 1)
	})
}

func TestCDCDeleteJudgesParentDeletedInSourceTransactionFromItsSnapshot(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.emitCDC(t, "cdc-cascade-allowed",
			fixture.cdcPostDelete(t, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished),
		)
		expectCDCDeleteDelivered(t, stream)
		fixture.cascadePost(t, deleteEvalDraft)
		fixture.emitCDC(t, "cdc-cascade-denied",
			fixture.cdcPostDelete(t, deleteEvalDraft, deleteEvalBob, "draft"),
			fixture.cdcCommentDelete(t, deleteEvalOnDraft, deleteEvalDraft),
		)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		expectNoUnverifiable(t, fixture)
	})
}

func TestCDCDeleteWhoseFactsRetentionRemovedResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		recorder := &deleteEvalCDCRecorder{inner: fixture.cdcRecorder(t), after: func(ctx context.Context) error {
			_, err := fixture.app.eventDelivery.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: time.Now().Add(time.Hour), MaxRows: eventprovider.MaximumRetentionRows})
			return err
		}}
		fixture.removeComment(t, deleteEvalOnPub)
		if err := fixture.cdcEmitter(t, fixture.transport, recorder).Emit(context.Background(), deleteEvalCDCBatch("cdc-retained", fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished))); err != nil {
			t.Fatal(err)
		}
		if facts := fixture.recordedFacts(t, recorder.last(t)); facts != 0 {
			t.Fatalf("retention left %d CDC facts", facts)
		}
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
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
		emitter := fixture.cdcEmitter(t, fixture.transport, &deleteEvalCDCRecorder{discard: true})
		fixture.removeComment(t, deleteEvalOnDraft)
		if err := emitter.Emit(context.Background(), deleteEvalCDCBatch("cdc-denied", fixture.cdcCommentDelete(t, deleteEvalOnDraft, deleteEvalDraft))); err != nil {
			t.Fatal(err)
		}
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		fixture.removeComment(t, deleteEvalOnPub)
		if err := emitter.Emit(context.Background(), deleteEvalCDCBatch("cdc-allowed", fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished))); err != nil {
			t.Fatal(err)
		}
		expectCDCDeleteDelivered(t, stream)
		if fixture.suppressions(events.SuppressionDeletionUnverifiable) != 0 {
			t.Fatal("a scalar-only CDC delete was reported unverifiable")
		}
	})
}

func TestCDCRecordedBatchIsNeverRepublishedByTheOutboxPublisher(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		transport := &deleteEvalCountingTransport{EventTransport: fixture.transport}
		recorder := &deleteEvalCDCRecorder{inner: fixture.cdcRecorder(t)}
		if err := fixture.cdcEmitter(t, transport, recorder).Emit(context.Background(), deleteEvalCDCBatch("cdc-once", fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished))); err != nil {
			t.Fatal(err)
		}
		recorded := recorder.last(t)
		ordinary := fixture.factRows(t, 300, fixture.commentFact(t, 301, 1, deleteEvalOnDraft, deleteEvalDraft))
		fixture.storeRows(t, ordinary)
		limits := fixture.app.eventLimits
		publisher, err := eventoutbox.NewPublisherObserved(fixture.app.eventDelivery, fixture.app.eventSchemas, transport, eventoutbox.Limits{
			ClaimGroups: limits.ClaimRows, Concurrency: limits.PublisherConcurrency,
			LeaseDuration: limits.LeaseDuration, PublishTimeout: limits.PublishTimeout,
			RetryBase: 5 * time.Millisecond, RetryCap: 10 * time.Millisecond,
			ShutdownGrace: limits.ShutdownGrace, MaxEncodedBytes: limits.MaxEncodedEventBytes,
			MaxBatchBytes: limits.MaxEncodedBatchBytes,
			RetentionAge:  limits.RetentionAge, RetentionEvery: events.RetentionDisabled,
			RetentionRows: limits.RetentionDeleteRows,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- publisher.Run(ctx) }()
		deadline := time.Now().Add(3 * time.Second)
		for transport.published(ordinary[0].CausationID) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got := transport.published(ordinary[0].CausationID); got != 1 {
			t.Fatalf("publisher delivered the ordinary outbox group %d times", got)
		}
		if got := transport.published(recorded); got != 1 {
			t.Fatalf("transport saw the recorded CDC batch %d times, want exactly once", got)
		}
		fixture.expectRecordedDelivered(t, recorded, 1)
	})
}

func TestCDCReplayRecordsNothingNewAndPublishesAgain(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		transport := &deleteEvalCountingTransport{EventTransport: fixture.transport}
		recorder := &deleteEvalCDCRecorder{inner: fixture.cdcRecorder(t)}
		emitter := fixture.cdcEmitter(t, transport, recorder)
		batch := deleteEvalCDCBatch("cdc-replay",
			fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "draft"),
			fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished),
		)
		for attempt := 0; attempt < 2; attempt++ {
			if err := emitter.Emit(context.Background(), batch); err != nil {
				t.Fatalf("emit %d: %v", attempt, err)
			}
		}
		causation := recorder.last(t)
		if got := transport.published(causation); got != 2 {
			t.Fatalf("replayed batch was published %d times, want 2", got)
		}
		if got := fixture.count(t, `SELECT COUNT(*) FROM `+fixture.deliveryTable(t)+` WHERE "causation_id" = ?`, causation); got != 1 {
			t.Fatalf("replay left %d delivery rows", got)
		}
		fixture.expectRecordedDelivered(t, causation, 2)

		changed := deleteEvalCDCBatch("cdc-replay",
			fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "draft"),
			fixture.cdcCommentDelete(t, deleteEvalOnDraft, deleteEvalDraft),
		)
		err := emitter.Emit(context.Background(), changed)
		if code, ok := events.CodeOf(err); !ok || code != events.CodeCDCInvalid {
			t.Fatalf("divergent replay error=%v, want %s", err, events.CodeCDCInvalid)
		}
		if got := transport.published(causation); got != 2 {
			t.Fatalf("divergent replay was published (%d publications)", got)
		}
		fixture.expectRecordedDelivered(t, causation, 2)
	})
}

func TestCDCRecordFailurePublishesNothing(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		closed, _, err := sqlite.New().Open(context.Background(), "file:"+t.TempDir()+"/closed.db")
		if err != nil {
			t.Fatal(err)
		}
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		broken := fixture.cdcRecorder(t)
		broken.database = closed
		transport := &deleteEvalCountingTransport{EventTransport: fixture.transport}
		recorder := &deleteEvalCDCRecorder{inner: broken}
		err = fixture.cdcEmitter(t, transport, recorder).Emit(context.Background(), deleteEvalCDCBatch("cdc-unrecorded", fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished)))
		if code, ok := events.CodeOf(err); !ok || code != events.CodeCDCUnavailable {
			t.Fatalf("record failure error=%v, want %s", err, events.CodeCDCUnavailable)
		}
		causation := recorder.last(t)
		if got := transport.published(causation); got != 0 {
			t.Fatalf("unrecorded batch was published %d times", got)
		}
		if got := fixture.recordedFacts(t, causation); got != 0 {
			t.Fatalf("failed record left %d facts", got)
		}
	})
}

func TestCDCBatchOverTheOutboxLimitsFailsClosed(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		for name, limit := range map[string]func(*cdcOutboxRecorder){
			"facts": func(recorder *cdcOutboxRecorder) { recorder.limits.facts = 1 },
			"bytes": func(recorder *cdcOutboxRecorder) { recorder.limits.outboxBytes = 64 },
		} {
			t.Run(name, func(t *testing.T) {
				limited := fixture.cdcRecorder(t)
				limit(limited)
				transport := &deleteEvalCountingTransport{EventTransport: fixture.transport}
				recorder := &deleteEvalCDCRecorder{inner: limited}
				err := fixture.cdcEmitter(t, transport, recorder).Emit(context.Background(), deleteEvalCDCBatch("cdc-over-"+name,
					fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "draft"),
					fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished),
				))
				if code, ok := events.CodeOf(err); !ok || code != events.CodeCDCInvalid {
					t.Fatalf("over-limit batch error=%v, want %s", err, events.CodeCDCInvalid)
				}
				causation := recorder.last(t)
				if transport.published(causation) != 0 || fixture.recordedFacts(t, causation) != 0 {
					t.Fatal("over-limit CDC batch was recorded or published")
				}
			})
		}
	})
}

type deleteEvalCDCAdapter struct{ provider golem.Provider }

func (adapter deleteEvalCDCAdapter) Identity() events.CDCIdentity {
	return events.CDCIdentity{Name: "delete-eval", Version: "1", Provider: adapter.provider}
}
func (deleteEvalCDCAdapter) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}
func (deleteEvalCDCAdapter) Run(context.Context, events.CDCEmitter) error { return nil }

func TestCDCWorkersOpenedByTheRuntimeRecordTheirBatches(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		if err := fixture.app.initializeEventRuntime([]events.CDCAdapter{deleteEvalCDCAdapter{provider: fixture.app.eventProvider}}, func(context.Context, events.OperatorAuditRecord) {}); err != nil {
			t.Fatal(err)
		}
		if len(fixture.app.eventCDCWorkers) != 1 {
			t.Fatalf("runtime opened %d CDC workers", len(fixture.app.eventCDCWorkers))
		}
		stream := fixture.subscribeComments(t, "alice")
		fixture.removeComment(t, deleteEvalOnPub)
		if err := fixture.app.eventCDCWorkers[0].emitter.Emit(context.Background(), deleteEvalCDCBatch("cdc-runtime-worker", fixture.cdcCommentDelete(t, deleteEvalOnPub, deleteEvalPublished))); err != nil {
			t.Fatal(err)
		}
		expectCDCDeleteDelivered(t, stream)
		var causation string
		if err := fixture.app.database.GetContext(context.Background(), &causation, `SELECT "causation_id" FROM `+fixture.outboxTable(t)); err != nil {
			t.Fatal(err)
		}
		fixture.expectRecordedDelivered(t, causation, 1)
	})
}
