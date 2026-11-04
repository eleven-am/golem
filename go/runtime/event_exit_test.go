package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func updateFactRequirement(registry *schema.Registry, model policyir.ModelID, identity []policyir.FieldID) (mutationir.FactRequirement, error) {
	_, _, snapshot, err := mutationplan.ModelEventSchema(registry, model)
	if err != nil {
		return mutationir.FactRequirement{}, err
	}
	return mutationir.NewUpdateFactRequirement(identity, identity, mutationir.DeleteSnapshotStoredScalars, snapshot)
}

func postAuthorOf(t testing.TB, fixture mutationResultFixture, envelope mutationfact.Envelope) byte {
	t.Helper()
	if envelope.DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars {
		t.Fatalf("%d fact has snapshot state %d, want stored scalars", envelope.Action(), envelope.DeleteSnapshotState())
	}
	snapshot, present := envelope.PrivateDeleteSnapshot()
	if !present {
		t.Fatal("fact carries no private snapshot")
	}
	cell, present := snapshot.Cell(policyir.FieldID(fixture.schema.AuthorID))
	if !present {
		t.Fatal("snapshot omits the author")
	}
	value, _ := cell.PolicyValue()
	author, _ := value.UUID()
	return author[15]
}

func outboxPostFacts(t testing.TB, fixture mutationResultFixture) []mutationfact.Envelope {
	t.Helper()
	var stored []struct {
		ModelID        string `db:"model_id"`
		Metadata       []byte `db:"metadata"`
		DeleteSnapshot []byte `db:"delete_snapshot"`
	}
	if err := fixture.app.database.Select(&stored, `SELECT "model_id","metadata","delete_snapshot" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	result := make([]mutationfact.Envelope, len(stored))
	for index, row := range stored {
		envelope, err := decodeRuntimeMutationFact(fixture.app.registry, mutationfact.OutboxRow{ModelID: row.ModelID, Metadata: row.Metadata, DeleteSnapshot: row.DeleteSnapshot})
		if err != nil {
			t.Fatal(err)
		}
		result[index] = envelope
	}
	return result
}

func TestUpdatedFactsCarryTheBeforeSnapshotOnEveryWritePath(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, func(t testing.TB, fixture mutationResultFixture) {
		ctx := context.Background()
		for _, row := range []struct{ id, author byte }{{10, 1}, {11, 1}, {12, 1}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(row.id, golem.UUID{15: row.author}, "before")); err != nil {
				t.Fatal(err)
			}
		}
		clear := func() {
			if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
				t.Fatal(err)
			}
		}
		expect := func(path string, authors ...byte) {
			t.Helper()
			facts := outboxPostFacts(t, fixture)
			if len(facts) != len(authors) {
				t.Fatalf("%s recorded %d facts, want %d", path, len(facts), len(authors))
			}
			for index, envelope := range facts {
				if envelope.Action() != mutationir.FactUpdated {
					t.Fatalf("%s fact %d action = %d", path, index, envelope.Action())
				}
				if got := postAuthorOf(t, fixture, envelope); got != authors[index] {
					t.Fatalf("%s fact %d snapshot author = %d, want the before image %d", path, index, got, authors[index])
				}
			}
		}
		moveToTwo := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))
		moveManyToTwo := golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))

		clear()
		if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(10), moveToTwo); err != nil {
			t.Fatal(err)
		}
		expect("scalar update", 1)

		clear()
		if _, err := SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 11}, golem.UUID{15: 12}), moveManyToTwo); err != nil {
			t.Fatal(err)
		}
		expect("update-many", 1, 1)
	})
}

const exitCarol byte = 3

func exitActorID(name string) golem.UUID {
	switch name {
	case "alice":
		return deleteEvalUUID(deleteEvalAlice)
	case "bob":
		return deleteEvalUUID(deleteEvalBob)
	default:
		return deleteEvalUUID(exitCarol)
	}
}

func exitOwnerPolicies() deleteEvalPolicies {
	return deleteEvalPolicies{
		post: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			return fields.postAuthorID.Eq(exitActorID(actor.Name)), true
		},
		comment: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthorID.Eq(exitActorID(actor.Name))), true
		},
	}
}

func (fixture deleteEvalFixture) publishUpdate(t *testing.T, model golem.ModelID, identity golem.FieldID, eventID uint16, version int64, before, after []mutationdecode.Cell) {
	t.Helper()
	beforeRow, err := mutationdecode.NewCompleteRow(fixture.schema.Registry, policyir.ModelID(model), withTestRowVersion(fixture.schema.Registry, model, before))
	if err != nil {
		t.Fatal(err)
	}
	afterRow, err := mutationdecode.NewCompleteRow(fixture.schema.Registry, policyir.ModelID(model), withTestRowVersion(fixture.schema.Registry, model, after))
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := updateFactRequirement(fixture.schema.Registry, policyir.ModelID(model), []policyir.FieldID{policyir.FieldID(identity)})
	if err != nil {
		t.Fatal(err)
	}
	digest := fixture.digests[model]
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	id := p7OracleID(eventID)
	fact, err := mutationfact.New(fixture.schema.Registry, golem.SchemaDigest(digest), mutationfact.EventID(id), requirement, mutationfact.CausationID(golem.CausationID(id)), 1, version, &beforeRow, &afterRow)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := fact.OutboxRow(time.Unix(int64(eventID), 0))
	if err != nil {
		t.Fatal(err)
	}
	fixture.storeRows(t, []mutationfact.OutboxRow{outbox})
	envelope, err := eventcodec.EncodeStoredRow(outbox, fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := eventvalue.NewEventBatch(notice.CausationID(), []events.Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func (fixture deleteEvalFixture) postCells(t *testing.T, post, author byte, title string) []mutationdecode.Cell {
	t.Helper()
	value, err := policyir.StringValue(title)
	if err != nil {
		t.Fatal(err)
	}
	return []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue(deleteEvalUUID(post))),
		mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue(deleteEvalUUID(author))),
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), value),
	}
}

func (fixture deleteEvalFixture) commentCells(t *testing.T, comment, post byte, body string) []mutationdecode.Cell {
	t.Helper()
	value, err := policyir.StringValue(body)
	if err != nil {
		t.Fatal(err)
	}
	return []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentID), policyir.UUIDValue(deleteEvalUUID(comment))),
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentPostID), policyir.UUIDValue(deleteEvalUUID(post))),
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentBody), value),
	}
}

func (fixture deleteEvalFixture) transferPost(t *testing.T, post, author byte) {
	t.Helper()
	fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+` SET "author_id" = ? WHERE "id" = ?`, mutationResultUUIDText(author), mutationResultUUIDText(post))
}

func (fixture deleteEvalFixture) ownerStreams(t *testing.T, filtered bool) (alice, bob, carol golem.EventStream[p7EventOracleValue]) {
	t.Helper()
	open := func(name string) golem.EventStream[p7EventOracleValue] {
		options := []golem.EventOption[deleteEvalPost]{golem.EventSelect[deleteEvalPost](fixture.fields.postTitle)}
		if filtered {
			options = append(options, golem.EventWhere[deleteEvalPost](fixture.fields.postAuthorID.Eq(exitActorID(name))))
		}
		return fixture.subscribeActor(t, fixture.schema.Post, name, options...)
	}
	return open("alice"), open("bob"), open("carol")
}

func expectExited(t *testing.T, stream golem.EventStream[p7EventOracleValue], post byte, version int64) {
	t.Helper()
	event := receiveP7Event(t, stream).validated
	metadata := event.Metadata()
	if metadata.Action() != golem.EventExited || metadata.Version() != version {
		t.Fatalf("exit event action = %q version = %d, want exited at version %d", metadata.Action(), metadata.Version(), version)
	}
	if identity := event.IdentityValues(); len(identity) != 1 || identity[0] != deleteEvalUUID(post) {
		t.Fatalf("exit event identity = %v, want the post that left the list", identity)
	}
	if _, present := event.Entity(); present {
		t.Fatal("an exit carries a row")
	}
}

func TestExitedEventReachesOnlyTheSubscribersThatHeldTheRow(t *testing.T) {
	for name, variant := range map[string]struct {
		policies deleteEvalPolicies
		filtered bool
	}{
		"policy": {exitOwnerPolicies(), false},
		"filter": {deleteEvalPolicies{}, true},
	} {
		t.Run(name, func(t *testing.T) {
			runDeleteEvalProfiles(t, variant.policies, func(t *testing.T, fixture deleteEvalFixture) {
				alice, bob, carol := fixture.ownerStreams(t, variant.filtered)
				fixture.transferPost(t, deleteEvalPublished, deleteEvalBob)
				fixture.publishUpdate(t, fixture.schema.Post, fixture.schema.PostID, 301, 7,
					fixture.postCells(t, deleteEvalPublished, deleteEvalAlice, "published"), fixture.postCells(t, deleteEvalPublished, deleteEvalBob, "published"))
				expectExited(t, alice, deleteEvalPublished, 7)
				if event := receiveP7Event(t, bob).validated.Metadata(); event.Action() != golem.EventUpdated {
					t.Fatalf("the new owner received %q, want updated", event.Action())
				}
				expectStreamSilent(t, carol)
				if fixture.suppressions(events.SuppressionFiltered) != 1 {
					t.Fatalf("filtered suppressions = %d, want the stream that never held the row suppressed once", fixture.suppressions(events.SuppressionFiltered))
				}
			})
		})
	}
}

func TestExitFollowsAHopWhoseKeyMovedWithTheUpdate(t *testing.T) {
	runDeleteEvalProfiles(t, exitOwnerPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
		alice := fixture.subscribeActor(t, fixture.schema.Comment, "alice")
		bob := fixture.subscribeActor(t, fixture.schema.Comment, "bob")
		fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` SET "post_id" = ? WHERE "id" = ?`, mutationResultUUIDText(deleteEvalDraft), mutationResultUUIDText(deleteEvalOnPub))
		fixture.publishUpdate(t, fixture.schema.Comment, fixture.schema.CommentID, 303, 7,
			fixture.commentCells(t, deleteEvalOnPub, deleteEvalPublished, "hello"), fixture.commentCells(t, deleteEvalOnPub, deleteEvalDraft, "hello"))
		expectExited(t, alice, deleteEvalOnPub, 7)
		if event := receiveP7Event(t, bob).validated.Metadata(); event.Action() != golem.EventUpdated {
			t.Fatalf("the owner of the new post received %q, want updated", event.Action())
		}
	})
}

func TestExitThatTheSnapshotCannotDecideResyncs(t *testing.T) {
	t.Run("a hook filter outside the snapshot", func(t *testing.T) {
		policies := deleteEvalPolicies{hook: func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return fields.commentBody.Eq("hi")
		}}
		runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
			stream := fixture.subscribeComments(t, "alice")
			fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` SET "body" = ? WHERE "id" = ?`, "hello again", mutationResultUUIDText(deleteEvalOnPub))
			fixture.publishUpdate(t, fixture.schema.Comment, fixture.schema.CommentID, 304, 7,
				fixture.commentCells(t, deleteEvalOnPub, deleteEvalPublished, "hello"), fixture.commentCells(t, deleteEvalOnPub, deleteEvalPublished, "hello again"))
			expectStreamEnds(t, stream, events.CodeSubscriptionResync)
		})
	})
	t.Run("a cdc update without a before image", func(t *testing.T) {
		runDeleteEvalProfiles(t, exitOwnerPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
			alice, bob, carol := fixture.ownerStreams(t, false)
			fixture.transferPost(t, deleteEvalPublished, deleteEvalBob)
			after := fixture.cdcPostRow(t, deleteEvalPublished, deleteEvalBob, "published")
			fixture.emitCDC(t, "cdc-exit-unknown", events.CDCChangeInput{Model: fixture.schema.Post, Action: golem.EventUpdated, After: &after})
			expectStreamEnds(t, alice, events.CodeSubscriptionResync)
			expectStreamEnds(t, carol, events.CodeSubscriptionResync)
			if event := receiveP7Event(t, bob).validated.Metadata(); event.Action() != golem.EventUpdated {
				t.Fatalf("the new owner received %q, want updated", event.Action())
			}
		})
	})
}

func TestCDCUpdateWithABeforeImageExitsAtTheStampedVersion(t *testing.T) {
	runDeleteEvalProfiles(t, exitOwnerPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
		alice, bob, carol := fixture.ownerStreams(t, false)
		fixture.transferPost(t, deleteEvalPublished, deleteEvalBob)
		before := fixture.cdcPostRow(t, deleteEvalPublished, deleteEvalAlice, "published")
		after := fixture.cdcPostRow(t, deleteEvalPublished, deleteEvalBob, "published")
		fixture.emitCDC(t, "cdc-exit-known", events.CDCChangeInput{Model: fixture.schema.Post, Action: golem.EventUpdated, Before: &before, After: &after})
		stamped := fixture.postVersion(t, deleteEvalPublished)
		if stamped <= 0 {
			t.Fatalf("the recorder left the row at version %d", stamped)
		}
		expectExited(t, alice, deleteEvalPublished, stamped)
		if event := receiveP7Event(t, bob).validated.Metadata(); event.Action() != golem.EventUpdated || event.Version() != stamped {
			t.Fatalf("the new owner received %q at version %d", event.Action(), event.Version())
		}
		expectStreamSilent(t, carol)
	})
}

func TestCDCEncoderRefusesAnUpdateWithoutAnAfterImage(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		encoder, err := newCDCEventEncoder(fixture.schema.Registry, fixture.app.eventSchemas, 0)
		if err != nil {
			t.Fatal(err)
		}
		before := fixture.cdcPostRow(t, deleteEvalPublished, deleteEvalAlice, "published")
		_, err = encoder.EncodeCDC(context.Background(), eventcdc.EncodeInput{
			Adapter: p7AuthorizationCDCAdapter{}.Identity(), SourceTransactionID: "no-after", RecordedAt: deleteEvalCDCRecordedAt,
			EventID: golem.EventID{1}, CausationID: golem.CausationID{2}, Ordinal: 1, Version: 3,
			Model: fixture.schema.Post, Action: golem.EventUpdated, Before: &before,
		})
		if err == nil || !strings.Contains(err.Error(), "update requires an after image") {
			t.Fatalf("an update without an after image was encoded: %v", err)
		}
	})
}

func TestExitIsJudgedAfterTheExactReadWhenTheShortcutCannotDecide(t *testing.T) {
	policies := deleteEvalPolicies{
		post: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			return golem.Or(fields.postAuthorID.Eq(exitActorID(actor.Name)), fields.postComments.Some(fields.commentBody.Eq("never"))), true
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		alice, _, carol := fixture.ownerStreams(t, false)
		fixture.transferPost(t, deleteEvalPublished, deleteEvalBob)
		fixture.publishUpdate(t, fixture.schema.Post, fixture.schema.PostID, 305, 7,
			fixture.postCells(t, deleteEvalPublished, deleteEvalAlice, "published"), fixture.postCells(t, deleteEvalPublished, deleteEvalBob, "published"))
		expectExited(t, alice, deleteEvalPublished, 7)
		expectStreamEnds(t, carol, events.CodeSubscriptionResync)
	})
}
