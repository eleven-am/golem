// Package codec owns the canonical, bounded golem.event.v1 transport envelope.
// It wraps one already validated immutable golem.fact.v1 or golem.fact.v2 row;
// it never reinterprets or rewrites the nested fact bytes.
package codec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
)

const (
	FormatVersion uint16 = 1
	CodecIdentity        = "golem.event.v1"

	DefaultMaxEncodedBytes = 2 << 20
	HardMaxEncodedBytes    = 16 << 20
)

var magic = []byte("GOLEMEVENT")

type Limits struct {
	MaxEncodedBytes int
}

func (limits Limits) normalized() (Limits, error) {
	if limits.MaxEncodedBytes == 0 {
		limits.MaxEncodedBytes = DefaultMaxEncodedBytes
	}
	if limits.MaxEncodedBytes < minimumEnvelopeBytes() || limits.MaxEncodedBytes > HardMaxEncodedBytes {
		return Limits{}, fmt.Errorf("GOLEM_EVENT_CODEC: encoded event byte limit %d is outside [%d,%d]", limits.MaxEncodedBytes, minimumEnvelopeBytes(), HardMaxEncodedBytes)
	}
	return limits, nil
}

func minimumEnvelopeBytes() int {
	return len(magic) + 2 + 2 + len(CodecIdentity) + 8 + 4 + 4
}

// Error is a sanitized hostile-input failure. Offset points only into the
// trusted event byte sequence and never contains fact, snapshot, or driver
// contents.
type Error struct {
	Offset int
	Detail string
}

func (failure *Error) Error() string {
	return fmt.Sprintf("GOLEM_EVENT_CODEC: byte=%d: %s", failure.Offset, failure.Detail)
}

// Envelope is one immutable validated transport event. Encoded returns an
// owned copy; Fact exposes the already schema-validated internal fact value to
// later internal P7 layers.
type Envelope struct {
	fact                mutationfact.Envelope
	resolvedEventSchema golem.EventSchemaDigest
	recordedAt          time.Time
	encoded             []byte
}

func (envelope Envelope) FormatVersion() uint16                { return FormatVersion }
func (envelope Envelope) CodecIdentity() string                { return CodecIdentity }
func (envelope Envelope) EventID() golem.EventID               { return golem.EventID(envelope.fact.EventID()) }
func (envelope Envelope) GenerationDigest() golem.SchemaDigest { return envelope.fact.Generation() }
func (envelope Envelope) ModelID() golem.ModelID               { return golem.ModelID(envelope.fact.ModelID()) }
func (envelope Envelope) CausationID() golem.CausationID {
	return golem.CausationID(envelope.fact.CausationID())
}
func (envelope Envelope) TransactionOrdinal() uint32  { return envelope.fact.TransactionOrdinal() }
func (envelope Envelope) RecordedAt() time.Time       { return envelope.recordedAt }
func (envelope Envelope) Encoded() []byte             { return append([]byte(nil), envelope.encoded...) }
func (envelope Envelope) Fact() mutationfact.Envelope { return envelope.fact }
func (envelope Envelope) Action() golem.EventAction {
	switch envelope.fact.Action() {
	case mutationir.FactCreated:
		return golem.EventCreated
	case mutationir.FactUpdated:
		return golem.EventUpdated
	case mutationir.FactDeleted:
		return golem.EventDeleted
	default:
		return ""
	}
}
func (envelope Envelope) EventSchemaDigest() (golem.EventSchemaDigest, bool) {
	digest, present := envelope.fact.EventSchemaDigest()
	return golem.EventSchemaDigest(digest), present
}

// ResolvedEventSchemaDigest is the logical event schema proven by the
// historical resolver. V1 does not carry this digest on wire, while V2 does;
// later typed delivery requires a non-zero value and compares it to the
// generated factory's schema.
func (envelope Envelope) ResolvedEventSchemaDigest() golem.EventSchemaDigest {
	return envelope.resolvedEventSchema
}

// EncodeStoredRow validates every duplicated immutable outbox column against
// its canonical nested fact before constructing golem.event.v1.
func EncodeStoredRow(row mutationfact.OutboxRow, resolver mutationfact.HistoricalSchemaResolver, limits Limits) (Envelope, error) {
	normalized, err := limits.normalized()
	if err != nil {
		return Envelope{}, err
	}
	fact, err := mutationfact.ValidateStoredRow(row, resolver)
	if err != nil {
		return Envelope{}, fmt.Errorf("GOLEM_EVENT_CODEC: validate stored fact: %w", err)
	}
	canonicalRow, err := fact.OutboxRow(row.RecordedAt)
	if err != nil {
		return Envelope{}, fmt.Errorf("GOLEM_EVENT_CODEC: canonical stored fact: %w", err)
	}
	resolvedEventSchema, err := resolveEventSchema(fact, resolver)
	if err != nil {
		return Envelope{}, err
	}
	encoded, err := encodeParts(canonicalRow.RecordedAt, canonicalRow.Metadata, canonicalRow.DeleteSnapshot, normalized.MaxEncodedBytes)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{fact: fact, resolvedEventSchema: resolvedEventSchema, recordedAt: canonicalRow.RecordedAt, encoded: encoded}, nil
}

func Decode(encoded []byte, resolver mutationfact.HistoricalSchemaResolver, limits Limits) (Envelope, error) {
	normalized, err := limits.normalized()
	if err != nil {
		return Envelope{}, err
	}
	if len(encoded) > normalized.MaxEncodedBytes {
		return Envelope{}, &Error{Detail: fmt.Sprintf("encoded event has %d bytes; maximum is %d", len(encoded), normalized.MaxEncodedBytes)}
	}
	if len(encoded) < minimumEnvelopeBytes() {
		return Envelope{}, &Error{Detail: "truncated event envelope"}
	}
	decoder := wireDecoder{data: encoded, limit: normalized.MaxEncodedBytes}
	readMagic, err := decoder.raw(len(magic))
	if err != nil || !bytes.Equal(readMagic, magic) {
		return Envelope{}, decoder.failure("invalid event magic")
	}
	version, err := decoder.u16()
	if err != nil || version != FormatVersion {
		return Envelope{}, decoder.failure("unsupported event version %d", version)
	}
	identity, err := decoder.text16()
	if err != nil || identity != CodecIdentity {
		return Envelope{}, decoder.failure("unsupported event codec identity")
	}
	microseconds, err := decoder.i64()
	if err != nil {
		return Envelope{}, err
	}
	recordedAt := time.UnixMicro(microseconds).UTC()
	if recordedAt.IsZero() || recordedAt.UnixMicro() != microseconds {
		return Envelope{}, decoder.failure("recorded time is invalid")
	}
	metadata, err := decoder.bytes32()
	if err != nil {
		return Envelope{}, err
	}
	if len(metadata) == 0 {
		return Envelope{}, decoder.failure("nested fact metadata is empty")
	}
	snapshot, err := decoder.bytes32()
	if err != nil {
		return Envelope{}, err
	}
	if decoder.remaining() != 0 {
		return Envelope{}, decoder.failure("trailing bytes")
	}
	fact, err := mutationfact.DecodeOutboxWithResolver(metadata, snapshot, resolver)
	if err != nil {
		return Envelope{}, fmt.Errorf("GOLEM_EVENT_CODEC: decode nested fact: %w", err)
	}
	canonicalRow, err := fact.OutboxRow(recordedAt)
	if err != nil {
		return Envelope{}, fmt.Errorf("GOLEM_EVENT_CODEC: canonical nested fact: %w", err)
	}
	resolvedEventSchema, err := resolveEventSchema(fact, resolver)
	if err != nil {
		return Envelope{}, err
	}
	canonical, err := encodeParts(recordedAt, canonicalRow.Metadata, canonicalRow.DeleteSnapshot, normalized.MaxEncodedBytes)
	if err != nil {
		return Envelope{}, err
	}
	if !bytes.Equal(canonical, encoded) {
		return Envelope{}, decoder.failure("event envelope is not canonically encoded")
	}
	return Envelope{fact: fact, resolvedEventSchema: resolvedEventSchema, recordedAt: recordedAt, encoded: append([]byte(nil), canonical...)}, nil
}

func resolveEventSchema(fact mutationfact.Envelope, resolver mutationfact.HistoricalSchemaResolver) (golem.EventSchemaDigest, error) {
	if resolver == nil {
		return golem.EventSchemaDigest{}, fmt.Errorf("GOLEM_EVENT_CODEC: historical schema resolver is required")
	}
	wireSchema, hasWireSchema := fact.EventSchemaDigest()
	reference := mutationfact.SchemaReference{
		FormatVersion: fact.FormatVersion(),
		CodecIdentity: fact.CodecIdentity(),
		Generation:    fact.Generation(),
		EventSchema:   wireSchema,
	}
	registry, resolved, ok := resolver.ResolveFactSchema(reference)
	if !ok || registry == nil {
		return golem.EventSchemaDigest{}, fmt.Errorf("GOLEM_EVENT_CODEC: resolved event schema is unavailable")
	}
	if hasWireSchema && resolved != wireSchema {
		return golem.EventSchemaDigest{}, fmt.Errorf("GOLEM_EVENT_CODEC: wire and resolved event schemas differ")
	}
	if !hasWireSchema {
		model, exists := registry.Model(golem.ModelID(fact.ModelID()))
		if !exists {
			return golem.EventSchemaDigest{}, fmt.Errorf("GOLEM_EVENT_CODEC: resolved V1 model is unavailable")
		}
		fingerprint, _, enabled := model.EventSchema()
		if !enabled {
			return golem.EventSchemaDigest{}, fmt.Errorf("GOLEM_EVENT_CODEC: resolved V1 model has no event schema")
		}
		parsed, parseErr := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if parseErr != nil {
			return golem.EventSchemaDigest{}, fmt.Errorf("GOLEM_EVENT_CODEC: resolved V1 event schema: %w", parseErr)
		}
		resolved = parsed
	}
	return golem.EventSchemaDigest(resolved), nil
}

func encodeParts(recordedAt time.Time, metadata, snapshot []byte, maximum int) ([]byte, error) {
	recordedAt = recordedAt.UTC().Truncate(time.Microsecond)
	if recordedAt.IsZero() {
		return nil, fmt.Errorf("GOLEM_EVENT_CODEC: recorded time is zero")
	}
	if len(metadata) == 0 {
		return nil, fmt.Errorf("GOLEM_EVENT_CODEC: nested fact metadata is empty")
	}
	if len(metadata) > math.MaxUint32 || len(snapshot) > math.MaxUint32 {
		return nil, fmt.Errorf("GOLEM_EVENT_CODEC: nested fact component exceeds uint32")
	}
	size := minimumEnvelopeBytes() + len(metadata) + len(snapshot)
	if size > maximum {
		return nil, fmt.Errorf("GOLEM_EVENT_CODEC: encoded event has %d bytes; maximum is %d", size, maximum)
	}
	encoded := make([]byte, 0, size)
	encoded = append(encoded, magic...)
	encoded = binary.BigEndian.AppendUint16(encoded, FormatVersion)
	encoded = binary.BigEndian.AppendUint16(encoded, uint16(len(CodecIdentity)))
	encoded = append(encoded, CodecIdentity...)
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(recordedAt.UnixMicro()))
	encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(metadata)))
	encoded = append(encoded, metadata...)
	encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(snapshot)))
	encoded = append(encoded, snapshot...)
	return encoded, nil
}

type wireDecoder struct {
	data   []byte
	offset int
	limit  int
}

func (decoder *wireDecoder) remaining() int { return len(decoder.data) - decoder.offset }
func (decoder *wireDecoder) failure(format string, values ...any) error {
	return &Error{Offset: decoder.offset, Detail: fmt.Sprintf(format, values...)}
}
func (decoder *wireDecoder) raw(size int) ([]byte, error) {
	if size < 0 || size > decoder.remaining() || size > decoder.limit {
		return nil, decoder.failure("truncated or oversized component")
	}
	result := decoder.data[decoder.offset : decoder.offset+size]
	decoder.offset += size
	return result, nil
}
func (decoder *wireDecoder) u16() (uint16, error) {
	value, err := decoder.raw(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(value), nil
}
func (decoder *wireDecoder) u32() (uint32, error) {
	value, err := decoder.raw(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(value), nil
}
func (decoder *wireDecoder) i64() (int64, error) {
	value, err := decoder.raw(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(value)), nil
}
func (decoder *wireDecoder) text16() (string, error) {
	length, err := decoder.u16()
	if err != nil {
		return "", err
	}
	value, err := decoder.raw(int(length))
	if err != nil {
		return "", err
	}
	return string(value), nil
}
func (decoder *wireDecoder) bytes32() ([]byte, error) {
	length, err := decoder.u32()
	if err != nil {
		return nil, err
	}
	if uint64(length) > uint64(decoder.remaining()) || uint64(length) > uint64(decoder.limit) {
		return nil, decoder.failure("truncated or oversized byte component")
	}
	return decoder.raw(int(length))
}
