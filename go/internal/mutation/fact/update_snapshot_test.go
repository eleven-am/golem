package fact

import (
	"bytes"
	"strings"
	"testing"
	"time"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func updatedSnapshotRequirement(t *testing.T, fixture schematest.Fixture, state mutationir.DeleteSnapshotState, stored []policyir.FieldID) mutationir.FactRequirement {
	t.Helper()
	id := []policyir.FieldID{policyir.FieldID(fixture.PostID)}
	requirement, err := mutationir.NewUpdateFactRequirement(id, id, state, stored)
	if err != nil {
		t.Fatal(err)
	}
	return mustBind(t, requirement)
}

func TestUpdatedFactCarriesItsBeforeSnapshot(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	title := policyir.FieldID(fixture.PostTitle)
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "published", 1))
	after := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "draft", 2))
	requirement := updatedSnapshotRequirement(t, fixture, mutationir.DeleteSnapshotStoredScalars, []policyir.FieldID{title})
	envelope, err := New(fixture.Registry, testEventSchema, EventID{1}, requirement, CausationID{2}, 1, 8, &before, &after)
	if err != nil {
		t.Fatal(err)
	}
	row, err := envelope.OutboxRow(time.Unix(5, 0))
	if err != nil {
		t.Fatal(err)
	}
	if row.Action != "updated" || len(row.DeleteSnapshot) == 0 || len(row.BeforeIdentity) == 0 || len(row.AfterIdentity) == 0 {
		t.Fatalf("updated outbox shape: %#v", row)
	}
	if bytes.Contains(row.Metadata, []byte("published")) || bytes.Contains(row.Metadata, []byte("draft")) {
		t.Fatal("a private snapshot field leaked into the public metadata")
	}
	joined, err := DecodeOutbox(row.Metadata, row.DeleteSnapshot, fixture.Registry, testEventSchema)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, present := joined.PrivateDeleteSnapshot()
	if !present || joined.DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars || !mutationdecode.EqualRow(snapshot, mustSelect(t, fixture, before, []policyir.FieldID{title})) {
		t.Fatalf("decoded snapshot is not the before image: present=%v state=%d", present, joined.DeleteSnapshotState())
	}
	if joined.RowVersion() != 8 {
		t.Fatalf("version = %d, want the after version 8", joined.RowVersion())
	}
	if size, sizeErr := envelope.EncodedSize(); sizeErr != nil || size != row.EncodedBytes() {
		t.Fatalf("encoded size = %d, row = %d, err = %v", size, row.EncodedBytes(), sizeErr)
	}
}

func TestUpdatedFactWithoutABeforeImageIsUnverifiable(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	title := policyir.FieldID(fixture.PostTitle)
	after := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "draft", 2))
	unverifiable := updatedSnapshotRequirement(t, fixture, mutationir.DeleteSnapshotUnverifiable, nil)
	envelope, err := New(fixture.Registry, testEventSchema, EventID{1}, unverifiable, CausationID{2}, 1, 8, nil, &after)
	if err != nil {
		t.Fatal(err)
	}
	beforeIdentity, hasBefore := envelope.BeforeIdentity()
	afterIdentity, hasAfter := envelope.AfterIdentity()
	if !hasBefore || !hasAfter || !bytes.Equal(mustEncodeIdentity(t, beforeIdentity), mustEncodeIdentity(t, afterIdentity)) {
		t.Fatal("an update without a before image must carry the after identity as its before identity")
	}
	row, err := envelope.OutboxRow(time.Unix(5, 0))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOutbox(row.Metadata, row.DeleteSnapshot, fixture.Registry, testEventSchema)
	if err != nil || decoded.DeleteSnapshotState() != mutationir.DeleteSnapshotUnverifiable || row.DeleteSnapshot != nil {
		t.Fatalf("unverifiable update: state=%d snapshot=%d err=%v", decoded.DeleteSnapshotState(), len(row.DeleteSnapshot), err)
	}

	stored := updatedSnapshotRequirement(t, fixture, mutationir.DeleteSnapshotStoredScalars, []policyir.FieldID{title})
	if _, err := New(fixture.Registry, testEventSchema, EventID{1}, stored, CausationID{2}, 1, 8, nil, &after); err == nil || !strings.Contains(err.Error(), "must be unverifiable") {
		t.Fatalf("a stored-scalar update without a before image was built: %v", err)
	}
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "published", 1))
	if _, err := New(fixture.Registry, testEventSchema, EventID{1}, stored, CausationID{2}, 1, 8, &before, nil); err == nil || !strings.Contains(err.Error(), "requires an after image") {
		t.Fatalf("an update without an after image was built: %v", err)
	}
	if _, err := New(fixture.Registry, testEventSchema, EventID{1}, unverifiable, CausationID{2}, 1, 8, &before, &after); err != nil {
		t.Fatalf("an unverifiable update that has a before image was refused: %v", err)
	}
}

func TestUpdatedFactCanDropItsSnapshotAndCreatedCannot(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	title := policyir.FieldID(fixture.PostTitle)
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "published", 1))
	after := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "draft", 2))
	requirement := updatedSnapshotRequirement(t, fixture, mutationir.DeleteSnapshotStoredScalars, []policyir.FieldID{title})
	envelope, err := New(fixture.Registry, testEventSchema, EventID{1}, requirement, CausationID{2}, 1, 8, &before, &after)
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := envelope.WithUnverifiableDeleteSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	row, err := dropped.OutboxRow(time.Unix(5, 0))
	if err != nil || row.DeleteSnapshot != nil {
		t.Fatalf("dropped snapshot: bytes=%d err=%v", len(row.DeleteSnapshot), err)
	}
	decoded, err := DecodeOutbox(row.Metadata, row.DeleteSnapshot, fixture.Registry, testEventSchema)
	if err != nil || decoded.DeleteSnapshotState() != mutationir.DeleteSnapshotUnverifiable || decoded.RowVersion() != 8 {
		t.Fatalf("dropped update: state=%d version=%d err=%v", decoded.DeleteSnapshotState(), decoded.RowVersion(), err)
	}
	created, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	createdFact, err := newTestFact(fixture.Registry, EventID{1}, created, CausationID{2}, 1, nil, &after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createdFact.WithUnverifiableDeleteSnapshot(); err == nil || !strings.Contains(err.Error(), "only an updated or deleted fact") {
		t.Fatalf("a created fact dropped a snapshot it never had: %v", err)
	}
}

func TestDecoderRefusesABeforeSnapshotShapeTheActionCannotCarry(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	title := policyir.FieldID(fixture.PostTitle)
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "published", 1))
	after := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "draft", 2))
	updated, err := New(fixture.Registry, testEventSchema, EventID{1}, updatedSnapshotRequirement(t, fixture, mutationir.DeleteSnapshotStoredScalars, []policyir.FieldID{title}), CausationID{2}, 1, 8, &before, &after)
	if err != nil {
		t.Fatal(err)
	}
	created, err := newTestFact(fixture.Registry, EventID{1}, mustCreatedRequirement(t, fixture), CausationID{2}, 1, nil, &after)
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		mutate func(Envelope) Envelope
		base   Envelope
		want   string
	}{
		"updated with an unusable state":  {func(e Envelope) Envelope { e.deleteState = mutationir.DeleteSnapshotNotApplicable; return e }, updated, "invalid before snapshot state"},
		"unverifiable update with fields": {func(e Envelope) Envelope { e.deleteState = mutationir.DeleteSnapshotUnverifiable; return e }, updated, "unverifiable fact carries snapshot fields"},
		"created with a snapshot state":   {func(e Envelope) Envelope { e.deleteState = mutationir.DeleteSnapshotStoredScalars; return e }, created, "created fact carries a before snapshot state"},
		"created with snapshot fields": {func(e Envelope) Envelope {
			e.snapshotFields = []policyir.FieldID{title}
			return e
		}, created, "private snapshot fields are valid only for update and delete"},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := Encode(test.mutate(test.base))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(encoded, fixture.Registry, testEventSchema); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}
}

func mustCreatedRequirement(t *testing.T, fixture schematest.Fixture) mutationir.FactRequirement {
	t.Helper()
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return requirement
}

func mustEncodeIdentity(t *testing.T, identity mutationdecode.Identity) []byte {
	t.Helper()
	encoded, err := EncodeIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
