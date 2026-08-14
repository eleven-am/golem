package fact

import (
	"testing"
	"time"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestScalarCompositeIdentityAndDeleteSnapshotRoundTrip(t *testing.T) {
	t.Run("scalar", func(t *testing.T) {
		fixture := schematest.NewIndexedExact(t)
		before := mustRow(t, fixture, postCells(t, fixture, [16]byte{7}, "private", 101))
		identity := []policyir.FieldID{policyir.FieldID(fixture.PostID)}
		snapshot := []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle), policyir.FieldID(fixture.PostBigInt)}
		decoded := p7RoundTripDelete(t, fixture.Registry, before, identity, snapshot, EventID{1}, CausationID{2})
		assertP7IdentityFields(t, decoded, identity)
		private, ok := decoded.PrivateDeleteSnapshot()
		if !ok || !mutationdecode.EqualRow(private, mustSelect(t, fixture, before, snapshot)) {
			t.Fatal("scalar private delete snapshot changed")
		}
	})

	t.Run("composite", func(t *testing.T) {
		fixture := schematest.NewCompositeRelation(t)
		cells := []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.ItemRegion), policyir.UUIDValue([16]byte{15: 1})),
			mutationdecode.Value(policyir.FieldID(fixture.ItemID), policyir.UUIDValue([16]byte{15: 2})),
			mutationdecode.Value(policyir.FieldID(fixture.OwnerRegion), policyir.UUIDValue([16]byte{15: 3})),
			mutationdecode.Value(policyir.FieldID(fixture.OwnerID), policyir.UUIDValue([16]byte{15: 4})),
		}
		before, err := mutationdecode.NewCompleteRow(fixture.Registry, policyir.ModelID(fixture.Item), cells)
		if err != nil {
			t.Fatal(err)
		}
		identity := []policyir.FieldID{policyir.FieldID(fixture.ItemRegion), policyir.FieldID(fixture.ItemID)}
		snapshot := []policyir.FieldID{policyir.FieldID(fixture.ItemRegion), policyir.FieldID(fixture.ItemID), policyir.FieldID(fixture.OwnerRegion), policyir.FieldID(fixture.OwnerID)}
		decoded := p7RoundTripDelete(t, fixture.Registry, before, identity, snapshot, EventID{3}, CausationID{4})
		assertP7IdentityFields(t, decoded, identity)
		private, ok := decoded.PrivateDeleteSnapshot()
		if !ok || !mutationdecode.EqualRow(private, before) {
			t.Fatal("composite private delete snapshot changed")
		}
	})
}

func p7RoundTripDelete(t testing.TB, registry *schema.Registry, before mutationdecode.Row, identity, snapshot []policyir.FieldID, event EventID, causation CausationID) Envelope {
	t.Helper()
	requirement, err := mutationir.NewDeleteFactRequirement(identity, mutationir.DeleteSnapshotStoredScalars, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fact, err := New(registry, event, requirement, causation, 1, &before, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := fact.OutboxRow(time.Unix(20, 123456000).UTC())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOutbox(row.Metadata, row.DeleteSnapshot, registry)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func assertP7IdentityFields(t testing.TB, fact Envelope, expected []policyir.FieldID) {
	t.Helper()
	identity, ok := fact.BeforeIdentity()
	if !ok {
		t.Fatal("before identity is absent")
	}
	components := identity.Components()
	if len(components) != len(expected) {
		t.Fatalf("identity width=%d want=%d", len(components), len(expected))
	}
	for index, component := range components {
		if component.FieldID() != expected[index] || component.IsNull() {
			t.Fatalf("identity component %d=%x null=%t want=%x", index, component.FieldID(), component.IsNull(), expected[index])
		}
	}
}
