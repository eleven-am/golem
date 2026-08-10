package codec

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type testResolver struct {
	registry *schema.Registry
	digest   golem.SchemaDigest
}

func (resolver testResolver) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	if resolver.registry == nil {
		return nil, golem.SchemaDigest{}, false
	}
	switch reference.FormatVersion {
	case mutationfact.FormatVersionV1:
		return resolver.registry, resolver.digest, reference.Generation == resolver.registry.GenerationDigest()
	case mutationfact.FormatVersionV2:
		return resolver.registry, resolver.digest, reference.EventSchema == resolver.digest
	default:
		return nil, golem.SchemaDigest{}, false
	}
}

func TestCanonicalEventV2PreservesFactBytesAndRecordedTime(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, snapshotFields, enabled := model.EventSchema()
	if !enabled {
		t.Fatal("subscribed fixture has no event schema")
	}
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	row := postRow(t, fixture, [16]byte{7})
	privateSnapshot := make([]policyir.FieldID, len(snapshotFields))
	for index, field := range snapshotFields {
		privateSnapshot[index] = policyir.FieldID(field)
	}
	requirement, err := mutationir.NewDeleteFactRequirement(
		[]policyir.FieldID{policyir.FieldID(fixture.PostID)},
		mutationir.DeleteSnapshotStoredScalars,
		privateSnapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	fact, err := mutationfact.NewV2(fixture.Registry, digest, mutationfact.EventID{1}, requirement, mutationfact.CausationID{2}, 1, &row, nil)
	if err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("hostile-offset", 9*60*60)
	recorded := time.Date(2026, 8, 7, 12, 34, 56, 987654321, zone)
	stored, err := fact.OutboxRow(recorded)
	if err != nil {
		t.Fatal(err)
	}
	resolver := testResolver{registry: fixture.Registry, digest: digest}
	envelope, err := EncodeStoredRow(stored, resolver, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantTime := recorded.UTC().Truncate(time.Microsecond)
	if !envelope.RecordedAt().Equal(wantTime) || envelope.RecordedAt().Location() != time.UTC {
		t.Fatalf("recordedAt=%v; want canonical %v", envelope.RecordedAt(), wantTime)
	}
	if envelope.ResolvedEventSchemaDigest() != golem.EventSchemaDigest(digest) {
		t.Fatal("resolver-proven event schema was not retained")
	}
	encoded := envelope.Encoded()
	metadata, snapshot := nestedParts(t, encoded)
	if len(snapshot) == 0 || !bytes.Equal(metadata, stored.Metadata) || !bytes.Equal(snapshot, stored.DeleteSnapshot) {
		t.Fatal("event wrapper rewrote canonical fact bytes")
	}
	decoded, err := Decode(encoded, resolver, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Encoded(), encoded) || decoded.Action() != golem.EventDeleted || decoded.ResolvedEventSchemaDigest() != golem.EventSchemaDigest(digest) {
		t.Fatal("event codec did not round-trip canonically")
	}
	owned := decoded.Encoded()
	owned[0] ^= 0xff
	if bytes.Equal(owned, decoded.Encoded()) {
		t.Fatal("Encoded returned aliased storage")
	}
}

func TestCanonicalEventV2PreservesCompositeIdentityAndResolvedSchema(t *testing.T) {
	fixture := schematest.NewCompositeRelation(t)
	region, id := [16]byte{3}, [16]byte{4}
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Item), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.ItemRegion), policyir.UUIDValue(region)),
		mutationdecode.Value(policyir.FieldID(fixture.ItemID), policyir.UUIDValue(id)),
		mutationdecode.Value(policyir.FieldID(fixture.OwnerRegion), policyir.UUIDValue([16]byte{5})),
		mutationdecode.Value(policyir.FieldID(fixture.OwnerID), policyir.UUIDValue([16]byte{6})),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.ItemRegion), policyir.FieldID(fixture.ItemID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := golem.SchemaDigest{7, 7, 7}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	fact, err := mutationfact.NewV2(fixture.Registry, digest, mutationfact.EventID{9}, requirement, mutationfact.CausationID{8}, 1, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fact.OutboxRow(time.Unix(10, 999))
	if err != nil {
		t.Fatal(err)
	}
	resolver := testResolver{registry: fixture.Registry, digest: digest}
	envelope, err := EncodeStoredRow(stored, resolver, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if wire, present := envelope.EventSchemaDigest(); !present || wire != golem.EventSchemaDigest(digest) {
		t.Fatal("V2 event schema is absent or changed")
	}
	if envelope.ResolvedEventSchemaDigest() != golem.EventSchemaDigest(digest) {
		t.Fatal("V2 resolution did not retain the logical event schema")
	}
	identity, ok := envelope.Fact().AfterIdentity()
	if !ok {
		t.Fatal("composite after identity is absent")
	}
	components := identity.Components()
	if len(components) != 2 || components[0].FieldID() != policyir.FieldID(fixture.ItemRegion) || components[1].FieldID() != policyir.FieldID(fixture.ItemID) {
		t.Fatalf("composite identity order changed: %#v", components)
	}
	first, _ := components[0].PolicyValue()
	second, _ := components[1].PolicyValue()
	if !mutationdecode.EqualValue(first, policyir.UUIDValue(region)) || !mutationdecode.EqualValue(second, policyir.UUIDValue(id)) {
		t.Fatal("composite identity values changed")
	}
	if _, err := Decode(envelope.Encoded(), testResolver{registry: fixture.Registry, digest: golem.SchemaDigest{9}}, Limits{}); err == nil {
		t.Fatal("different V2 event schema was accepted")
	}
}

func TestEventCodecRejectsHostileBoundsTruncationAndMalformedWire(t *testing.T) {
	fixture := schematest.NewCompositeRelation(t)
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Item), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.ItemRegion), policyir.UUIDValue([16]byte{1})),
		mutationdecode.Value(policyir.FieldID(fixture.ItemID), policyir.UUIDValue([16]byte{2})),
		mutationdecode.Value(policyir.FieldID(fixture.OwnerRegion), policyir.UUIDValue([16]byte{3})),
		mutationdecode.Value(policyir.FieldID(fixture.OwnerID), policyir.UUIDValue([16]byte{4})),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.ItemRegion), policyir.FieldID(fixture.ItemID)}, nil)
	digest := golem.SchemaDigest{7}
	requirement, _ = requirement.WithEventSchema([32]byte(digest))
	fact, err := mutationfact.NewV2(fixture.Registry, digest, mutationfact.EventID{1}, requirement, mutationfact.CausationID{2}, 1, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := fact.OutboxRow(time.Unix(100, 0))
	resolver := testResolver{registry: fixture.Registry, digest: digest}
	valid, err := EncodeStoredRow(stored, resolver, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	encoded := valid.Encoded()
	for length := 0; length < len(encoded); length++ {
		if _, err := Decode(encoded[:length], resolver, Limits{}); err == nil {
			t.Fatalf("truncation at %d bytes was accepted", length)
		}
	}
	mutations := map[string]func([]byte){
		"magic":        func(value []byte) { value[0] ^= 0xff },
		"version":      func(value []byte) { binary.BigEndian.PutUint16(value[len(magic):], 2) },
		"codec-length": func(value []byte) { binary.BigEndian.PutUint16(value[len(magic)+2:], 0xffff) },
		"zero-time": func(value []byte) {
			binary.BigEndian.PutUint64(value[len(magic)+2+2+len(CodecIdentity):], uint64(time.Time{}.UnixMicro()))
		},
		"metadata-length": func(value []byte) {
			binary.BigEndian.PutUint32(value[len(magic)+2+2+len(CodecIdentity)+8:], ^uint32(0))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := append([]byte(nil), encoded...)
			mutate(candidate)
			if _, err := Decode(candidate, resolver, Limits{}); err == nil {
				t.Fatal("malformed event was accepted")
			}
		})
	}
	if _, err := Decode(append(encoded, 0), resolver, Limits{}); err == nil {
		t.Fatal("trailing bytes were accepted")
	}
	if _, err := Decode(encoded, resolver, Limits{MaxEncodedBytes: len(encoded) - 1}); err == nil {
		t.Fatal("configured byte bound was ignored")
	}
	if _, err := Decode(encoded, resolver, Limits{MaxEncodedBytes: HardMaxEncodedBytes + 1}); err == nil {
		t.Fatal("hard byte bound was ignored")
	}
}

func FuzzEventDecodeNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("GOLEMEVENT"))
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = Decode(input, testResolver{}, Limits{})
	})
}

func postRow(t *testing.T, fixture schematest.Fixture, id [16]byte) mutationdecode.Row {
	t.Helper()
	title, _ := policyir.StringValue("exact-π")
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue(id)),
		mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{8})),
		mutationdecode.Value(policyir.FieldID(fixture.PostTitle), title),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func nestedParts(t *testing.T, encoded []byte) ([]byte, []byte) {
	t.Helper()
	offset := len(magic) + 2
	codecLength := int(binary.BigEndian.Uint16(encoded[offset:]))
	offset += 2 + codecLength + 8
	metadataLength := int(binary.BigEndian.Uint32(encoded[offset:]))
	offset += 4
	metadata := encoded[offset : offset+metadataLength]
	offset += metadataLength
	snapshotLength := int(binary.BigEndian.Uint32(encoded[offset:]))
	offset += 4
	return metadata, encoded[offset : offset+snapshotLength]
}
