package fact

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

var (
	factMagic     = []byte("GOLEMFACT")
	identityMagic = []byte("GOLEMID")
	rowMagic      = []byte("GOLEMROW")
)

const (
	maxCodecDepth = 64
	maxItems      = 1_000_000
)

// CodecError reports a corrupt or unsupported fact without exposing partially
// decoded state.
type CodecError struct {
	Offset int
	Detail string
}

func (failure *CodecError) Error() string {
	return fmt.Sprintf("P4_FACT_CODEC: byte=%d: %s", failure.Offset, failure.Detail)
}

type encoder struct {
	data []byte
	err  error
}

func (e *encoder) raw(value []byte) {
	if e.err == nil {
		e.data = append(e.data, value...)
	}
}
func (e *encoder) u8(value uint8) { e.raw([]byte{value}) }
func (e *encoder) u16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	e.raw(encoded[:])
}
func (e *encoder) u32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	e.raw(encoded[:])
}
func (e *encoder) u64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	e.raw(encoded[:])
}
func (e *encoder) i16(value int16) { e.u16(uint16(value)) }
func (e *encoder) i32(value int32) { e.u32(uint32(value)) }
func (e *encoder) i64(value int64) { e.u64(uint64(value)) }
func (e *encoder) bytes(value []byte) {
	if len(value) > math.MaxUint32 {
		e.err = fmt.Errorf("P4_FACT_CODEC: byte string exceeds uint32")
		return
	}
	e.u32(uint32(len(value)))
	e.raw(value)
}
func (e *encoder) text(value string) {
	if !utf8.ValidString(value) {
		e.err = fmt.Errorf("P4_FACT_CODEC: text is not UTF-8")
		return
	}
	e.bytes([]byte(value))
}

func Encode(envelope Envelope) ([]byte, error) {
	if envelope.formatVersion != FormatVersionV1 && envelope.formatVersion != FormatVersionV2 {
		return nil, fmt.Errorf("P4_FACT_CODEC: unsupported fact version %d", envelope.formatVersion)
	}
	expectedCodec := CodecIdentityV1
	if envelope.formatVersion == FormatVersionV2 {
		expectedCodec = CodecIdentityV2
		if envelope.eventSchema == (golem.SchemaDigest{}) {
			return nil, fmt.Errorf("P7_FACT_CODEC: event-schema fingerprint is zero")
		}
	}
	if envelope.codecIdentity != expectedCodec {
		return nil, fmt.Errorf("P4_FACT_CODEC: fact version and codec identity disagree")
	}
	e := encoder{}
	e.raw(factMagic)
	e.u16(envelope.formatVersion)
	e.text(envelope.codecIdentity)
	e.raw(envelope.event[:])
	e.raw(envelope.generation[:])
	if envelope.formatVersion == FormatVersionV2 {
		e.raw(envelope.eventSchema[:])
	}
	e.raw(envelope.model[:])
	e.u8(uint8(envelope.action))
	e.raw(envelope.causation[:])
	e.u32(envelope.ordinal)
	e.optionalIdentity(envelope.beforeIdentity)
	e.optionalIdentity(envelope.afterIdentity)
	if envelope.formatVersion == FormatVersionV2 {
		e.u8(uint8(envelope.deleteState))
	}
	e.fields(envelope.snapshotFields)
	if e.err != nil {
		return nil, e.err
	}
	return append([]byte(nil), e.data...), nil
}

func (e *encoder) optionalIdentity(identity *mutationdecode.Identity) {
	if identity == nil {
		e.u8(0)
		return
	}
	e.u8(1)
	e.identity(*identity)
}

func (e *encoder) identity(identity mutationdecode.Identity) {
	key := identity.KeyID()
	e.raw(key[:])
	components := identity.Components()
	e.u32(uint32(len(components)))
	for _, component := range components {
		field := component.FieldID()
		e.raw(field[:])
		if component.IsNull() {
			e.u8(0)
			continue
		}
		e.u8(1)
		value, _ := component.PolicyValue()
		e.value(value, 0)
	}
}

func (e *encoder) fields(fields []policyir.FieldID) {
	e.u32(uint32(len(fields)))
	for _, field := range fields {
		e.raw(field[:])
	}
}

func (e *encoder) row(row mutationdecode.Row) {
	model := row.ModelID()
	e.raw(model[:])
	cells := row.Cells()
	e.u32(uint32(len(cells)))
	for _, cell := range cells {
		field := cell.FieldID()
		e.raw(field[:])
		if cell.IsNull() {
			e.u8(0)
			continue
		}
		e.u8(1)
		value, _ := cell.PolicyValue()
		e.value(value, 0)
	}
}

func (e *encoder) value(value policyir.Value, depth int) {
	if depth > maxCodecDepth {
		e.err = fmt.Errorf("P4_FACT_CODEC: value nesting exceeds %d", maxCodecDepth)
		return
	}
	e.u8(uint8(value.Kind()))
	switch value.Kind() {
	case policyir.ValueBool:
		v, _ := value.Bool()
		if v {
			e.u8(1)
		} else {
			e.u8(0)
		}
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		v, _ := value.Signed()
		e.i64(v)
	case policyir.ValueFloat32:
		v, _ := value.Float32Bits()
		e.u32(v)
	case policyir.ValueFloat64:
		v, _ := value.Float64Bits()
		e.u64(v)
	case policyir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		e.i64(coefficient)
		e.u8(scale)
	case policyir.ValueString:
		v, _ := value.Text()
		e.text(v)
	case policyir.ValueBytes:
		v, _ := value.Bytes()
		e.bytes(v)
	case policyir.ValueUUID:
		v, _ := value.UUID()
		e.raw(v[:])
	case policyir.ValueDate:
		year, month, day, _ := value.Date()
		e.i16(year)
		e.u8(month)
		e.u8(day)
	case policyir.ValueTime:
		v, _ := value.Time()
		e.i64(v)
	case policyir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		e.i64(seconds)
		e.u32(nanos)
	case policyir.ValueEnum:
		enum, member, _ := value.Enum()
		e.raw(enum[:])
		e.raw(member[:])
	case policyir.ValueJSON:
		v, _ := value.JSON()
		e.json(v, depth+1)
	case policyir.ValueScalarList:
		values, _ := value.List()
		e.u32(uint32(len(values)))
		for _, item := range values {
			e.value(item, depth+1)
		}
	default:
		e.err = fmt.Errorf("P4_FACT_CODEC: unsupported value kind %d", value.Kind())
	}
}

func (e *encoder) json(value policyir.JSONValue, depth int) {
	if depth > maxCodecDepth {
		e.err = fmt.Errorf("P4_FACT_CODEC: JSON nesting exceeds %d", maxCodecDepth)
		return
	}
	e.u8(uint8(value.Kind()))
	switch value.Kind() {
	case policyir.JSONNull:
	case policyir.JSONBool:
		v, _ := value.Bool()
		if v {
			e.u8(1)
		} else {
			e.u8(0)
		}
	case policyir.JSONNumber:
		v, _ := value.Number()
		if v.Negative() {
			e.u8(1)
		} else {
			e.u8(0)
		}
		e.bytes(v.Coefficient())
		e.i32(v.Exponent())
	case policyir.JSONString:
		v, _ := value.Text()
		e.text(v)
	case policyir.JSONArray:
		values, _ := value.Array()
		e.u32(uint32(len(values)))
		for _, item := range values {
			e.json(item, depth+1)
		}
	case policyir.JSONObject:
		members, _ := value.Object()
		e.u32(uint32(len(members)))
		for _, member := range members {
			e.text(member.Key())
			e.json(member.Value(), depth+1)
		}
	default:
		e.err = fmt.Errorf("P4_FACT_CODEC: unsupported JSON kind %d", value.Kind())
	}
}

type decoder struct {
	data   []byte
	offset int
}

func (d *decoder) fail(format string, args ...any) error {
	return &CodecError{Offset: d.offset, Detail: fmt.Sprintf(format, args...)}
}
func (d *decoder) remaining() int { return len(d.data) - d.offset }
func (d *decoder) raw(size int) ([]byte, error) {
	if size < 0 || size > d.remaining() {
		return nil, d.fail("truncated input: need %d bytes, have %d", size, d.remaining())
	}
	result := d.data[d.offset : d.offset+size]
	d.offset += size
	return result, nil
}
func (d *decoder) u8() (uint8, error) {
	value, err := d.raw(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}
func (d *decoder) u16() (uint16, error) {
	value, err := d.raw(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(value), nil
}
func (d *decoder) u32() (uint32, error) {
	value, err := d.raw(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(value), nil
}
func (d *decoder) u64() (uint64, error) {
	value, err := d.raw(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(value), nil
}
func (d *decoder) i16() (int16, error) { v, err := d.u16(); return int16(v), err }
func (d *decoder) i32() (int32, error) { v, err := d.u32(); return int32(v), err }
func (d *decoder) i64() (int64, error) { v, err := d.u64(); return int64(v), err }
func (d *decoder) bytes() ([]byte, error) {
	length, err := d.u32()
	if err != nil {
		return nil, err
	}
	return d.raw(int(length))
}
func (d *decoder) text() (string, error) {
	value, err := d.bytes()
	if err != nil {
		return "", err
	}
	if !utf8.Valid(value) {
		return "", d.fail("text is not UTF-8")
	}
	return string(value), nil
}
func (d *decoder) fixed16() ([16]byte, error) {
	var result [16]byte
	value, err := d.raw(16)
	if err != nil {
		return result, err
	}
	copy(result[:], value)
	return result, nil
}
func (d *decoder) fixed32() ([32]byte, error) {
	var result [32]byte
	value, err := d.raw(32)
	if err != nil {
		return result, err
	}
	copy(result[:], value)
	return result, nil
}

// SchemaReference is the immutable decoder lookup key. V1 uses Generation;
// V2 additionally uses EventSchema, allowing a GraphQL-only generation change
// to decode pending facts without weakening schema validation.
type SchemaReference struct {
	FormatVersion uint16
	CodecIdentity string
	Generation    golem.SchemaDigest
	EventSchema   golem.SchemaDigest
}

// HistoricalSchemaResolver supplies the exact registry needed by a stored
// fact. Returning a different event-schema digest is rejected. Implementations
// may source current or generated historical bundles; this seam does not
// pretend those bundles are already persisted or discovered automatically.
type HistoricalSchemaResolver interface {
	ResolveFactSchema(SchemaReference) (*schema.Registry, golem.SchemaDigest, bool)
}

type activeResolver struct {
	registry    *schema.Registry
	eventSchema golem.SchemaDigest
}

func (resolver activeResolver) ResolveFactSchema(reference SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	if resolver.registry == nil {
		return nil, golem.SchemaDigest{}, false
	}
	if reference.FormatVersion == FormatVersionV1 {
		return resolver.registry, golem.SchemaDigest{}, reference.Generation == resolver.registry.GenerationDigest()
	}
	return resolver.registry, resolver.eventSchema, resolver.eventSchema != (golem.SchemaDigest{}) && reference.EventSchema == resolver.eventSchema
}

// Decode retains the exact V1 active-generation behavior used by P4.
func Decode(payload []byte, registry *schema.Registry) (Envelope, error) {
	return DecodeWithResolver(payload, activeResolver{registry: registry})
}

// DecodeV2 decodes against one active compiler-owned event-schema digest.
func DecodeV2(payload []byte, registry *schema.Registry, eventSchema golem.SchemaDigest) (Envelope, error) {
	return DecodeWithResolver(payload, activeResolver{registry: registry, eventSchema: eventSchema})
}

// DecodeWithResolver decodes V1 and V2 without falling back from a missing
// historical schema to the active schema.
func DecodeWithResolver(payload []byte, resolver HistoricalSchemaResolver) (Envelope, error) {
	if resolver == nil {
		return Envelope{}, &CodecError{Detail: "historical schema resolver is required"}
	}
	d := decoder{data: append([]byte(nil), payload...)}
	magic, err := d.raw(len(factMagic))
	if err != nil || !bytes.Equal(magic, factMagic) {
		return Envelope{}, d.fail("invalid fact magic")
	}
	version, err := d.u16()
	if err != nil || version != FormatVersionV1 && version != FormatVersionV2 {
		return Envelope{}, d.fail("unsupported fact version %d", version)
	}
	codec, err := d.text()
	expectedCodec := CodecIdentityV1
	if version == FormatVersionV2 {
		expectedCodec = CodecIdentityV2
	}
	if err != nil || codec != expectedCodec {
		return Envelope{}, d.fail("unsupported codec identity %q", codec)
	}
	eventBytes, err := d.fixed16()
	if err != nil {
		return Envelope{}, err
	}
	if eventBytes == ([16]byte{}) {
		return Envelope{}, d.fail("event identity is zero")
	}
	generationBytes, err := d.fixed32()
	if err != nil {
		return Envelope{}, err
	}
	var eventSchemaBytes [32]byte
	if version == FormatVersionV2 {
		eventSchemaBytes, err = d.fixed32()
		if err != nil {
			return Envelope{}, err
		}
		if eventSchemaBytes == ([32]byte{}) {
			return Envelope{}, d.fail("event-schema fingerprint is zero")
		}
	}
	reference := SchemaReference{FormatVersion: version, CodecIdentity: codec, Generation: golem.SchemaDigest(generationBytes), EventSchema: golem.SchemaDigest(eventSchemaBytes)}
	registry, resolvedEventSchema, resolved := resolver.ResolveFactSchema(reference)
	if !resolved || registry == nil {
		return Envelope{}, d.fail("required historical fact schema is unavailable")
	}
	if version == FormatVersionV1 && registry.GenerationDigest() != reference.Generation {
		return Envelope{}, d.fail("resolved V1 generation fingerprint disagrees")
	}
	if version == FormatVersionV2 && resolvedEventSchema != reference.EventSchema {
		return Envelope{}, d.fail("resolved V2 event-schema fingerprint disagrees")
	}
	modelBytes, err := d.fixed16()
	if err != nil {
		return Envelope{}, err
	}
	if modelBytes == ([16]byte{}) {
		return Envelope{}, d.fail("model identity is zero")
	}
	actionByte, err := d.u8()
	if err != nil {
		return Envelope{}, err
	}
	causationBytes, err := d.fixed16()
	if err != nil {
		return Envelope{}, err
	}
	if causationBytes == ([16]byte{}) {
		return Envelope{}, d.fail("causation identity is zero")
	}
	ordinal, err := d.u32()
	if err != nil {
		return Envelope{}, err
	}
	model := policyir.ModelID(modelBytes)
	before, err := d.optionalIdentity()
	if err != nil {
		return Envelope{}, err
	}
	after, err := d.optionalIdentity()
	if err != nil {
		return Envelope{}, err
	}
	deleteState := mutationir.DeleteSnapshotNotApplicable
	if version == FormatVersionV2 {
		state, stateErr := d.u8()
		if stateErr != nil {
			return Envelope{}, stateErr
		}
		deleteState = mutationir.DeleteSnapshotState(state)
	}
	snapshotFields, err := d.fields()
	if err != nil {
		return Envelope{}, err
	}
	if d.remaining() != 0 {
		return Envelope{}, d.fail("trailing bytes")
	}
	action := mutationir.FactAction(actionByte)
	if action < mutationir.FactCreated || action > mutationir.FactDeleted {
		return Envelope{}, d.fail("invalid fact action %d", action)
	}
	if version == FormatVersionV1 {
		if action == mutationir.FactDeleted {
			deleteState = mutationir.DeleteSnapshotUnverifiable
			if len(snapshotFields) != 0 {
				deleteState = mutationir.DeleteSnapshotStoredScalars
			}
		}
	} else if action == mutationir.FactDeleted {
		if deleteState != mutationir.DeleteSnapshotUnverifiable && deleteState != mutationir.DeleteSnapshotStoredScalars {
			return Envelope{}, d.fail("invalid delete snapshot state %d", deleteState)
		}
		if deleteState == mutationir.DeleteSnapshotUnverifiable && len(snapshotFields) != 0 {
			return Envelope{}, d.fail("unverifiable delete carries snapshot fields")
		}
	} else if deleteState != mutationir.DeleteSnapshotNotApplicable {
		return Envelope{}, d.fail("non-delete fact carries delete snapshot state")
	}
	if before != nil {
		if err := mutationdecode.ValidateIdentity(registry, model, *before); err != nil {
			return Envelope{}, d.fail("before identity: %v", err)
		}
	}
	if after != nil {
		if err := mutationdecode.ValidateIdentity(registry, model, *after); err != nil {
			return Envelope{}, d.fail("after identity: %v", err)
		}
	}
	if err := validateFactShape(action, before, after, deleteState, snapshotFields, registry, model); err != nil {
		return Envelope{}, d.fail("invalid envelope: %v", err)
	}
	result := Envelope{
		formatVersion: version, codecIdentity: codec,
		event: EventID(eventBytes), generation: golem.SchemaDigest(generationBytes), model: model,
		eventSchema: golem.SchemaDigest(eventSchemaBytes),
		action:      action, causation: CausationID(causationBytes), ordinal: ordinal,
		beforeIdentity: before, afterIdentity: after, snapshotFields: snapshotFields, deleteState: deleteState,
	}
	canonical, err := Encode(result)
	if err != nil || !bytes.Equal(canonical, payload) {
		return Envelope{}, d.fail("envelope is not canonically encoded")
	}
	return result, nil
}

// DecodeOutbox joins the versioned metadata with its separate private
// delete_snapshot column. Snapshot values never appear in Metadata.
func DecodeOutbox(metadata, deleteSnapshot []byte, registry *schema.Registry) (Envelope, error) {
	return decodeOutbox(metadata, deleteSnapshot, activeResolver{registry: registry})
}

func DecodeOutboxV2(metadata, deleteSnapshot []byte, registry *schema.Registry, eventSchema golem.SchemaDigest) (Envelope, error) {
	return decodeOutbox(metadata, deleteSnapshot, activeResolver{registry: registry, eventSchema: eventSchema})
}

func DecodeOutboxWithResolver(metadata, deleteSnapshot []byte, resolver HistoricalSchemaResolver) (Envelope, error) {
	return decodeOutbox(metadata, deleteSnapshot, resolver)
}

func decodeOutbox(metadata, deleteSnapshot []byte, resolver HistoricalSchemaResolver) (Envelope, error) {
	envelope, err := DecodeWithResolver(metadata, resolver)
	if err != nil {
		return Envelope{}, err
	}
	if len(envelope.snapshotFields) == 0 {
		if len(deleteSnapshot) != 0 {
			return Envelope{}, &CodecError{Detail: "delete snapshot is present but not configured"}
		}
		return envelope, nil
	}
	if len(deleteSnapshot) == 0 {
		return Envelope{}, &CodecError{Detail: "configured delete snapshot is absent"}
	}
	reference := SchemaReference{FormatVersion: envelope.formatVersion, CodecIdentity: envelope.codecIdentity, Generation: envelope.generation, EventSchema: envelope.eventSchema}
	registry, resolvedDigest, resolved := resolver.ResolveFactSchema(reference)
	if !resolved || registry == nil || envelope.formatVersion == FormatVersionV2 && resolvedDigest != envelope.eventSchema {
		return Envelope{}, &CodecError{Detail: "required historical delete-snapshot schema is unavailable"}
	}
	snapshot, err := DecodeDeleteSnapshot(deleteSnapshot, registry)
	if err != nil {
		return Envelope{}, err
	}
	if snapshot.ModelID() != envelope.model || !sameInventory(snapshot, envelope.snapshotFields) {
		return Envelope{}, &CodecError{Detail: "delete snapshot field inventory does not match metadata"}
	}
	envelope.deleteSnapshot = &snapshot
	return envelope, nil
}

func sameInventory(row mutationdecode.Row, fields []policyir.FieldID) bool {
	cells := row.Cells()
	if len(cells) != len(fields) {
		return false
	}
	for index := range cells {
		if cells[index].FieldID() != fields[index] {
			return false
		}
	}
	return true
}

func (d *decoder) optionalIdentity() (*mutationdecode.Identity, error) {
	present, err := d.u8()
	if err != nil {
		return nil, err
	}
	if present == 0 {
		return nil, nil
	}
	if present != 1 {
		return nil, d.fail("invalid identity presence marker %d", present)
	}
	identity, err := d.identity()
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

func (d *decoder) identity() (mutationdecode.Identity, error) {
	keyBytes, err := d.fixed16()
	if err != nil {
		return mutationdecode.Identity{}, err
	}
	count, err := d.u32()
	if err != nil {
		return mutationdecode.Identity{}, err
	}
	if count == 0 || count > maxItems || uint64(count)*17 > uint64(d.remaining()) {
		return mutationdecode.Identity{}, d.fail("invalid identity component count %d", count)
	}
	components := make([]mutationdecode.IdentityComponent, int(count))
	for index := range components {
		fieldBytes, fieldErr := d.fixed16()
		if fieldErr != nil {
			return mutationdecode.Identity{}, fieldErr
		}
		present, markerErr := d.u8()
		if markerErr != nil {
			return mutationdecode.Identity{}, markerErr
		}
		if present == 0 {
			components[index], err = mutationdecode.IdentityNull(policyir.FieldID(fieldBytes))
		} else if present == 1 {
			value, valueErr := d.value(0)
			if valueErr != nil {
				return mutationdecode.Identity{}, valueErr
			}
			components[index], err = mutationdecode.IdentityValue(policyir.FieldID(fieldBytes), value)
		} else {
			return mutationdecode.Identity{}, d.fail("invalid identity presence marker %d", present)
		}
		if err != nil {
			return mutationdecode.Identity{}, d.fail("invalid identity component: %v", err)
		}
	}
	identity, err := mutationdecode.NewIdentity(golem.KeyID(keyBytes), components)
	if err != nil {
		return mutationdecode.Identity{}, d.fail("invalid identity: %v", err)
	}
	return identity, nil
}

func (d *decoder) fields() ([]policyir.FieldID, error) {
	count, err := d.u32()
	if err != nil {
		return nil, err
	}
	if count > maxItems || uint64(count)*16 > uint64(d.remaining()) {
		return nil, d.fail("invalid field inventory count %d", count)
	}
	fields := make([]policyir.FieldID, int(count))
	for index := range fields {
		value, readErr := d.fixed16()
		if readErr != nil {
			return nil, readErr
		}
		fields[index] = policyir.FieldID(value)
	}
	return fields, nil
}

func validateFactShape(action mutationir.FactAction, before, after *mutationdecode.Identity, deleteState mutationir.DeleteSnapshotState, snapshotFields []policyir.FieldID, registry *schema.Registry, model policyir.ModelID) error {
	switch action {
	case mutationir.FactCreated:
		if before != nil || after == nil {
			return fmt.Errorf("created fact requires only after identity")
		}
	case mutationir.FactUpdated:
		if before == nil || after == nil {
			return fmt.Errorf("updated fact requires before and after identities")
		}
	case mutationir.FactDeleted:
		if before == nil || after != nil {
			return fmt.Errorf("deleted fact requires only before identity")
		}
	default:
		return fmt.Errorf("unknown fact action")
	}
	if action != mutationir.FactDeleted && len(snapshotFields) != 0 {
		return fmt.Errorf("private snapshot fields are valid only for delete")
	}
	if action == mutationir.FactDeleted {
		if deleteState != mutationir.DeleteSnapshotUnverifiable && deleteState != mutationir.DeleteSnapshotStoredScalars {
			return fmt.Errorf("delete snapshot verification state is invalid")
		}
		if deleteState == mutationir.DeleteSnapshotUnverifiable && len(snapshotFields) != 0 {
			return fmt.Errorf("unverifiable delete carries private snapshot fields")
		}
	} else if deleteState != mutationir.DeleteSnapshotNotApplicable {
		return fmt.Errorf("non-delete fact carries delete snapshot verification state")
	}
	for index, fieldID := range snapshotFields {
		if fieldID == (policyir.FieldID{}) || index > 0 && bytes.Compare(snapshotFields[index-1][:], fieldID[:]) >= 0 {
			return fmt.Errorf("private snapshot field inventory is not canonical")
		}
		field, ok := registry.Field(golem.ModelID(model), golem.FieldID(fieldID))
		if !ok || field.Kind() == compilerir.FieldRelation {
			return fmt.Errorf("private snapshot field is absent, foreign, or relational")
		}
	}
	return nil
}

func (d *decoder) row(registry *schema.Registry) (mutationdecode.Row, error) {
	modelBytes, err := d.fixed16()
	if err != nil {
		return mutationdecode.Row{}, err
	}
	count, err := d.u32()
	if err != nil {
		return mutationdecode.Row{}, err
	}
	if count > maxItems || uint64(count)*17 > uint64(d.remaining()) {
		return mutationdecode.Row{}, d.fail("invalid row cell count %d", count)
	}
	cells := make([]mutationdecode.Cell, int(count))
	for index := range cells {
		fieldBytes, fieldErr := d.fixed16()
		if fieldErr != nil {
			return mutationdecode.Row{}, fieldErr
		}
		present, markerErr := d.u8()
		if markerErr != nil {
			return mutationdecode.Row{}, markerErr
		}
		field := policyir.FieldID(fieldBytes)
		if present == 0 {
			cells[index] = mutationdecode.Null(field)
			continue
		}
		if present != 1 {
			return mutationdecode.Row{}, d.fail("invalid cell presence marker %d", present)
		}
		value, valueErr := d.value(0)
		if valueErr != nil {
			return mutationdecode.Row{}, valueErr
		}
		cells[index] = mutationdecode.Value(field, value)
	}
	row, err := mutationdecode.NewRow(registry, policyir.ModelID(modelBytes), cells)
	if err != nil {
		return mutationdecode.Row{}, d.fail("persisted row validation: %v", err)
	}
	return row, nil
}

func (d *decoder) value(depth int) (policyir.Value, error) {
	if depth > maxCodecDepth {
		return policyir.Value{}, d.fail("value nesting exceeds %d", maxCodecDepth)
	}
	tag, err := d.u8()
	if err != nil {
		return policyir.Value{}, err
	}
	kind := policyir.ValueKind(tag)
	var result policyir.Value
	switch kind {
	case policyir.ValueBool:
		value, readErr := d.u8()
		if readErr != nil || value > 1 {
			return result, d.fail("invalid boolean value %d", value)
		}
		result = policyir.BoolValue(value == 1)
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		value, readErr := d.i64()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.SignedValue(kind, value)
	case policyir.ValueFloat32:
		bits, readErr := d.u32()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.Float32Value(math.Float32frombits(bits))
		if err == nil {
			canonical, _ := result.Float32Bits()
			if canonical != bits {
				err = fmt.Errorf("non-canonical float32")
			}
		}
	case policyir.ValueFloat64:
		bits, readErr := d.u64()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.Float64Value(math.Float64frombits(bits))
		if err == nil {
			canonical, _ := result.Float64Bits()
			if canonical != bits {
				err = fmt.Errorf("non-canonical float64")
			}
		}
	case policyir.ValueDecimal:
		coefficient, readErr := d.i64()
		if readErr != nil {
			return result, readErr
		}
		scale, readErr := d.u8()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.NewDecimalValue(coefficient, scale)
		if err == nil {
			canonicalCoefficient, canonicalScale, _ := result.Decimal()
			if canonicalCoefficient != coefficient || canonicalScale != scale {
				err = fmt.Errorf("non-canonical decimal")
			}
		}
	case policyir.ValueString:
		value, readErr := d.text()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.StringValue(value)
	case policyir.ValueBytes:
		value, readErr := d.bytes()
		if readErr != nil {
			return result, readErr
		}
		result = policyir.BytesValue(value)
	case policyir.ValueUUID:
		value, readErr := d.fixed16()
		if readErr != nil {
			return result, readErr
		}
		result = policyir.UUIDValue(value)
	case policyir.ValueDate:
		year, readErr := d.i16()
		if readErr != nil {
			return result, readErr
		}
		month, readErr := d.u8()
		if readErr != nil {
			return result, readErr
		}
		day, readErr := d.u8()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.NewDateValue(year, month, day)
	case policyir.ValueTime:
		value, readErr := d.i64()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.NewTimeValue(value)
	case policyir.ValueDateTime:
		seconds, readErr := d.i64()
		if readErr != nil {
			return result, readErr
		}
		nanos, readErr := d.u32()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.NewDateTimeValue(seconds, nanos)
	case policyir.ValueEnum:
		enum, readErr := d.fixed16()
		if readErr != nil {
			return result, readErr
		}
		member, readErr := d.fixed16()
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.NewEnumValue(policyir.EnumID(enum), policyir.EnumValueID(member))
	case policyir.ValueJSON:
		value, readErr := d.json(depth + 1)
		if readErr != nil {
			return result, readErr
		}
		result, err = policyir.NewJSONValue(value)
	case policyir.ValueScalarList:
		count, readErr := d.u32()
		if readErr != nil {
			return result, readErr
		}
		if count > maxItems || count > uint32(d.remaining()) {
			return result, d.fail("invalid list length %d", count)
		}
		values := make([]policyir.Value, int(count))
		for index := range values {
			values[index], readErr = d.value(depth + 1)
			if readErr != nil {
				return result, readErr
			}
		}
		result, err = policyir.NewListValue(values)
	default:
		return result, d.fail("unknown value tag %d", tag)
	}
	if err != nil {
		return policyir.Value{}, d.fail("invalid logical value: %v", err)
	}
	return result, nil
}

func (d *decoder) json(depth int) (policyir.JSONValue, error) {
	if depth > maxCodecDepth {
		return policyir.JSONValue{}, d.fail("JSON nesting exceeds %d", maxCodecDepth)
	}
	tag, err := d.u8()
	if err != nil {
		return policyir.JSONValue{}, err
	}
	switch policyir.JSONKind(tag) {
	case policyir.JSONNull:
		return policyir.JSONNullValue(), nil
	case policyir.JSONBool:
		value, readErr := d.u8()
		if readErr != nil || value > 1 {
			return policyir.JSONValue{}, d.fail("invalid JSON boolean %d", value)
		}
		return policyir.JSONBoolValue(value == 1), nil
	case policyir.JSONNumber:
		negative, readErr := d.u8()
		if readErr != nil || negative > 1 {
			return policyir.JSONValue{}, d.fail("invalid JSON number sign %d", negative)
		}
		coefficient, readErr := d.bytes()
		if readErr != nil {
			return policyir.JSONValue{}, readErr
		}
		exponent, readErr := d.i32()
		if readErr != nil {
			return policyir.JSONValue{}, readErr
		}
		number, numberErr := policyir.NewJSONNumber(negative == 1, coefficient, exponent)
		if numberErr != nil {
			return policyir.JSONValue{}, d.fail("invalid exact JSON number: %v", numberErr)
		}
		return policyir.JSONNumberValueOf(number)
	case policyir.JSONString:
		value, readErr := d.text()
		if readErr != nil {
			return policyir.JSONValue{}, readErr
		}
		return policyir.JSONStringValue(value)
	case policyir.JSONArray:
		count, readErr := d.u32()
		if readErr != nil {
			return policyir.JSONValue{}, readErr
		}
		if count > maxItems || count > uint32(d.remaining()) {
			return policyir.JSONValue{}, d.fail("invalid JSON array length %d", count)
		}
		values := make([]policyir.JSONValue, int(count))
		for index := range values {
			values[index], readErr = d.json(depth + 1)
			if readErr != nil {
				return policyir.JSONValue{}, readErr
			}
		}
		return policyir.JSONArrayValue(values)
	case policyir.JSONObject:
		count, readErr := d.u32()
		if readErr != nil {
			return policyir.JSONValue{}, readErr
		}
		if count > maxItems || count > uint32(d.remaining()) {
			return policyir.JSONValue{}, d.fail("invalid JSON object length %d", count)
		}
		members := make([]policyir.JSONMember, int(count))
		previous := ""
		for index := range members {
			key, keyErr := d.text()
			if keyErr != nil {
				return policyir.JSONValue{}, keyErr
			}
			if index > 0 && key <= previous {
				return policyir.JSONValue{}, d.fail("JSON object keys are not canonical")
			}
			value, valueErr := d.json(depth + 1)
			if valueErr != nil {
				return policyir.JSONValue{}, valueErr
			}
			members[index], valueErr = policyir.NewJSONMember(key, value)
			if valueErr != nil {
				return policyir.JSONValue{}, d.fail("invalid JSON member: %v", valueErr)
			}
			previous = key
		}
		return policyir.JSONObjectValue(members)
	default:
		return policyir.JSONValue{}, d.fail("unknown JSON tag %d", tag)
	}
}

func encodeIdentity(identity mutationdecode.Identity) ([]byte, error) {
	e := encoder{}
	e.raw(identityMagic)
	e.u16(FormatVersion)
	e.identity(identity)
	if e.err != nil {
		return nil, e.err
	}
	return append([]byte(nil), e.data...), nil
}

// EncodeIdentity returns the detached canonical identity bytes shared by
// outbox persistence and nested mutation identity ordering/deduplication.
func EncodeIdentity(identity mutationdecode.Identity) ([]byte, error) {
	return encodeIdentity(identity)
}

// DecodeIdentity decodes one before_identity/after_identity column without
// stringifying any component. The active envelope generation check occurs when
// Metadata is decoded; this value carries only the declared key and values.
func DecodeIdentity(payload []byte) (mutationdecode.Identity, error) {
	d := decoder{data: append([]byte(nil), payload...)}
	magic, err := d.raw(len(identityMagic))
	if err != nil || !bytes.Equal(magic, identityMagic) {
		return mutationdecode.Identity{}, d.fail("invalid identity magic")
	}
	version, err := d.u16()
	if err != nil || version != FormatVersion {
		return mutationdecode.Identity{}, d.fail("unsupported identity version %d", version)
	}
	identity, err := d.identity()
	if d.remaining() != 0 {
		return mutationdecode.Identity{}, d.fail("trailing identity bytes")
	}
	if err != nil {
		return mutationdecode.Identity{}, err
	}
	canonical, err := encodeIdentity(identity)
	if err != nil || !bytes.Equal(canonical, payload) {
		return mutationdecode.Identity{}, d.fail("identity is not canonically encoded")
	}
	return identity, nil
}

func encodeRowBlob(row mutationdecode.Row) ([]byte, error) {
	e := encoder{}
	e.raw(rowMagic)
	e.u16(FormatVersion)
	e.row(row)
	if e.err != nil {
		return nil, e.err
	}
	return append([]byte(nil), e.data...), nil
}

// DecodeDeleteSnapshot decodes the private pre-delete image using the active
// schema. It is intentionally separate from public event delivery.
func DecodeDeleteSnapshot(payload []byte, registry *schema.Registry) (mutationdecode.Row, error) {
	if registry == nil {
		return mutationdecode.Row{}, &CodecError{Detail: "active schema registry is required"}
	}
	d := decoder{data: append([]byte(nil), payload...)}
	magic, err := d.raw(len(rowMagic))
	if err != nil || !bytes.Equal(magic, rowMagic) {
		return mutationdecode.Row{}, d.fail("invalid row magic")
	}
	version, err := d.u16()
	if err != nil || version != FormatVersion {
		return mutationdecode.Row{}, d.fail("unsupported row version %d", version)
	}
	row, err := d.row(registry)
	if err != nil {
		return mutationdecode.Row{}, err
	}
	if d.remaining() != 0 {
		return mutationdecode.Row{}, d.fail("trailing row bytes")
	}
	canonical, err := encodeRowBlob(row)
	if err != nil || !bytes.Equal(canonical, payload) {
		return mutationdecode.Row{}, d.fail("row is not canonically encoded")
	}
	return row, nil
}
