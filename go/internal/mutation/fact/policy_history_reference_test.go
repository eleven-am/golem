package fact

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func historyReferenceFact(t *testing.T) (Envelope, testHistoricalResolver) {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, enabled := model.EventSchema()
	if !enabled {
		t.Fatal("fixture must subscribe the captured model")
	}
	digest, err := ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{7})),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	base, err := New(fixture.Registry, digest, EventID{1}, requirement, CausationID{2}, 2, 41, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := NewPolicyHistoryReference(PolicyHistoryFormatVersion, fixture.Registry.PolicyCaptureFingerprint(), golem.SchemaDigest{9})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base.WithPolicyHistory(reference)
	if err != nil {
		t.Fatal(err)
	}
	if base.FormatVersion() != 3 {
		t.Fatal("sealing mutated the original V3 value")
	}
	return sealed, testHistoricalResolver{registry: fixture.Registry, digest: digest, enabled: true}
}

func TestPolicyHistoryReferenceRoundTripAndOwnership(t *testing.T) {
	fact, resolver := historyReferenceFact(t)
	if fact.FormatVersion() != 4 || fact.CodecIdentity() != "golem.fact.v4" {
		t.Fatal("history fact is not V4")
	}
	reference, present := fact.PolicyHistory()
	if !present || reference.FormatVersion() != 1 || reference.ProjectionFingerprint() != resolver.registry.PolicyCaptureFingerprint() || reference.Digest() != (golem.SchemaDigest{9}) {
		t.Fatal("reference changed")
	}
	projection, digest := reference.ProjectionFingerprint(), reference.Digest()
	projection[0] ^= 1
	digest[0] ^= 1
	if reference.ProjectionFingerprint() == projection || reference.Digest() == digest {
		t.Fatal("reference accessors alias mutable storage")
	}
	if _, err := fact.WithPolicyHistory(reference); err != nil {
		t.Fatal(err)
	}
	other, err := NewPolicyHistoryReference(1, reference.ProjectionFingerprint(), digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fact.WithPolicyHistory(other); err == nil {
		t.Fatal("sealed graph reference was replaceable")
	}
	if _, err := fact.WithTransactionOrdinal(3); err == nil {
		t.Fatal("sealed ordinal was replaceable")
	}
	if _, err := fact.WithTransactionOrdinal(2); err != nil {
		t.Fatal(err)
	}
	stored, err := fact.OutboxRow(time.Unix(123, 456000))
	if err != nil {
		t.Fatal(err)
	}
	if stored.FactVersion != 4 || stored.CodecIdentity != "golem.fact.v4" {
		t.Fatal("stored format columns lost V4")
	}
	decoded, err := ValidateStoredRow(stored, resolver)
	if err != nil {
		t.Fatal(err)
	}
	got, present := decoded.PolicyHistory()
	if !present || got != reference {
		t.Fatal("reference did not round-trip")
	}
	again, err := Encode(decoded)
	if err != nil || !bytes.Equal(again, stored.Metadata) {
		t.Fatalf("V4 bytes changed: %v", err)
	}
	stored.Metadata[0] ^= 1
	owned, err := Encode(decoded)
	if err != nil || !bytes.Equal(owned, again) {
		t.Fatal("decoded value retained input byte storage")
	}
}

func TestPolicyHistoryReferenceRejectsInvalidReferenceAndWire(t *testing.T) {
	for _, value := range []struct {
		format             uint16
		projection, digest golem.SchemaDigest
	}{
		{0, golem.SchemaDigest{1}, golem.SchemaDigest{2}}, {2, golem.SchemaDigest{1}, golem.SchemaDigest{2}},
		{1, golem.SchemaDigest{}, golem.SchemaDigest{2}}, {1, golem.SchemaDigest{1}, golem.SchemaDigest{}},
	} {
		if _, err := NewPolicyHistoryReference(value.format, value.projection, value.digest); err == nil {
			t.Fatal("invalid reference admitted")
		}
	}
	fact, resolver := historyReferenceFact(t)
	if _, err := fact.WithPolicyHistory(PolicyHistoryReference{}); err == nil {
		t.Fatal("zero reference admitted")
	}
	encoded, err := Encode(fact)
	if err != nil {
		t.Fatal(err)
	}
	trailer := len(encoded) - 66
	generation := len(factMagic) + 2 + 4 + len(HistoryFactCodecIdentity) + 16
	mutations := map[string]func([]byte) []byte{
		"unknown fact version":      func(b []byte) []byte { binary.BigEndian.PutUint16(b[len(factMagic):], 5); return b },
		"V3 version with V4 codec":  func(b []byte) []byte { binary.BigEndian.PutUint16(b[len(factMagic):], 3); return b },
		"V4 version with V3 codec":  func(b []byte) []byte { b[len(factMagic)+2+4+len(HistoryFactCodecIdentity)-1] = '3'; return b },
		"unknown graph version":     func(b []byte) []byte { binary.BigEndian.PutUint16(b[trailer:], 2); return b },
		"wrong projection":          func(b []byte) []byte { b[trailer+2] ^= 1; return b },
		"zero projection":           func(b []byte) []byte { clear(b[trailer+2 : trailer+34]); return b },
		"zero digest":               func(b []byte) []byte { clear(b[trailer+34:]); return b },
		"wrong producer generation": func(b []byte) []byte { b[generation] ^= 1; return b },
		"zero producer generation":  func(b []byte) []byte { clear(b[generation : generation+32]); return b },
		"trailing byte":             func(b []byte) []byte { return append(b, 0) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWithResolver(mutate(bytes.Clone(encoded)), resolver); err == nil {
				t.Fatal("invalid history fact decoded")
			}
		})
	}
	for cut := range len(encoded) {
		if _, err := DecodeWithResolver(encoded[:cut], resolver); err == nil {
			t.Fatalf("truncation at %d decoded", cut)
		}
	}
	missing := resolver
	missing.enabled = false
	if _, err := DecodeWithResolver(encoded, missing); err == nil {
		t.Fatal("missing producer schema accepted")
	}
	wrong := resolver
	wrong.registry = schematest.NewSubscribedGraph(t).Registry
	if _, err := DecodeWithResolver(encoded, wrong); err == nil {
		t.Fatal("another generation accepted for historical capture")
	}
}

func TestPolicyHistoryStoredColumnsRemainBoundToMetadata(t *testing.T) {
	fact, resolver := historyReferenceFact(t)
	stored, err := fact.OutboxRow(time.Unix(123, 0))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*OutboxRow){
		"version": func(r *OutboxRow) { r.FactVersion = 3 }, "codec": func(r *OutboxRow) { r.CodecIdentity = CodecIdentity },
		"generation": func(r *OutboxRow) { r.GenerationFingerprint = "wrong" }, "event": func(r *OutboxRow) { r.EventID = r.CausationID },
		"causation": func(r *OutboxRow) { r.CausationID = r.EventID }, "ordinal": func(r *OutboxRow) { r.TransactionOrdinal++ },
		"model": func(r *OutboxRow) { r.ModelID = "wrong" }, "action": func(r *OutboxRow) { r.Action = "deleted" },
		"identity": func(r *OutboxRow) {
			r.AfterIdentity = bytes.Clone(r.AfterIdentity)
			r.AfterIdentity[len(r.AfterIdentity)-1] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			row := stored
			change(&row)
			if _, err := ValidateStoredRow(row, resolver); err == nil {
				t.Fatal("mismatched stored column admitted")
			}
		})
	}
}

func TestPolicyHistoryLeavesFrozenV3EncodingUnchanged(t *testing.T) {
	fixture := schematest.NewSubscribedGraph(t)
	component, err := mutationdecode.IdentityValue(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{9}))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := mutationdecode.NewIdentity(fixture.PostKey, []mutationdecode.IdentityComponent{component})
	if err != nil {
		t.Fatal(err)
	}
	base := Envelope{event: EventID{1}, generation: golem.SchemaDigest{2}, eventSchema: golem.SchemaDigest{3}, model: policyir.ModelID(fixture.Post), action: mutationir.FactCreated, causation: CausationID{5}, ordinal: 6, afterIdentity: &identity, rowVersion: 41}
	// Fixed wire vector transcribed from HEAD's V3 field order, not the changed encoder.
	const frozen = "474f4c454d4641435400030000000d676f6c656d2e666163742e7633" +
		"01000000000000000000000000000000" +
		"0200000000000000000000000000000000000000000000000000000000000000" +
		"0300000000000000000000000000000000000000000000000000000000000000" +
		"00000000000000000000000000000066" + "01" +
		"05000000000000000000000000000000" + "00000006" + "00" + "01" +
		"00000000000000000000000000000098" + "00000001" +
		"00000000000000000000000000000079" + "01" + "0a" +
		"09000000000000000000000000000000" +
		"00" + "0000000000000029" + "00000000"
	want, err := hex.DecodeString(frozen)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Encode(base)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("frozen V3 bytes changed: %v\ngot %x\nwant %x", err, got, want)
	}
	decoded, err := DecodeWithResolver(want, testHistoricalResolver{registry: fixture.Registry, digest: golem.SchemaDigest{3}, enabled: true})
	if err != nil {
		t.Fatal("frozen V3 reader regressed", err)
	}
	if decoded.FormatVersion() != 3 || decoded.RowVersion() != 41 || decoded.TransactionOrdinal() != 6 {
		t.Fatal("frozen V3 fields changed")
	}
	if base.FormatVersion() != 3 || base.CodecIdentity() != "golem.fact.v3" {
		t.Fatal("default format changed")
	}
	if _, present := base.PolicyHistory(); present {
		t.Fatal("V3 acquired a graph reference")
	}
	if _, err := base.WithTransactionOrdinal(7); err != nil {
		t.Fatal("unsealed V3 ordinal rewrite regressed", err)
	}
}
