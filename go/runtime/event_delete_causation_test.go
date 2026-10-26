package runtime

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"
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
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type deleteEvalFact struct {
	model        golem.ModelID
	eventID      uint16
	ordinal      uint32
	action       mutationir.FactAction
	cells        []mutationdecode.Cell
	unverifiable bool
}

func deleteEvalText(t *testing.T, value string) policyir.Value {
	t.Helper()
	text, err := policyir.StringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func (fixture deleteEvalFixture) postFact(t *testing.T, eventID uint16, ordinal uint32, post, author byte, title string) deleteEvalFact {
	t.Helper()
	return deleteEvalFact{model: fixture.schema.Post, eventID: eventID, ordinal: ordinal, action: mutationir.FactDeleted, cells: []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue([16]byte{15: post})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue([16]byte{15: author})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), deleteEvalText(t, title)),
	}}
}

func (fixture deleteEvalFixture) userFact(t *testing.T, eventID uint16, ordinal uint32, user byte, name string) deleteEvalFact {
	t.Helper()
	return deleteEvalFact{model: fixture.schema.User, eventID: eventID, ordinal: ordinal, action: mutationir.FactDeleted, cells: []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.UserID), policyir.UUIDValue([16]byte{15: user})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.UserName), deleteEvalText(t, name)),
	}}
}

func (fixture deleteEvalFixture) commentFact(t *testing.T, eventID uint16, ordinal uint32, comment, post byte) deleteEvalFact {
	t.Helper()
	return deleteEvalFact{model: fixture.schema.Comment, eventID: eventID, ordinal: ordinal, action: mutationir.FactDeleted, cells: []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentID), policyir.UUIDValue([16]byte{15: comment})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentPostID), policyir.UUIDValue([16]byte{15: post})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentBody), deleteEvalText(t, "hello")),
	}}
}

func (fixture deleteEvalFixture) identityField(model golem.ModelID) golem.FieldID {
	switch model {
	case fixture.schema.User:
		return fixture.schema.UserID
	case fixture.schema.Post:
		return fixture.schema.PostID
	default:
		return fixture.schema.CommentID
	}
}

func (fixture deleteEvalFixture) factRows(t *testing.T, causation uint16, facts ...deleteEvalFact) []mutationfact.OutboxRow {
	t.Helper()
	rows := make([]mutationfact.OutboxRow, 0, len(facts))
	for _, fact := range facts {
		row, err := mutationdecode.NewCompleteRow(fixture.schema.Registry, policyir.ModelID(fact.model), fact.cells)
		if err != nil {
			t.Fatal(err)
		}
		identity := []policyir.FieldID{policyir.FieldID(fixture.identityField(fact.model))}
		var requirement mutationir.FactRequirement
		var after *mutationdecode.Row
		switch fact.action {
		case mutationir.FactDeleted:
			registered, _ := fixture.schema.Registry.Model(fact.model)
			_, declared, _ := registered.EventSchema()
			stored := make([]policyir.FieldID, len(declared))
			for index, field := range declared {
				stored[index] = policyir.FieldID(field)
			}
			requirement, err = mutationir.NewDeleteFactRequirement(identity, mutationir.DeleteSnapshotStoredScalars, stored)
		default:
			requirement, err = mutationir.NewFactRequirement(mutationir.FactUpdated, identity, identity, nil)
			after = &row
		}
		if err != nil {
			t.Fatal(err)
		}
		digest := fixture.digests[fact.model]
		requirement, err = requirement.WithEventSchema([32]byte(digest))
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := mutationfact.NewV2(fixture.schema.Registry, golem.SchemaDigest(digest), mutationfact.EventID(p7OracleID(fact.eventID)), requirement, mutationfact.CausationID(golem.CausationID(p7OracleID(causation))), fact.ordinal, &row, after)
		if err != nil {
			t.Fatal(err)
		}
		if fact.unverifiable {
			if envelope, err = envelope.WithUnverifiableDeleteSnapshot(); err != nil {
				t.Fatal(err)
			}
		}
		outbox, err := envelope.OutboxRow(time.Unix(int64(fact.eventID), 0))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, outbox)
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].TransactionOrdinal < rows[right].TransactionOrdinal })
	return rows
}

func (fixture deleteEvalFixture) outboxTable(t *testing.T) string {
	t.Helper()
	namespace, ok := fixture.app.registry.PhysicalSystemNamespace(fixture.app.eventProvider)
	if !ok {
		t.Fatal("system namespace is unavailable")
	}
	return `"` + string(namespace) + `"."_golem_outbox"`
}

func (fixture deleteEvalFixture) storeRows(t *testing.T, rows []mutationfact.OutboxRow) {
	t.Helper()
	namespace, _ := fixture.app.registry.PhysicalSystemNamespace(fixture.app.eventProvider)
	statements, err := mutationfact.RenderInsertsAt(fixture.app.provider, string(namespace), rows, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := fixture.app.database.ExecContext(context.Background(), statement.SQL(), statement.Args()...); err != nil {
			t.Fatal(err)
		}
	}
}

func (fixture deleteEvalFixture) publishRows(t *testing.T, causation uint16, rows []mutationfact.OutboxRow) {
	t.Helper()
	notices := make([]events.Notice, 0, len(rows))
	for _, row := range rows {
		envelope, err := eventcodec.EncodeStoredRow(row, fixture.app.eventSchemas, eventcodec.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
		if err != nil {
			t.Fatal(err)
		}
		notices = append(notices, notice)
	}
	batch, err := eventvalue.NewEventBatch(golem.CausationID(p7OracleID(causation)), notices)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func (fixture deleteEvalFixture) commit(t *testing.T, causation uint16, facts ...deleteEvalFact) {
	t.Helper()
	rows := fixture.factRows(t, causation, facts...)
	fixture.storeRows(t, rows)
	fixture.publishRows(t, causation, rows)
}

func (fixture deleteEvalFixture) cascadeUser(t *testing.T, user, post byte) {
	t.Helper()
	fixture.cascadePost(t, post)
	fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.User)+` WHERE "id" = ?`, mutationResultUUIDText(user))
}

func expectNoUnverifiable(t *testing.T, fixture deleteEvalFixture) {
	t.Helper()
	if count := fixture.suppressions(events.SuppressionDeletionUnverifiable); count != 0 {
		t.Fatalf("delete resolved from a same-causation snapshot was reported unverifiable %d times", count)
	}
}

func deleteEvalAuthorPolicy() deleteEvalPolicies {
	return deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthorID.Eq(deleteEvalUUID(deleteEvalAlice))), true
		},
	}
}

func deleteEvalPublishedPolicy() deleteEvalPolicies {
	return deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}
}

func TestDeleteEventCascadedParentSnapshotDecides(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.commit(t, 100,
			fixture.postFact(t, 101, 1, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 102, 2, deleteEvalOnPub, deleteEvalPublished),
		)
		expectDeleteDelivered(t, stream, 102)
		fixture.cascadePost(t, deleteEvalDraft)
		fixture.commit(t, 110,
			fixture.commentFact(t, 112, 1, deleteEvalOnDraft, deleteEvalDraft),
			fixture.postFact(t, 111, 2, deleteEvalDraft, deleteEvalBob, "draft"),
		)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		expectNoUnverifiable(t, fixture)
	})
}

func TestDeleteEventNegatedPolicyOverCascadedParentUsesSnapshot(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			owner := deleteEvalUUID(deleteEvalAlice)
			if actor.Name == "bob" {
				owner = deleteEvalUUID(deleteEvalBob)
			}
			return golem.Not(fields.commentPost.Is(fields.postAuthorID.Eq(owner))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.commit(t, 120,
			fixture.postFact(t, 121, 1, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 122, 2, deleteEvalOnPub, deleteEvalPublished),
		)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		fixture.cascadePost(t, deleteEvalDraft)
		fixture.commit(t, 125,
			fixture.postFact(t, 126, 1, deleteEvalDraft, deleteEvalBob, "draft"),
			fixture.commentFact(t, 127, 2, deleteEvalOnDraft, deleteEvalDraft),
		)
		expectDeleteDelivered(t, stream, 127)
		expectNoUnverifiable(t, fixture)
	})
}

func TestDeleteEventCascadedParentFactFromAnotherCausationResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.commit(t, 130, fixture.postFact(t, 131, 1, deleteEvalPublished, deleteEvalAlice, "published"))
		fixture.commit(t, 133, fixture.commentFact(t, 132, 1, deleteEvalOnPub, deleteEvalPublished))
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
	})
}

func TestDeleteEventCascadedParentFactRetainedAwayResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		rows := fixture.factRows(t, 134,
			fixture.postFact(t, 135, 1, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 136, 2, deleteEvalOnPub, deleteEvalPublished),
		)
		fixture.storeRows(t, rows)
		fixture.exec(t, `DELETE FROM `+fixture.outboxTable(t)+` WHERE "event_id" = ?`, rows[0].EventID)
		fixture.publishRows(t, 134, rows)
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
	})
}

func TestDeleteEventCascadedParentUnverifiableSnapshotResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		parent := fixture.postFact(t, 141, 1, deleteEvalPublished, deleteEvalAlice, "published")
		parent.unverifiable = true
		fixture.commit(t, 140, parent, fixture.commentFact(t, 142, 2, deleteEvalOnPub, deleteEvalPublished))
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
	})
}

func TestDeleteEventCascadedParentChangedAfterChildResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		update := fixture.postFact(t, 151, 2, deleteEvalPublished, deleteEvalAlice, "published")
		update.action = mutationir.FactUpdated
		fixture.commit(t, 150,
			fixture.commentFact(t, 153, 1, deleteEvalOnPub, deleteEvalPublished),
			update,
			fixture.postFact(t, 152, 3, deleteEvalPublished, deleteEvalAlice, "published"),
		)
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventCascadedParentUpdatedBeforeChildResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		update := fixture.postFact(t, 156, 1, deleteEvalPublished, deleteEvalAlice, "published")
		update.action = mutationir.FactUpdated
		fixture.commit(t, 155,
			update,
			fixture.commentFact(t, 158, 2, deleteEvalOnPub, deleteEvalPublished),
			fixture.postFact(t, 157, 3, deleteEvalPublished, deleteEvalAlice, "published"),
		)
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventCascadedParentDeletedTwiceResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalAuthorPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.commit(t, 160,
			fixture.postFact(t, 161, 1, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 163, 2, deleteEvalOnPub, deleteEvalPublished),
			fixture.postFact(t, 162, 3, deleteEvalPublished, deleteEvalAlice, "published"),
		)
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventMultiLevelCascadeUsesEachSnapshot(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthor.Is(fields.userID.Eq(deleteEvalUUID(deleteEvalAlice)))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		fixture.cascadeUser(t, deleteEvalAlice, deleteEvalPublished)
		fixture.commit(t, 170,
			fixture.userFact(t, 171, 1, deleteEvalAlice, "alice"),
			fixture.postFact(t, 172, 2, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 173, 3, deleteEvalOnPub, deleteEvalPublished),
		)
		expectDeleteDelivered(t, stream, 173)
		fixture.cascadeUser(t, deleteEvalBob, deleteEvalDraft)
		fixture.commit(t, 175,
			fixture.userFact(t, 176, 1, deleteEvalBob, "bob"),
			fixture.postFact(t, 177, 2, deleteEvalDraft, deleteEvalBob, "draft"),
			fixture.commentFact(t, 178, 3, deleteEvalOnDraft, deleteEvalDraft),
		)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		expectNoUnverifiable(t, fixture)
	})
}

func TestDeleteEventNegatedMultiLevelCascadeUsesEachSnapshot(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return golem.Not(fields.commentPost.Is(fields.postAuthor.Is(fields.userID.Eq(deleteEvalUUID(deleteEvalAlice))))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		fixture.cascadeUser(t, deleteEvalAlice, deleteEvalPublished)
		fixture.commit(t, 180,
			fixture.userFact(t, 181, 1, deleteEvalAlice, "alice"),
			fixture.postFact(t, 182, 2, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 183, 3, deleteEvalOnPub, deleteEvalPublished),
		)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		fixture.cascadeUser(t, deleteEvalBob, deleteEvalDraft)
		fixture.commit(t, 185,
			fixture.userFact(t, 186, 1, deleteEvalBob, "bob"),
			fixture.postFact(t, 187, 2, deleteEvalDraft, deleteEvalBob, "draft"),
			fixture.commentFact(t, 188, 3, deleteEvalOnDraft, deleteEvalDraft),
		)
		expectDeleteDelivered(t, stream, 188)
		expectNoUnverifiable(t, fixture)
	})
}

func TestDeleteEventNegatedNestedHopWithoutNestedFactResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return golem.Not(fields.commentPost.Is(fields.postAuthor.Is(fields.userID.Eq(deleteEvalUUID(deleteEvalAlice))))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		fixture.cascadeUser(t, deleteEvalAlice, deleteEvalPublished)
		fixture.commit(t, 190,
			fixture.postFact(t, 192, 1, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 193, 2, deleteEvalOnPub, deleteEvalPublished),
		)
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventNegatedNestedHopOverPresentTargetUsesSnapshot(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return golem.Not(fields.commentPost.Is(fields.postAuthor.Is(fields.userName.Eq("alice")))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.commit(t, 195,
			fixture.postFact(t, 196, 1, deleteEvalPublished, deleteEvalAlice, "published"),
			fixture.commentFact(t, 197, 2, deleteEvalOnPub, deleteEvalPublished),
		)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		fixture.cascadePost(t, deleteEvalDraft)
		fixture.commit(t, 198,
			fixture.postFact(t, 199, 1, deleteEvalDraft, deleteEvalBob, "draft"),
			fixture.commentFact(t, 200, 2, deleteEvalOnDraft, deleteEvalDraft),
		)
		expectDeleteDelivered(t, stream, 200)
		expectNoUnverifiable(t, fixture)
	})
}

func (fixture deleteEvalFixture) retitlePost(t *testing.T, post byte, title string) {
	t.Helper()
	fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+` SET "title" = ? WHERE "id" = ?`, title, mutationResultUUIDText(post))
}

func (fixture deleteEvalFixture) postUpdate(t *testing.T, eventID uint16, ordinal uint32, post, author byte, title string) deleteEvalFact {
	t.Helper()
	fact := fixture.postFact(t, eventID, ordinal, post, author, title)
	fact.action = mutationir.FactUpdated
	return fact
}

func TestDeleteEventNegatedPolicyOverParentUpdatedInSameTransactionResyncs(t *testing.T) {
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
		fixture.commit(t, 210,
			fixture.postUpdate(t, 211, 1, deleteEvalPublished, deleteEvalAlice, "draft"),
			fixture.commentFact(t, 212, 2, deleteEvalOnPub, deleteEvalPublished),
		)
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
	})
}

func TestDeleteEventPolicyOverParentUpdatedInSameTransactionResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		if !fixture.liveCommentVisible(t, "alice", deleteEvalOnPub) {
			t.Fatal("live read hides a comment under the published post")
		}
		fixture.retitlePost(t, deleteEvalPublished, "draft")
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.commit(t, 215,
			fixture.postUpdate(t, 216, 1, deleteEvalPublished, deleteEvalAlice, "draft"),
			fixture.commentFact(t, 217, 2, deleteEvalOnPub, deleteEvalPublished),
		)
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
		if fixture.suppressions(events.SuppressionUnauthorized) != 0 {
			t.Fatal("a delete over a parent changed in the same transaction was suppressed as unauthorized")
		}
	})
}

func TestDeleteEventParentChangedInAnotherTransactionUsesDeliveryTimeState(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.retitlePost(t, deleteEvalPublished, "draft")
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.storeRows(t, fixture.factRows(t, 220, fixture.postUpdate(t, 221, 1, deleteEvalPublished, deleteEvalAlice, "draft")))
		fixture.commit(t, 222, fixture.commentFact(t, 223, 1, deleteEvalOnPub, deleteEvalPublished))
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
	})
}

func TestDeleteEventNestedParentUpdatedInSameTransactionResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthor.Is(fields.userName.Eq("alice"))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.User)+` SET "name" = ? WHERE "id" = ?`, "carol", mutationResultUUIDText(deleteEvalAlice))
		fixture.removeComment(t, deleteEvalOnPub)
		update := fixture.userFact(t, 226, 1, deleteEvalAlice, "carol")
		update.action = mutationir.FactUpdated
		fixture.commit(t, 225, update, fixture.commentFact(t, 227, 2, deleteEvalOnPub, deleteEvalPublished))
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventHopTargetPolicyUnavailableSuppresses(t *testing.T) {
	broken := &atomic.Bool{}
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		postUnavailable: broken,
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		broken.Store(false)
		stream := fixture.subscribeComments(t, "alice")
		broken.Store(true)
		fixture.app.InvalidatePolicies()
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.commit(t, 230, fixture.commentFact(t, 231, 1, deleteEvalOnPub, deleteEvalPublished))
		fixture.awaitSuppression(t, events.SuppressionPolicyUnavailable)
		expectDeleteSilent(t, stream)
		if fixture.suppressions(events.SuppressionDeletionUnverifiable) != 0 {
			t.Fatal("an unavailable hop policy was reported unverifiable")
		}
	})
}

func TestDeleteEventProbeFailureRevalidates(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPublishedPolicy(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.exec(t, `DROP TABLE `+nestedAcceptanceTable(fixture.app, fixture.schema.Post))
		fixture.commit(t, 235, fixture.commentFact(t, 236, 1, deleteEvalOnPub, deleteEvalPublished))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		value, err := stream.Recv(ctx)
		if err == nil {
			t.Fatalf("stream delivered %x after a failed probe", value.validated.Metadata().EventID())
		}
		if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionRevalidation {
			t.Fatalf("stream ended with %v, want %s", err, events.CodeSubscriptionRevalidation)
		}
	})
}

func TestDeleteEventNegationInducedByReadExpansion(t *testing.T) {
	quantifiers := map[string]func(deleteEvalFields) golem.Predicate[deleteEvalUser]{
		"some": func(fields deleteEvalFields) golem.Predicate[deleteEvalUser] {
			return fields.userPosts.Some(fields.postTitle.Eq("published"))
		},
		"every": func(fields deleteEvalFields) golem.Predicate[deleteEvalUser] {
			return fields.userPosts.Every(fields.postTitle.Eq("published"))
		},
		"none": func(fields deleteEvalFields) golem.Predicate[deleteEvalUser] {
			return fields.userPosts.None(fields.postTitle.Eq("draft"))
		},
	}
	for name, quantifier := range quantifiers {
		name, quantifier := name, quantifier
		t.Run(name, func(t *testing.T) {
			runDeleteEvalProfiles(t, deleteEvalPolicies{
				user: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalUser], bool) {
					return quantifier(fields), true
				},
				comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
					return golem.Not(fields.commentPost.Is(golem.Not(fields.postAuthor.Is(fields.userName.Eq("zed"))))), true
				},
			}, func(t *testing.T, fixture deleteEvalFixture) {
				stream := fixture.subscribeComments(t, "alice")
				visible := fixture.liveCommentVisible(t, "alice", deleteEvalOnPub)
				fixture.removeComment(t, deleteEvalOnPub)
				fixture.commit(t, 240, fixture.commentFact(t, 241, 1, deleteEvalOnPub, deleteEvalPublished))
				switch {
				case name != "some":
					expectDeleteResync(t, stream)
				case visible:
					expectDeleteDelivered(t, stream, 241)
				default:
					fixture.awaitSuppression(t, events.SuppressionUnauthorized)
					expectDeleteSilent(t, stream)
				}
			})
		})
	}
}

func TestDeleteEventRelationalFilterOverCascadedParent(t *testing.T) {
	filter := func(fixture deleteEvalFixture) golem.EventOption[deleteEvalComment] {
		return golem.EventWhere[deleteEvalComment](fixture.fields.commentPost.Is(fixture.fields.postAuthorID.Eq(deleteEvalUUID(deleteEvalAlice))))
	}
	t.Run("without-parent-fact-over-delivers", func(t *testing.T) {
		runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
			stream := fixture.subscribeComments(t, "alice", filter(fixture))
			fixture.cascadePost(t, deleteEvalDraft)
			fixture.commit(t, 250, fixture.commentFact(t, 251, 1, deleteEvalOnDraft, deleteEvalDraft))
			expectDeleteDelivered(t, stream, 251)
			deadline := time.Now().Add(2 * time.Second)
			for {
				found := false
				for _, value := range fixture.observed.kind(events.ObservationDelivery) {
					if value.SuppressionReason() == events.SuppressionDeleteOverDelivered && value.Action() == golem.EventDeleted {
						found = true
					}
				}
				if found {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("over-delivered delete was not reported")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	})
	t.Run("with-parent-fact-filters-exactly", func(t *testing.T) {
		runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
			stream := fixture.subscribeComments(t, "alice", filter(fixture))
			fixture.cascadePost(t, deleteEvalDraft)
			fixture.commit(t, 255,
				fixture.postFact(t, 256, 1, deleteEvalDraft, deleteEvalBob, "draft"),
				fixture.commentFact(t, 257, 2, deleteEvalOnDraft, deleteEvalDraft),
			)
			fixture.awaitSuppression(t, events.SuppressionDeleteFiltered)
			fixture.cascadePost(t, deleteEvalPublished)
			fixture.commit(t, 258,
				fixture.postFact(t, 259, 1, deleteEvalPublished, deleteEvalAlice, "published"),
				fixture.commentFact(t, 260, 2, deleteEvalOnPub, deleteEvalPublished),
			)
			expectDeleteDelivered(t, stream, 260)
		})
	})
}

func TestDeleteEventCorrelationNullAndComposite(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			if actor.Name == "isnot" {
				return fields.commentPost.IsNot(fields.postTitle.Eq("published")), true
			}
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		ctx := context.Background()
		comment := policyir.ModelID(fixture.schema.Comment)
		constraint := func(actor string) (*Caller[deleteEvalPrincipal, deleteEvalActor], policyir.Condition) {
			caller, err := fixture.app.ForPrincipal(ctx, deleteEvalPrincipal{Name: actor})
			if err != nil {
				t.Fatal(err)
			}
			row, _, err := readplan.CallerReach(caller.policies, comment, nil, fixture.app.readLimits.plan.MaxRelationDepth)
			if err != nil {
				t.Fatal(err)
			}
			return caller, row
		}
		node := func() *deleteEvaluator {
			rows := fixture.factRows(t, 270, fixture.commentFact(t, 271, 1, deleteEvalOnPub, deleteEvalPublished))
			fact, err := mutationfact.ValidateStoredRow(rows[0], fixture.app.eventSchemas)
			if err != nil {
				t.Fatal(err)
			}
			identity, _ := fact.BeforeIdentity()
			evaluator, err := fixture.app.newDeleteEvaluator(comment, fact, identity)
			if err != nil {
				t.Fatal(err)
			}
			return evaluator
		}
		postField := policyir.FieldID(fixture.schema.CommentPostID)
		bodyField := policyir.FieldID(fixture.schema.CommentBody)
		for actor, want := range map[string]deleteTruth{"is": deleteFalse, "isnot": deleteTrue} {
			_, condition := constraint(actor)
			evaluator := node()
			evaluator.cells[postField] = mutationdecode.Null(postField)
			if got := evaluator.truth(condition); got != want {
				t.Fatalf("%s over a NULL foreign key evaluated %d, want %d", actor, got, want)
			}
		}
		for _, actor := range []string{"is", "isnot"} {
			caller, condition := constraint(actor)
			evaluator := node()
			evaluator.cells[bodyField] = mutationdecode.Null(bodyField)
			evaluator.shared.forward = func(policyir.ModelID, policyir.FieldID, policyir.RelationID) (deleteForwardHop, bool) {
				return deleteForwardHop{target: fixture.schema.Post, parentField: []golem.FieldID{fixture.schema.CommentPostID, fixture.schema.CommentBody}, childField: []golem.FieldID{fixture.schema.PostID, fixture.schema.PostTitle}}, true
			}
			if err := fixture.app.resolveDeleteEvidence(ctx, caller, evaluator, []policyir.Condition{condition}); err != nil {
				t.Fatal(err)
			}
			if got := evaluator.truth(condition); got != deleteUnknown {
				t.Fatalf("%s over a partly NULL composite foreign key evaluated %d, want unknown", actor, got)
			}
		}
	})
}

func TestDeleteFactLookupUsesTheCausationIndex(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		if fixture.app.provider != policyir.ProviderSQLite {
			return
		}
		query, args, err := fixture.app.deleteFactCheckStatement(golem.UUID(p7OracleID(1)).String(), []*deleteFactCheck{
			{target: policyir.ModelID(fixture.schema.Post), identity: []byte{1, 2, 3}},
			{target: policyir.ModelID(fixture.schema.User)},
		})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := fixture.app.database.QueryxContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		outbox := 0
		for rows.Next() {
			var id, parent, unused int64
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(detail, "_golem_outbox") {
				continue
			}
			outbox++
			if !strings.Contains(detail, "USING INDEX") && !strings.Contains(detail, "USING COVERING INDEX") {
				t.Fatalf("delete fact lookup does not use an index: %s", detail)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if outbox != 2 {
			t.Fatalf("delete fact lookup planned %d outbox reads, want 2", outbox)
		}
	})
}
