package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type deleteSnapshotObservations struct {
	mu     sync.Mutex
	values []events.Observation
}

func (capture *deleteSnapshotObservations) ObserveEvent(_ context.Context, value events.Observation) {
	capture.mu.Lock()
	capture.values = append(capture.values, value)
	capture.mu.Unlock()
}

func (capture *deleteSnapshotObservations) kind(kind events.ObservationKind) []events.Observation {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var result []events.Observation
	for _, value := range capture.values {
		if value.Kind() == kind {
			result = append(result, value)
		}
	}
	return result
}

func (capture *deleteSnapshotObservations) await(t *testing.T, kind events.ObservationKind) events.Observation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if values := capture.kind(kind); len(values) != 0 {
			return values[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("observation %s was not received", kind)
	return events.Observation{}
}

type deleteSnapshotStoredFact struct {
	Action         string `db:"action"`
	BeforeIdentity []byte `db:"before_identity"`
	AfterIdentity  []byte `db:"after_identity"`
	Metadata       []byte `db:"metadata"`
	DeleteSnapshot []byte `db:"delete_snapshot"`
}

func (fact deleteSnapshotStoredFact) encodedBytes() int {
	return len(fact.BeforeIdentity) + len(fact.AfterIdentity) + len(fact.Metadata) + len(fact.DeleteSnapshot)
}

func deleteSnapshotFacts(t *testing.T, fixture mutationResultFixture) []deleteSnapshotStoredFact {
	t.Helper()
	var stored []deleteSnapshotStoredFact
	query := `SELECT "action","before_identity","after_identity","metadata","delete_snapshot" FROM ` + nestedAcceptanceOutbox(fixture.app) + ` ORDER BY "recorded_at","transaction_ordinal"`
	if err := fixture.app.database.SelectContext(context.Background(), &stored, query); err != nil {
		t.Fatal(err)
	}
	return stored
}

func clearDeleteSnapshotFacts(t *testing.T, fixture mutationResultFixture) {
	t.Helper()
	if _, err := fixture.app.database.ExecContext(context.Background(), `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
}

func TestMutationLimitsNormalizeDeleteSnapshotBytes(t *testing.T) {
	defaulted, err := normalizeMutationLimits(MutationLimits{})
	if err != nil || defaulted.deleteSnapshotBytes != DefaultMutationDeleteSnapshotBytes || DefaultMutationDeleteSnapshotBytes != 64<<10 {
		t.Fatalf("default delete snapshot limit=%d err=%v", defaulted.deleteSnapshotBytes, err)
	}
	lowered, err := normalizeMutationLimits(MutationLimits{MaxDeleteSnapshotBytes: 512})
	if err != nil || lowered.deleteSnapshotBytes != 512 {
		t.Fatalf("lowered delete snapshot limit=%d err=%v", lowered.deleteSnapshotBytes, err)
	}
	ceiling, err := normalizeMutationLimits(MutationLimits{MaxDeleteSnapshotBytes: MaxMutationDeleteSnapshotBytes})
	if err != nil || ceiling.deleteSnapshotBytes != 1<<20 {
		t.Fatalf("ceiling delete snapshot limit=%d err=%v", ceiling.deleteSnapshotBytes, err)
	}
	if _, err := normalizeMutationLimits(MutationLimits{MaxDeleteSnapshotBytes: MaxMutationDeleteSnapshotBytes + 1}); err == nil {
		t.Fatal("delete snapshot limit above the ceiling was accepted")
	}
	if _, err := normalizeMutationLimits(MutationLimits{MaxDeleteSnapshotBytes: -1}); err == nil {
		t.Fatal("negative delete snapshot limit was accepted")
	}
}

func TestDefaultDeleteSnapshotIsAboutTheSizeOfAnUpdate(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	system := fixture.app.System()
	large := strings.Repeat("t", 40*1024)
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(211, golem.UUID{15: 1}, large)); err != nil {
		t.Fatal(err)
	}
	clearDeleteSnapshotFacts(t, fixture)
	update := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, large+"u"))
	if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(211), update); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(211)); err != nil {
		t.Fatal(err)
	}
	stored := deleteSnapshotFacts(t, fixture)
	if len(stored) != 2 || stored[0].Action != "updated" || stored[1].Action != "deleted" {
		t.Fatalf("facts=%#v", stored)
	}
	updated, deleted := stored[0].encodedBytes(), stored[1].encodedBytes()
	if len(stored[1].DeleteSnapshot) == 0 || deleted > updated+256 {
		t.Fatalf("delete fact bytes=%d snapshot=%d update fact bytes=%d", deleted, len(stored[1].DeleteSnapshot), updated)
	}
}

func TestDeclaredDeleteSnapshotOversizeIsUnverifiableAndObserved(t *testing.T) {
	ctx := context.Background()
	fixture := openMutationResultFixture(t, schematest.NewSubscribedIndexedWithFullDeleteSnapshot(t), MutationLimits{}, nil, nil, nil, true)
	capture := &deleteSnapshotObservations{}
	fixture.app.eventObserver = capture
	system := fixture.app.System()
	huge := strings.Repeat("h", 100*1024)
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(221, golem.UUID{15: 1}, huge)); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(222, golem.UUID{15: 1}, "small")); err != nil {
		t.Fatal(err)
	}
	clearDeleteSnapshotFacts(t, fixture)
	if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(221)); err != nil {
		t.Fatalf("oversized delete failed: %v", err)
	}
	if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(222)); err != nil {
		t.Fatal(err)
	}
	stored := deleteSnapshotFacts(t, fixture)
	if len(stored) != 2 {
		t.Fatalf("facts=%d", len(stored))
	}
	states := make([]mutationir.DeleteSnapshotState, len(stored))
	for index, row := range stored {
		envelope, err := decodeCurrentMutationFact(fixture.schema.Registry, policyir.ModelID(fixture.schema.Post), row.Metadata, row.DeleteSnapshot)
		if err != nil {
			t.Fatalf("fact %d: %v", index, err)
		}
		states[index] = envelope.DeleteSnapshotState()
		_, present := envelope.PrivateDeleteSnapshot()
		if present != (states[index] == mutationir.DeleteSnapshotStoredScalars) {
			t.Fatalf("fact %d state=%d snapshot present=%t", index, states[index], present)
		}
	}
	if states[0] != mutationir.DeleteSnapshotUnverifiable || len(stored[0].DeleteSnapshot) != 0 || states[1] != mutationir.DeleteSnapshotStoredScalars {
		t.Fatalf("delete states=%v oversized snapshot bytes=%d", states, len(stored[0].DeleteSnapshot))
	}
	observed := capture.kind(events.ObservationDeleteSnapshotOversize)
	if len(observed) != 1 {
		t.Fatalf("oversize observations=%d", len(observed))
	}
	value := observed[0]
	if value.ModelID() != fixture.schema.Post || value.Action() != golem.EventDeleted || value.QueueLimit() != DefaultMutationDeleteSnapshotBytes || value.AggregateCount() <= int64(len(huge)) || value.SuppressionReason() != events.SuppressionDeletionUnverifiable {
		t.Fatalf("oversize observation=%#v", value)
	}
}

func TestDeleteAuthorizationOutsideSnapshotIsUnverifiableAndReportsCoverage(t *testing.T) {
	title := func(schemaFixture schematest.Fixture) golem.TextField[p7EventPost, string] {
		return golem.GeneratedTextField[p7EventPost, string](schemaFixture.PostTitle)
	}
	visible := func(schemaFixture schematest.Fixture) golem.Predicate[p7EventPost] {
		return title(schemaFixture).Eq("visible")
	}
	open := func(t *testing.T, schemaFixture schematest.Fixture, policy func(schematest.Fixture) golem.Predicate[p7EventPost]) (p7EventRuntimeFixture, *deleteSnapshotObservations, golem.EventStream[p7EventOracleValue], context.CancelFunc) {
		t.Helper()
		fixture := newP7EventRuntimeFixtureWith(t, schemaFixture, policy)
		capture := &deleteSnapshotObservations{}
		fixture.app.eventObserver = capture
		ctx, cancel := context.WithCancel(context.Background())
		caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		second, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = second.Close() })
		select {
		case <-fixture.transport.subscribed:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("model hub did not subscribe")
		}
		return fixture, capture, stream, cancel
	}
	assertUnverifiable := func(t *testing.T, policy func(schematest.Fixture) golem.Predicate[p7EventPost], id uint16) {
		t.Helper()
		fixture, capture, stream, cancel := open(t, schematest.NewSubscribedIndexed(t), policy)
		defer cancel()
		defer stream.Close()
		coverage := capture.kind(events.ObservationDeleteCoverage)
		if len(coverage) != 1 || coverage[0].ModelID() != fixture.schema.Post || coverage[0].AggregateCount() != 1 || coverage[0].Outcome() != events.OutcomeSuccess || coverage[0].SuppressionReason() != "" {
			t.Fatalf("coverage observations=%#v", coverage)
		}
		if suppressions := capture.kind(events.ObservationSuppression); len(suppressions) != 0 {
			t.Fatalf("stream open recorded suppressions=%#v", suppressions)
		}
		fixture.publish(t, fixture.notice(t, golem.EventDeleted, id, "visible", false))
		suppressed := capture.await(t, events.ObservationSuppression)
		if suppressed.SuppressionReason() != events.SuppressionDeletionUnverifiable {
			t.Fatalf("delete outside the snapshot suppression=%q", suppressed.SuppressionReason())
		}
		fixture.publish(t, fixture.notice(t, golem.EventCreated, id+1, "visible", false))
		event := receiveP7Event(t, stream)
		if event.validated.Metadata().EventID() != p7OracleID(id+1) {
			t.Fatalf("stream delivered %x after the unverifiable delete", event.validated.Metadata().EventID())
		}
		for _, suppression := range capture.kind(events.ObservationSuppression) {
			if suppression.SuppressionReason() != events.SuppressionDeletionUnverifiable || suppression.Action() != golem.EventDeleted {
				t.Fatalf("unexpected suppression=%#v", suppression)
			}
		}
	}

	t.Run("outside", func(t *testing.T) {
		assertUnverifiable(t, visible, 801)
	})

	t.Run("outside-negated", func(t *testing.T) {
		assertUnverifiable(t, func(schemaFixture schematest.Fixture) golem.Predicate[p7EventPost] {
			return golem.Not(title(schemaFixture).Eq("hidden"))
		}, 821)
	})

	t.Run("outside-tautology", func(t *testing.T) {
		assertUnverifiable(t, func(schemaFixture schematest.Fixture) golem.Predicate[p7EventPost] {
			return golem.Or(golem.Not(title(schemaFixture).Eq("visible")), title(schemaFixture).Eq("visible"))
		}, 831)
	})

	t.Run("inside", func(t *testing.T) {
		fixture, capture, stream, cancel := open(t, schematest.NewSubscribedIndexedWithFullDeleteSnapshot(t), visible)
		defer cancel()
		defer stream.Close()
		if coverage := capture.kind(events.ObservationDeleteCoverage); len(coverage) != 0 {
			t.Fatalf("covered policy reported coverage=%#v", coverage)
		}
		fixture.publish(t, fixture.notice(t, golem.EventDeleted, 811, "visible", false))
		event := receiveP7Event(t, stream)
		if event.validated.Metadata().EventID() != p7OracleID(811) || event.validated.Metadata().Action() != golem.EventDeleted {
			t.Fatalf("covered delete event=%#v", event.validated.Metadata())
		}
	})
}

func TestDeleteCoverageReportsRelationDependenciesThatCannotBeHydrated(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	capture := &deleteSnapshotObservations{}
	fixture.app.eventObserver = capture
	post, user := policyir.ModelID(fixture.schema.Post), policyir.ModelID(fixture.schema.User)
	truth, err := policyir.NewConstant(post, true)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := policyir.NewRelation(user, policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship), post, policyir.RelationToMany, policyir.OperatorRelationSome, &truth, nil)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := policyir.NewRelation(post, policyir.FieldID(fixture.schema.PostAuthor), policyir.RelationID(fixture.schema.Authorship), user, policyir.RelationToOne, policyir.OperatorRelationIs, &posts, nil)
	if err != nil {
		t.Fatal(err)
	}
	userTruth, err := policyir.NewConstant(user, true)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := policyir.NewRelation(post, policyir.FieldID(fixture.schema.PostAuthor), policyir.RelationID(fixture.schema.Authorship), user, policyir.RelationToOne, policyir.OperatorRelationIs, &userTruth, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app.observeDeleteCoverage(context.Background(), fixture.schema.Post, direct)
	if coverage := capture.kind(events.ObservationDeleteCoverage); len(coverage) != 0 {
		t.Fatalf("hydratable forward hop reported coverage=%#v", coverage)
	}
	fixture.app.observeDeleteCoverage(context.Background(), fixture.schema.Post, nested)
	fixture.app.observeDeleteCoverage(context.Background(), fixture.schema.Post, nested)
	coverage := capture.kind(events.ObservationDeleteCoverage)
	if len(coverage) != 1 || coverage[0].ModelID() != fixture.schema.Post || coverage[0].AggregateCount() != 1 || coverage[0].Outcome() != events.OutcomeSuccess {
		t.Fatalf("nested relation coverage=%#v", coverage)
	}
	caller, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	titleValue, err := policyir.StringValue("visible")
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewCompleteRow(fixture.schema.Registry, post, []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue([16]byte{15: 9})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue([16]byte{15: 1})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), titleValue),
	})
	if err != nil {
		t.Fatal(err)
	}
	model, _ := fixture.schema.Registry.Model(fixture.schema.Post)
	_, declared, _ := model.EventSchema()
	fields := make([]policyir.FieldID, len(declared))
	for index, field := range declared {
		fields[index] = policyir.FieldID(field)
	}
	snapshot, err := row.Select(fixture.schema.Registry, fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, verifiable, err := fixture.app.deleteSnapshotRecord(context.Background(), caller, snapshot, nested); err != nil || verifiable {
		t.Fatalf("nested relation delete verifiable=%t err=%v", verifiable, err)
	}
}
