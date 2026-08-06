package fact

import (
	"bytes"
	"math/rand"
	"testing"
	"time"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestOutboxExactCodecPreservesEveryLogicalType(t *testing.T) {
	values := exactValueMatrix(t)
	if len(values) != 16 {
		t.Fatalf("logical value matrix has %d entries; want 16", len(values))
	}
	for _, value := range values {
		value := value
		t.Run(valueKindName(value.Kind()), func(t *testing.T) {
			e := encoder{}
			e.value(value, 0)
			if e.err != nil {
				t.Fatal(e.err)
			}
			d := decoder{data: append([]byte(nil), e.data...)}
			decoded, err := d.value(0)
			if err != nil {
				t.Fatal(err)
			}
			if d.remaining() != 0 || !mutationdecode.EqualValue(value, decoded) {
				t.Fatalf("value kind %d did not round-trip exactly", value.Kind())
			}
			again := encoder{}
			again.value(decoded, 0)
			if again.err != nil || !bytes.Equal(e.data, again.data) {
				t.Fatal("logical value encoding is not deterministic")
			}
		})
	}
}

func TestExactMutationDiffCoversEveryLogicalType(t *testing.T) {
	values := exactValueMatrix(t)
	if len(values) != 16 {
		t.Fatalf("logical value matrix has %d entries; want 16", len(values))
	}
	for _, before := range values {
		before := before
		t.Run(valueKindName(before.Kind()), func(t *testing.T) {
			if !mutationdecode.EqualValue(before, before) {
				t.Fatal("unchanged logical value was classified as changed")
			}
			after := differentExactValue(t, before.Kind())
			if after.Kind() != before.Kind() || mutationdecode.EqualValue(before, after) {
				t.Fatalf("same-kind logical change was collapsed: before=%d after=%d", before.Kind(), after.Kind())
			}
		})
	}
}

func differentExactValue(t testing.TB, kind policyir.ValueKind) policyir.Value {
	t.Helper()
	switch kind {
	case policyir.ValueBool:
		return policyir.BoolValue(false)
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		value, err := policyir.SignedValue(kind, 0)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueFloat32:
		value, err := policyir.Float32Value(0)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueFloat64:
		value, err := policyir.Float64Value(0)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueDecimal:
		value, err := policyir.NewDecimalValue(1, 0)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueString:
		value, err := policyir.StringValue("different")
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueBytes:
		return policyir.BytesValue([]byte{9})
	case policyir.ValueUUID:
		return policyir.UUIDValue([16]byte{9})
	case policyir.ValueDate:
		value, err := policyir.NewDateValue(2025, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueTime:
		value, err := policyir.NewTimeValue(1)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueDateTime:
		value, err := policyir.NewDateTimeValue(1, 0)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueEnum:
		value, err := policyir.NewEnumValue(policyir.EnumID{1}, policyir.EnumValueID{9})
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueJSON:
		value, err := policyir.NewJSONValue(policyir.JSONNullValue())
		if err != nil {
			t.Fatal(err)
		}
		return value
	case policyir.ValueScalarList:
		value, err := policyir.NewListValue(nil)
		if err != nil {
			t.Fatal(err)
		}
		return value
	default:
		t.Fatalf("unhandled logical value kind %d", kind)
		return policyir.Value{}
	}
}

func TestEnvelopeRoundTripDeterministicUnderCellShuffle(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	baseCells := postCells(t, fixture, [16]byte{1}, "before", 9_007_199_254_740_993)
	before := mustRow(t, fixture, baseCells)
	afterCells := postCells(t, fixture, [16]byte{2}, "after", 9_007_199_254_740_994)
	after := mustRow(t, fixture, afterCells)
	requirement, _ := mutationir.NewFactRequirement(mutationir.FactUpdated,
		[]policyir.FieldID{policyir.FieldID(fixture.PostID)},
		[]policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	envelope, err := New(fixture.Registry, EventID{1}, requirement, CausationID{2}, 7, &before, &after)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Encode(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(want, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Encode(decoded)
	if !bytes.Equal(want, got) {
		t.Fatal("decoded envelope did not reproduce canonical bytes")
	}
	decodedBefore, beforeOK := decoded.BeforeIdentity()
	decodedAfter, afterOK := decoded.AfterIdentity()
	if !beforeOK || !afterOK || len(decodedBefore.Components()) != 1 || len(decodedAfter.Components()) != 1 {
		t.Fatal("required before/after identities were not lossless")
	}
	// Metadata must not carry unrelated scalar values from wider SQL images.
	if bytes.Contains(want, []byte("before")) || bytes.Contains(want, []byte("after")) {
		t.Fatal("fact metadata leaked non-required scalar fields")
	}
	changedExtra := mustRow(t, fixture, postCells(t, fixture, [16]byte{1}, "completely different secret", 1))
	changedEnvelope, err := New(fixture.Registry, EventID{1}, requirement, CausationID{2}, 7, &changedExtra, &after)
	if err != nil {
		t.Fatal(err)
	}
	changedBytes, _ := Encode(changedEnvelope)
	if !bytes.Equal(want, changedBytes) {
		t.Fatal("non-required scalar values changed metadata bytes")
	}
	partialBefore, err := before.Select(fixture.Registry, requirement.BeforeIdentity())
	if err != nil {
		t.Fatal(err)
	}
	partialAfter, err := after.Select(fixture.Registry, requirement.AfterIdentity())
	if err != nil {
		t.Fatal(err)
	}
	partialEnvelope, err := New(fixture.Registry, EventID{1}, requirement, CausationID{2}, 7, &partialBefore, &partialAfter)
	if err != nil {
		t.Fatalf("fact rejected requirement-sufficient partial images: %v", err)
	}
	partialBytes, _ := Encode(partialEnvelope)
	if !bytes.Equal(want, partialBytes) {
		t.Fatal("partial and wider images produced different requirement-scoped metadata")
	}

	random := rand.New(rand.NewSource(42))
	for iteration := 0; iteration < 50; iteration++ {
		shuffledBefore := append([]mutationdecode.Cell(nil), baseCells...)
		shuffledAfter := append([]mutationdecode.Cell(nil), afterCells...)
		random.Shuffle(len(shuffledBefore), func(i, j int) { shuffledBefore[i], shuffledBefore[j] = shuffledBefore[j], shuffledBefore[i] })
		random.Shuffle(len(shuffledAfter), func(i, j int) { shuffledAfter[i], shuffledAfter[j] = shuffledAfter[j], shuffledAfter[i] })
		left := mustRow(t, fixture, shuffledBefore)
		right := mustRow(t, fixture, shuffledAfter)
		candidate, err := New(fixture.Registry, EventID{1}, requirement, CausationID{2}, 7, &left, &right)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := Encode(candidate)
		if !bytes.Equal(encoded, want) {
			t.Fatalf("shuffle %d changed canonical fact bytes", iteration)
		}
	}
}

func TestOutboxDeleteSnapshotIdentityAndSize(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{3}, "deleted", 42))
	requirement, _ := mutationir.NewFactRequirement(mutationir.FactDeleted,
		[]policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil,
		[]policyir.FieldID{policyir.FieldID(fixture.PostTitle)})
	envelope, err := New(fixture.Registry, EventID{1}, requirement, CausationID{2}, 9, &before, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 8, 6, 12, 0, 0, 123_456_789, time.FixedZone("test", 3600))
	row, err := envelope.OutboxRow(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if row.FactVersion != 1 || row.CodecIdentity != CodecIdentity || row.Action != "deleted" || row.AfterIdentity != nil || len(row.BeforeIdentity) == 0 || len(row.DeleteSnapshot) == 0 {
		t.Fatalf("invalid delete outbox shape: %#v", row)
	}
	if row.RecordedAt.Location() != time.UTC || row.RecordedAt.Nanosecond()%1_000 != 0 {
		t.Fatalf("recorded_at=%s; want normalized UTC microseconds", row.RecordedAt)
	}
	identity, err := DecodeIdentity(row.BeforeIdentity)
	if err != nil || identity.KeyID() != fixture.PostKey || len(identity.Components()) != 1 {
		t.Fatalf("decode identity: identity=%#v err=%v", identity, err)
	}
	snapshot, err := DecodeDeleteSnapshot(row.DeleteSnapshot, fixture.Registry)
	if err != nil || len(snapshot.Cells()) != 1 || snapshot.Cells()[0].FieldID() != policyir.FieldID(fixture.PostTitle) {
		t.Fatalf("decode snapshot: err=%v", err)
	}
	if bytes.Contains(row.Metadata, []byte("deleted")) {
		t.Fatal("private delete field leaked into public fact metadata")
	}
	joined, err := DecodeOutbox(row.Metadata, row.DeleteSnapshot, fixture.Registry)
	joinedSnapshot, joinedOK := joined.PrivateDeleteSnapshot()
	if err != nil || !joinedOK || !mutationdecode.EqualRow(snapshot, joinedSnapshot) {
		t.Fatalf("decode outbox join: ok=%v err=%v", joinedOK, err)
	}
	size, err := envelope.EncodedSize()
	if err != nil || size != row.EncodedBytes() {
		t.Fatalf("encoded size=%d row=%d err=%v", size, row.EncodedBytes(), err)
	}

	row.Metadata[0] ^= 0xff
	fresh, err := envelope.OutboxRow(recorded)
	if err != nil || fresh.Metadata[0] == row.Metadata[0] {
		t.Fatal("outbox row mutation escaped into immutable envelope")
	}
}

func TestCorruptCodecFailsClosed(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	after := mustRow(t, fixture, postCells(t, fixture, [16]byte{1}, "created", 7))
	requirement, _ := mutationir.NewFactRequirement(mutationir.FactCreated, nil,
		[]policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	envelope, err := New(fixture.Registry, EventID{1}, requirement, CausationID{2}, 0, nil, &after)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := Encode(envelope)
	for length := 0; length < len(encoded); length++ {
		if _, err := Decode(encoded[:length], fixture.Registry); err == nil {
			t.Fatalf("truncated envelope length %d was accepted", length)
		}
	}
	corruptions := [][]byte{
		append([]byte(nil), encoded...),
		append([]byte(nil), encoded...),
		append(append([]byte(nil), encoded...), 0),
	}
	corruptions[0][0] ^= 0xff
	// Generation begins after magic, version, and codec length+text.
	generationOffset := len(factMagic) + 2 + 4 + len(CodecIdentity) + 16
	corruptions[1][generationOffset] ^= 0xff
	for index, corrupted := range corruptions {
		if _, err := Decode(corrupted, fixture.Registry); err == nil {
			t.Fatalf("corruption %d was accepted", index)
		}
	}

	row, _ := envelope.OutboxRow(time.Unix(1, 0))
	for length := 0; length < len(row.AfterIdentity); length++ {
		if _, err := DecodeIdentity(row.AfterIdentity[:length]); err == nil {
			t.Fatalf("truncated identity length %d was accepted", length)
		}
	}
}

func TestDeleteSnapshotCanBeUnconfiguredAndAbsent(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{4}, "must-not-leak", 99))
	requirement, _ := mutationir.NewFactRequirement(mutationir.FactDeleted,
		[]policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil, nil)
	envelope, err := New(fixture.Registry, EventID{4}, requirement, CausationID{5}, 1, &before, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := envelope.OutboxRow(time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if row.DeleteSnapshot != nil || bytes.Contains(row.Metadata, []byte("must-not-leak")) {
		t.Fatal("unconfigured delete snapshot or unrelated scalar leaked into outbox")
	}
	if _, err := DecodeOutbox(row.Metadata, nil, fixture.Registry); err != nil {
		t.Fatalf("absent unconfigured delete snapshot was rejected: %v", err)
	}
}

func exactValueMatrix(t *testing.T) []policyir.Value {
	t.Helper()
	int16Value, _ := policyir.SignedValue(policyir.ValueInt16, -32_768)
	int32Value, _ := policyir.SignedValue(policyir.ValueInt32, 2_147_483_647)
	int64Value, _ := policyir.SignedValue(policyir.ValueInt64, 9_007_199_254_740_993)
	float32Value, _ := policyir.Float32Value(1.25)
	float64Value, _ := policyir.Float64Value(1.0000000000000002)
	decimal, _ := policyir.NewDecimalValue(123_456_789_012_345_678, 13)
	text, _ := policyir.StringValue("héllo")
	date, _ := policyir.NewDateValue(2024, 2, 29)
	clock, _ := policyir.NewTimeValue(86_399_999_999)
	instant, _ := policyir.NewDateTimeValue(-1, 999_999_000)
	enumeration, _ := policyir.NewEnumValue(policyir.EnumID{1}, policyir.EnumValueID{2})
	number, _ := policyir.NewJSONNumber(false, []byte("9007199254740993"), -7)
	numberValue, _ := policyir.JSONNumberValueOf(number)
	member, _ := policyir.NewJSONMember("exact", numberValue)
	object, _ := policyir.JSONObjectValue([]policyir.JSONMember{member})
	jsonValue, _ := policyir.NewJSONValue(object)
	list, _ := policyir.NewListValue([]policyir.Value{text})
	return []policyir.Value{
		policyir.BoolValue(true), int16Value, int32Value, int64Value,
		float32Value, float64Value, decimal, text, policyir.BytesValue([]byte{0, 1, 255}),
		policyir.UUIDValue([16]byte{1, 2, 3}), date, clock, instant, enumeration, jsonValue, list,
	}
}

func postCells(t *testing.T, fixture schematest.Fixture, id [16]byte, title string, big int64) []mutationdecode.Cell {
	t.Helper()
	text, _ := policyir.StringValue(title)
	integer, _ := policyir.SignedValue(policyir.ValueInt64, big)
	decimal, _ := policyir.NewDecimalValue(123_456_789_012_345_678, 13)
	return []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostDecimal), decimal),
		mutationdecode.Value(policyir.FieldID(fixture.PostTitle), text),
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue(id)),
		mutationdecode.Value(policyir.FieldID(fixture.PostBigInt), integer),
		mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{9})),
	}
}

func mustRow(t *testing.T, fixture schematest.Fixture, cells []mutationdecode.Cell) mutationdecode.Row {
	t.Helper()
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), cells)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func valueKindName(kind policyir.ValueKind) string {
	return map[policyir.ValueKind]string{
		policyir.ValueBool: "bool", policyir.ValueInt16: "int16", policyir.ValueInt32: "int32",
		policyir.ValueInt64: "int64", policyir.ValueFloat32: "float32", policyir.ValueFloat64: "float64",
		policyir.ValueDecimal: "decimal", policyir.ValueString: "string", policyir.ValueBytes: "bytes",
		policyir.ValueUUID: "uuid", policyir.ValueDate: "date", policyir.ValueTime: "time",
		policyir.ValueDateTime: "datetime", policyir.ValueEnum: "enum", policyir.ValueJSON: "json",
		policyir.ValueScalarList: "scalar-list",
	}[kind]
}
