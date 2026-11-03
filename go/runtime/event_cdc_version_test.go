package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

func (fixture deleteEvalFixture) postVersion(t *testing.T, post byte) int64 {
	t.Helper()
	var version int64
	query := fixture.app.database.Rebind(`SELECT "golem_version" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &version, query, mutationResultUUIDText(post)); err != nil {
		t.Fatal(err)
	}
	return version
}

func (fixture deleteEvalFixture) recordedFactVersions(t *testing.T, causation string) []int64 {
	t.Helper()
	var stored []deleteSnapshotStoredFact
	query := fixture.app.database.Rebind(`SELECT "action","before_identity","after_identity","metadata","delete_snapshot" FROM ` + fixture.outboxTable(t) + ` WHERE "causation_id" = ? ORDER BY "transaction_ordinal"`)
	if err := fixture.app.database.SelectContext(context.Background(), &stored, query, causation); err != nil {
		t.Fatal(err)
	}
	versions := make([]int64, len(stored))
	for index, row := range stored {
		envelope, err := mutationfact.DecodeOutboxWithResolver(row.Metadata, row.DeleteSnapshot, fixture.app.eventSchemas)
		if err != nil {
			t.Fatal(err)
		}
		versions[index] = envelope.RowVersion()
	}
	return versions
}

func TestCDCStampsVersion(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		if before := fixture.postVersion(t, deleteEvalPublished); before != 0 {
			t.Fatalf("an externally written row starts at version %d, want 0", before)
		}
		first := fixture.emitCDC(t, "cdc-stamp-1", fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "edited"))
		firstVersion := fixture.postVersion(t, deleteEvalPublished)
		if firstVersion <= 0 {
			t.Fatalf("the recorder left the row at version %d", firstVersion)
		}
		if versions := fixture.recordedFactVersions(t, first); len(versions) != 1 || versions[0] != firstVersion {
			t.Fatalf("recorded fact versions %v, row version %d", versions, firstVersion)
		}

		second := fixture.emitCDC(t, "cdc-stamp-2", fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "edited", "edited-again"))
		secondVersion := fixture.postVersion(t, deleteEvalPublished)
		if secondVersion <= firstVersion {
			t.Fatalf("the second external update was stamped %d after %d", secondVersion, firstVersion)
		}
		if versions := fixture.recordedFactVersions(t, second); len(versions) != 1 || versions[0] != secondVersion {
			t.Fatalf("recorded fact versions %v, row version %d", versions, secondVersion)
		}

		fixture.emitCDC(t, "cdc-stamp-2", fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "edited", "edited-again"))
		if replayed := fixture.postVersion(t, deleteEvalPublished); replayed != secondVersion {
			t.Fatalf("a replayed batch re-stamped the row: %d then %d", secondVersion, replayed)
		}
		if versions := fixture.recordedFactVersions(t, second); len(versions) != 1 || versions[0] != secondVersion {
			t.Fatalf("a replay changed the recorded versions to %v", versions)
		}
	})
}

func TestCDCStampCommitsOnlyWithItsFacts(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		recorder := fixture.cdcRecorder(t)
		change := fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "edited")
		change.Ordinal = 1
		causation := golem.CausationID{15: 77}
		failure := errors.New("the facts could not be built")
		err := recorder.RecordCDC(context.Background(), causation, []events.CDCChangeInput{change}, func(versions []int64) ([]mutationfact.OutboxRow, error) {
			if len(versions) != 1 || versions[0] <= 0 {
				t.Errorf("the recorder offered versions %v", versions)
			}
			return nil, failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("record error = %v, want the build failure", err)
		}
		if version := fixture.postVersion(t, deleteEvalPublished); version != 0 {
			t.Fatalf("a stamp survived the failed record: the row is at version %d", version)
		}
	})
}

func (fixture deleteEvalFixture) cdcCommentDeleteWith(t *testing.T, cells ...golem.RuntimeReadCell) events.CDCChangeInput {
	t.Helper()
	row, err := golem.RuntimeCDCModelRow(fixture.schema.Comment, cells...)
	if err != nil {
		t.Fatal(err)
	}
	return events.CDCChangeInput{Model: fixture.schema.Comment, Action: golem.EventDeleted, Before: &row}
}

func TestCDCDeleteOfAnUnstampedRowIsPublishedAtVersionZero(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		change := fixture.cdcCommentDeleteWith(t,
			golem.RuntimePresentReadCell(fixture.schema.CommentID, deleteEvalUUID(deleteEvalOnPub), nil),
			golem.RuntimePresentReadCell(fixture.schema.CommentPostID, deleteEvalUUID(deleteEvalPublished), nil),
			golem.RuntimePresentReadCell(fixture.schema.CommentBody, "hello", nil),
			golem.RuntimePresentReadCell(fixture.schema.CommentRowVersion, int64(0), nil),
		)
		fixture.removeComment(t, deleteEvalOnPub)
		causation := fixture.emitCDC(t, "cdc-unstamped-delete", change)
		if versions := fixture.recordedFactVersions(t, causation); len(versions) != 1 || versions[0] != 0 {
			t.Fatalf("recorded fact versions %v, want [0]", versions)
		}
		deleted := receiveP7Event(t, stream)
		if deleted.validated.Metadata().Action() != golem.EventDeleted || deleted.validated.Metadata().Version() != 0 {
			t.Fatalf("deleted event version = %d, want 0", deleted.validated.Metadata().Version())
		}
	})
}

func TestCDCDeleteWithoutTheVersionColumnIsRefused(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		change := fixture.cdcCommentDeleteWith(t,
			golem.RuntimePresentReadCell(fixture.schema.CommentID, deleteEvalUUID(deleteEvalOnPub), nil),
			golem.RuntimePresentReadCell(fixture.schema.CommentPostID, deleteEvalUUID(deleteEvalPublished), nil),
			golem.RuntimePresentReadCell(fixture.schema.CommentBody, "hello", nil),
		)
		recorder := &deleteEvalCDCRecorder{inner: fixture.cdcRecorder(t)}
		err := fixture.cdcEmitter(t, fixture.transport, recorder).Emit(context.Background(), deleteEvalCDCBatch("cdc-no-version-column", change))
		if code, ok := events.CodeOf(err); !ok || code != events.CodeCDCInvalid {
			t.Fatalf("emit error = %v, want %s", err, events.CodeCDCInvalid)
		}
		if facts := fixture.recordedFacts(t, recorder.last(t)); facts != 0 {
			t.Fatalf("a delete without the version column recorded %d facts", facts)
		}
	})
}

func (fixture deleteEvalFixture) commentVersion(t *testing.T, comment byte) int64 {
	t.Helper()
	var version int64
	query := fixture.app.database.Rebind(`SELECT "golem_version" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Comment) + ` WHERE "id" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &version, query, mutationResultUUIDText(comment)); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestEventsCarryTheRowVersion(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		before := fixture.cdcCommentRow(t, deleteEvalOnPub, deleteEvalPublished, "hello")
		after := fixture.cdcCommentRow(t, deleteEvalOnPub, deleteEvalPublished, "edited")
		fixture.emitCDC(t, "cdc-event-version-update", events.CDCChangeInput{Model: fixture.schema.Comment, Action: golem.EventUpdated, Before: &before, After: &after})
		updated := receiveP7Event(t, stream)
		stamped := fixture.commentVersion(t, deleteEvalOnPub)
		if updated.validated.Metadata().Action() != golem.EventUpdated || stamped <= 0 || updated.validated.Metadata().Version() != stamped {
			t.Fatalf("updated event version = %d, row version = %d", updated.validated.Metadata().Version(), stamped)
		}
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.emitCDCDelete(t, "cdc-event-version-delete", deleteEvalOnPub, deleteEvalPublished)
		deleted := receiveP7Event(t, stream)
		if deleted.validated.Metadata().Action() != golem.EventDeleted || deleted.validated.Metadata().Version() != 1 {
			t.Fatalf("deleted event version = %d, want the fact's version 1", deleted.validated.Metadata().Version())
		}
	})
}

type stampLogAdapter struct {
	p7AuthorizationCDCAdapter
	recorded func() []int64
	fixture  deleteEvalFixture
	t        *testing.T
}

func (adapter stampLogAdapter) CorrelatesGolemTransaction(_ context.Context, input events.CDCCorrelationInput) (bool, error) {
	var version int64
	if _, err := fmt.Sscanf(input.SourceTransactionID(), "stamp:%d", &version); err != nil {
		return false, nil
	}
	for _, recorded := range adapter.recorded() {
		if recorded == version {
			return true, nil
		}
	}
	return false, nil
}

func TestCDCStampTransactionIsCorrelatedAndProducesNoSecondBatch(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		first := fixture.emitCDC(t, "cdc-loop-1", fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "published", "edited"))
		stamped := fixture.postVersion(t, deleteEvalPublished)
		encoder, err := newCDCEventEncoder(fixture.schema.Registry, fixture.app.eventSchemas, 0)
		if err != nil {
			t.Fatal(err)
		}
		adapter := stampLogAdapter{recorded: func() []int64 { return fixture.recordedFactVersions(t, first) }, fixture: fixture, t: t}
		emitter, err := eventcdc.NewEmitter(eventcdc.Config{Adapter: adapter, Transport: fixture.transport, Encoder: encoder, Recorder: fixture.cdcRecorder(t)})
		if err != nil {
			t.Fatal(err)
		}
		facts := fixture.count(t, `SELECT COUNT(*) FROM `+fixture.outboxTable(t))
		echo := fixture.cdcPostUpdate(t, deleteEvalPublished, deleteEvalAlice, "edited", "edited")
		if err := emitter.Emit(context.Background(), deleteEvalCDCBatch(fmt.Sprintf("stamp:%d", stamped), echo)); err != nil {
			t.Fatal(err)
		}
		if got := fixture.count(t, `SELECT COUNT(*) FROM `+fixture.outboxTable(t)); got != facts {
			t.Fatalf("the stamp's own transaction produced a second batch: %d facts became %d", facts, got)
		}
		if version := fixture.postVersion(t, deleteEvalPublished); version != stamped {
			t.Fatalf("the stamp's own transaction was stamped again: %d then %d", stamped, version)
		}
	})
}

func TestEventCarriesItsOwnFactsVersionWhenALaterWriteLandsFirst(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		version := golem.GeneratedEqualField[deleteEvalComment, int64](fixture.schema.CommentRowVersion)
		stream := fixture.subscribeComments(t, "alice", golem.EventSelect[deleteEvalComment](version))
		fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` SET "golem_version" = 50 WHERE "id" = ?`, mutationResultUUIDText(deleteEvalOnPub))
		fixture.publishCommentEvent(t, golem.EventUpdated, 3100, deleteEvalUUID(deleteEvalOnPub), deleteEvalUUID(deleteEvalPublished), "hello")
		event := receiveP7Event(t, stream)
		if got := event.validated.Metadata().Version(); got != 7 {
			t.Fatalf("event version = %d, want its own fact's version 7 (the row is already at 50)", got)
		}
	})
}
