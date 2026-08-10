package runtime

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestP7ProductionCDCEncoderUsesCanonicalV2FactAndEventPath(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	encoder, history := testCDCEventEncoder(t, fixture)
	after := testCDCPostRow(t, fixture, false, "created")
	input := testCDCEncodeInput(fixture, golem.EventCreated, nil, &after)
	notice, err := encoder.EncodeCDC(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !notice.Valid() || notice.EventID() != input.EventID || notice.CausationID() != input.CausationID || notice.TransactionOrdinal() != input.Ordinal || notice.ModelID() != input.Model || notice.Action() != input.Action {
		t.Fatalf("notice metadata=%#v", notice)
	}
	envelope, err := eventcodec.Decode(notice.Encoded(), history, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Fact().FormatVersion() != mutationfact.FormatVersionV2 || envelope.Fact().CodecIdentity() != mutationfact.CodecIdentityV2 || envelope.RecordedAt() != input.RecordedAt {
		t.Fatalf("canonical CDC envelope fact=%d/%q recorded=%v", envelope.Fact().FormatVersion(), envelope.Fact().CodecIdentity(), envelope.RecordedAt())
	}
	if !bytes.Equal(envelope.Encoded(), notice.Encoded()) {
		t.Fatal("CDC notice did not round-trip through golem.event.v1 canonically")
	}
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, _ := model.EventSchema()
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil || envelope.ResolvedEventSchemaDigest() != golem.EventSchemaDigest(digest) {
		t.Fatalf("resolved event schema=%x error=%v", envelope.ResolvedEventSchemaDigest(), err)
	}
}

func TestP7ProductionCDCReplayKeepsExactEventIDAndCanonicalBytes(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	encoder, _ := testCDCEventEncoder(t, fixture)
	after := testCDCPostRow(t, fixture, false, "replayed")
	input := testCDCEncodeInput(fixture, golem.EventCreated, nil, &after)
	first, err := encoder.EncodeCDC(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encoder.EncodeCDC(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID() != second.EventID() || !bytes.Equal(first.Encoded(), second.Encoded()) {
		t.Fatal("stable CDC source transaction replay changed event identity or canonical bytes")
	}
}

func TestP7ProductionCDCEncoderRejectsNoncanonicalRecordedTime(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	encoder, _ := testCDCEventEncoder(t, fixture)
	after := testCDCPostRow(t, fixture, false, "recorded")
	base := testCDCEncodeInput(fixture, golem.EventCreated, nil, &after)
	for name, recordedAt := range map[string]time.Time{
		"zero":           {},
		"non UTC":        base.RecordedAt.In(time.FixedZone("offset", 3600)),
		"submicrosecond": base.RecordedAt.Add(time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.RecordedAt = recordedAt
			if _, err := encoder.EncodeCDC(context.Background(), input); err == nil {
				t.Fatal("noncanonical CDC recorded time encoded")
			}
		})
	}
}

func TestP7ProductionCDCEncoderCapturesCompilerOwnedDeleteSnapshot(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	encoder, history := testCDCEventEncoder(t, fixture)
	before := testCDCPostRow(t, fixture, false, "deleted")
	input := testCDCEncodeInput(fixture, golem.EventDeleted, &before, nil)
	notice, err := encoder.EncodeCDC(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := eventcodec.Decode(notice.Encoded(), history, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, present := envelope.Fact().PrivateDeleteSnapshot()
	if !present {
		t.Fatal("CDC delete omitted its private stored-scalar snapshot")
	}
	model, _ := fixture.Registry.Model(fixture.Post)
	_, inventory, _ := model.EventSchema()
	if len(snapshot.Cells()) != len(inventory) {
		t.Fatalf("delete snapshot cells=%d compiler inventory=%d", len(snapshot.Cells()), len(inventory))
	}
	for index, field := range inventory {
		if snapshot.Cells()[index].FieldID() != policyir.FieldID(field) {
			t.Fatalf("snapshot field %d=%x want=%x", index, snapshot.Cells()[index].FieldID(), field)
		}
	}
}

func TestP7ProductionCDCEncoderRejectsProjectedIncompleteForeignRelationAndInvalidImages(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	encoder, _ := testCDCEventEncoder(t, fixture)
	completeCells := testCDCPostCells(fixture, "valid")
	ordinary, err := golem.RuntimeModelReadRow(fixture.Post, completeCells...)
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := golem.RuntimeCDCModelRow(fixture.Post, completeCells[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	foreignCells := append(append([]golem.RuntimeReadCell(nil), completeCells...), golem.RuntimePresentReadCell(fixture.UserName, "foreign", nil))
	foreign, err := golem.RuntimeCDCModelRow(fixture.Post, foreignCells...)
	if err != nil {
		t.Fatal(err)
	}
	relationCells := append(append([]golem.RuntimeReadCell(nil), completeCells...), golem.RuntimeToOneReadCell(fixture.PostAuthor, ordinary))
	relation, err := golem.RuntimeCDCModelRow(fixture.Post, relationCells...)
	if err != nil {
		t.Fatal(err)
	}
	wrongCells := append([]golem.RuntimeReadCell(nil), completeCells...)
	wrongCells[2] = golem.RuntimePresentReadCell(fixture.PostTitle, int64(7), nil)
	wrong, err := golem.RuntimeCDCModelRow(fixture.Post, wrongCells...)
	if err != nil {
		t.Fatal(err)
	}

	for name, row := range map[string]golem.RuntimeModelRow{
		"ordinary or masked projection": ordinary,
		"incomplete":                    incomplete,
		"foreign":                       foreign,
		"relation":                      relation,
		"wrong scalar type":             wrong,
	} {
		t.Run(name, func(t *testing.T) {
			input := testCDCEncodeInput(fixture, golem.EventCreated, nil, &row)
			if _, err := encoder.EncodeCDC(context.Background(), input); err == nil {
				t.Fatal("invalid CDC image encoded")
			}
		})
	}
}

func TestP7ProductionCDCEncoderDistinguishesTrustedStoredNullFromMaskedNull(t *testing.T) {
	fixture := schematest.NewSubscribedIndexedOptionalSource(t)
	encoder, _ := testCDCEventEncoder(t, fixture)
	cells := []golem.RuntimeReadCell{
		golem.RuntimePresentReadCell(fixture.PostID, golem.UUID{15: 1}, nil),
		golem.RuntimeNullReadCell(fixture.AuthorID),
		golem.RuntimePresentReadCell(fixture.PostTitle, "nullable-author", nil),
	}
	stored, err := golem.RuntimeCDCModelRow(fixture.Post, cells...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.EncodeCDC(context.Background(), testCDCEncodeInput(fixture, golem.EventCreated, nil, &stored)); err != nil {
		t.Fatalf("trusted stored NULL was rejected: %v", err)
	}
	masked, err := golem.RuntimeModelReadRow(fixture.Post, cells...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.EncodeCDC(context.Background(), testCDCEncodeInput(fixture, golem.EventCreated, nil, &masked)); err == nil {
		t.Fatal("ordinary nullable/masked row acquired exact CDC provenance")
	}
}

func testCDCEventEncoder(t testing.TB, fixture schematest.Fixture) (*cdcEventEncoder, *eventSchemaHistory) {
	t.Helper()
	history, err := newEventSchemaHistory(fixture.Registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := newCDCEventEncoder(fixture.Registry, history, 0)
	if err != nil {
		t.Fatal(err)
	}
	return encoder, history
}

func testCDCEncodeInput(fixture schematest.Fixture, action golem.EventAction, before, after *golem.RuntimeModelRow) eventcdc.EncodeInput {
	return eventcdc.EncodeInput{
		Adapter:             events.CDCIdentity{Name: "fixture-log", Version: "1.0.0", Provider: golem.SQLite},
		SourceTransactionID: "fixture:transaction:1", RecordedAt: time.Date(2026, 8, 7, 9, 34, 56, 987654000, time.UTC), Cursor: []byte("fixture-cursor"),
		EventID: golem.EventID{15: 1}, CausationID: golem.CausationID{15: 2}, Ordinal: 1,
		Model: fixture.Post, Action: action, Before: before, After: after,
	}
}

func testCDCPostRow(t testing.TB, fixture schematest.Fixture, ordinary bool, title string) golem.RuntimeModelRow {
	t.Helper()
	var row golem.RuntimeModelRow
	var err error
	if ordinary {
		row, err = golem.RuntimeModelReadRow(fixture.Post, testCDCPostCells(fixture, title)...)
	} else {
		row, err = golem.RuntimeCDCModelRow(fixture.Post, testCDCPostCells(fixture, title)...)
	}
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func testCDCPostCells(fixture schematest.Fixture, title string) []golem.RuntimeReadCell {
	return []golem.RuntimeReadCell{
		golem.RuntimePresentReadCell(fixture.PostID, golem.UUID{15: 1}, nil),
		golem.RuntimePresentReadCell(fixture.AuthorID, golem.UUID{15: 2}, nil),
		golem.RuntimePresentReadCell(fixture.PostTitle, title, nil),
	}
}
