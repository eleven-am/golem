package physical

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
)

const maxCanonicalDecodeDepth = 1024

// CanonicalDecode decodes the versioned binary document emitted by
// CanonicalEncode. The decoder is deliberately schema-directed: type names,
// field names, field counts, and value tags must exactly match PhysicalSchema.
// A successful decode is validated and must reproduce payload byte-for-byte
// when canonically encoded again.
func CanonicalDecode(payload []byte) (PhysicalSchema, error) {
	return canonicalDecode(payload, decodeCurrent)
}

// CanonicalDecodeHistorical decodes the explicitly supported physical
// v1 canonical history. It exists only for immutable historical artifacts;
// active generated provider documents must use CanonicalDecode. Version 1
// cannot synthesize later physical identities.
func CanonicalDecodeHistorical(payload []byte) (PhysicalSchema, error) {
	return canonicalDecode(payload, decodeHistoricalV1Only)
}

// CanonicalDecodeHistoricalV2 decodes only the independently frozen physical
// v2 document. It is for immutable reviewed history and never an active
// provider bootstrap path.
func CanonicalDecodeHistoricalV2(payload []byte) (PhysicalSchema, error) {
	return canonicalDecode(payload, decodeHistoricalV2Only)
}

// CanonicalDecodeHistoricalV3 decodes only the independently frozen physical
// v3 document. It never uses the mutable current struct projection or rules.
func CanonicalDecodeHistoricalV3(payload []byte) (PhysicalSchema, error) {
	return canonicalDecode(payload, decodeHistoricalV3Only)
}

// CanonicalDecodeReviewedHistory accepts only the explicitly retained v1, v2,
// and v3 projections. It is the migration-history boundary; callers that claim
// one exact version should use the corresponding version-specific decoder.
func CanonicalDecodeReviewedHistory(payload []byte) (PhysicalSchema, error) {
	return canonicalDecode(payload, decodeReviewedHistory)
}

type decodeMode uint8

const (
	decodeCurrent decodeMode = iota
	decodeHistoricalV1Only
	decodeHistoricalV2Only
	decodeHistoricalV3Only
	decodeReviewedHistory
)

func canonicalDecode(payload []byte, mode decodeMode) (PhysicalSchema, error) {
	decoder := binaryDecoder{payload: payload}
	magic, err := decoder.bytes()
	if err != nil {
		return PhysicalSchema{}, decoder.wrap("magic", err)
	}
	if !bytes.Equal(magic, canonicalMagic) {
		return PhysicalSchema{}, decoder.fail("magic", "unknown canonical document magic")
	}
	version, err := decoder.uint()
	if err != nil {
		return PhysicalSchema{}, decoder.wrap("canonical format version", err)
	}
	acceptCurrent := mode == decodeCurrent
	acceptV1 := mode == decodeHistoricalV1Only || mode == decodeReviewedHistory
	acceptV2 := mode == decodeHistoricalV2Only || mode == decodeReviewedHistory
	acceptV3 := mode == decodeHistoricalV3Only || mode == decodeReviewedHistory
	accepted := version == uint64(CanonicalFormatVersion) && acceptCurrent || version == 1 && acceptV1 || version == 2 && acceptV2 || version == 3 && acceptV3
	if !accepted {
		return PhysicalSchema{}, decoder.fail("canonical format version", "got %d, want %d", version, CanonicalFormatVersion)
	}
	decoder.canonicalVersion = uint32(version)
	if version == 3 && acceptV3 {
		decoder.frozenProjection = historicalV3StructFields
	}
	if version == 1 {
		// This guard owns the retained DTO/type projection and must run before
		// caller bytes are interpreted as the mutable current root struct.
		if err := validateHistoricalV1SchemaShape(); err != nil {
			return PhysicalSchema{}, fmt.Errorf("physical canonical decode: historical v1 schema shape: %w", err)
		}
	}
	if version == 2 {
		if err := validateHistoricalV2SchemaShape(); err != nil {
			return PhysicalSchema{}, fmt.Errorf("physical canonical decode: historical v2 schema shape: %w", err)
		}
	}
	if version == 3 && acceptV3 {
		if err := validateHistoricalV3SchemaShape(); err != nil {
			return PhysicalSchema{}, fmt.Errorf("physical canonical decode: historical v3 schema shape: %w", err)
		}
	}

	var schema PhysicalSchema
	if err := decoder.value(reflect.ValueOf(&schema).Elem(), 0); err != nil {
		return PhysicalSchema{}, err
	}
	if decoder.remaining() != 0 {
		return PhysicalSchema{}, decoder.fail("document", "%d trailing bytes", decoder.remaining())
	}
	if err := validateDecodedSchema(schema, uint32(version), mode); err != nil {
		return PhysicalSchema{}, fmt.Errorf("physical canonical decode: invalid schema: %w", err)
	}
	var reencoded []byte
	if uint32(version) == CanonicalFormatVersion && mode == decodeCurrent {
		reencoded, err = CanonicalEncode(schema)
	} else if uint32(version) == 1 {
		normalized, normalizeErr := NormalizeHistoricalV1(schema)
		if normalizeErr != nil {
			return PhysicalSchema{}, fmt.Errorf("physical canonical decode: normalize historical v1: %w", normalizeErr)
		}
		reencoded, err = canonicalValueVersion(normalized, uint32(version))
	} else if uint32(version) == 2 {
		normalized, normalizeErr := NormalizeHistoricalV2(schema)
		if normalizeErr != nil {
			return PhysicalSchema{}, fmt.Errorf("physical canonical decode: normalize historical v2: %w", normalizeErr)
		}
		reencoded, err = canonicalValueVersion(normalized, uint32(version))
	} else {
		normalized, normalizeErr := NormalizeHistoricalV3(schema)
		if normalizeErr != nil {
			return PhysicalSchema{}, fmt.Errorf("physical canonical decode: normalize historical v3: %w", normalizeErr)
		}
		reencoded, err = canonicalHistoricalValueVersion(normalized, 3, historicalV3StructFields)
	}
	if err != nil {
		return PhysicalSchema{}, fmt.Errorf("physical canonical decode: re-encode: %w", err)
	}
	if !bytes.Equal(reencoded, payload) {
		return PhysicalSchema{}, fmt.Errorf("physical canonical decode: document is not in canonical normalized form")
	}
	return schema, nil
}

func validateDecodedSchema(schema PhysicalSchema, canonicalVersion uint32, mode decodeMode) error {
	if canonicalVersion == CanonicalFormatVersion && mode == decodeCurrent {
		return Validate(schema)
	}
	if canonicalVersion == 3 && schema.Version == 3 && schema.CanonicalVersion == 3 {
		return validateHistoricalV3(schema)
	}
	if canonicalVersion != 1 || schema.Version != 1 || schema.CanonicalVersion != 1 {
		if canonicalVersion != 2 || schema.Version != 2 || schema.CanonicalVersion != 2 {
			return fmt.Errorf("unsupported historical physical format/canonical versions %d/%d", schema.Version, schema.CanonicalVersion)
		}
		return validateHistoricalV2(schema)
	}
	return validateHistoricalV1(schema)
}

// CanonicalDecodeVerified is the SchemaBundle integration boundary. It checks
// the independently domain-separated application and system fingerprints only
// after the complete physical document has decoded and validated.
func CanonicalDecodeVerified(payload []byte, expectedPhysical, expectedSystem Digest) (PhysicalSchema, error) {
	schema, err := CanonicalDecode(payload)
	if err != nil {
		return PhysicalSchema{}, err
	}
	physicalFingerprint, err := PhysicalFingerprint(schema)
	if err != nil {
		return PhysicalSchema{}, fmt.Errorf("physical canonical decode: physical fingerprint: %w", err)
	}
	if physicalFingerprint != expectedPhysical {
		return PhysicalSchema{}, fmt.Errorf("physical canonical decode: physical fingerprint mismatch: got %s want %s", physicalFingerprint, expectedPhysical)
	}
	systemFingerprint, err := SystemFingerprint(schema.Provider, schema.System)
	if err != nil {
		return PhysicalSchema{}, fmt.Errorf("physical canonical decode: system fingerprint: %w", err)
	}
	if systemFingerprint != expectedSystem {
		return PhysicalSchema{}, fmt.Errorf("physical canonical decode: system fingerprint mismatch: got %s want %s", systemFingerprint, expectedSystem)
	}
	return schema, nil
}

type binaryDecoder struct {
	payload          []byte
	offset           int
	canonicalVersion uint32
	frozenProjection map[string][]string
}

func (d *binaryDecoder) remaining() int { return len(d.payload) - d.offset }

func (d *binaryDecoder) fail(subject, format string, arguments ...any) error {
	return fmt.Errorf("physical canonical decode at byte %d: %s: %s", d.offset, subject, fmt.Sprintf(format, arguments...))
}

func (d *binaryDecoder) wrap(subject string, err error) error {
	return d.fail(subject, "%v", err)
}

func (d *binaryDecoder) byte() (byte, error) {
	if d.remaining() == 0 {
		return 0, fmt.Errorf("truncated input")
	}
	value := d.payload[d.offset]
	d.offset++
	return value, nil
}

func (d *binaryDecoder) uint() (uint64, error) {
	value, length := binary.Uvarint(d.payload[d.offset:])
	switch {
	case length == 0:
		return 0, fmt.Errorf("truncated unsigned varint")
	case length < 0:
		return 0, fmt.Errorf("malformed unsigned varint")
	default:
		d.offset += length
		return value, nil
	}
}

func (d *binaryDecoder) integer() (int64, error) {
	value, length := binary.Varint(d.payload[d.offset:])
	switch {
	case length == 0:
		return 0, fmt.Errorf("truncated signed varint")
	case length < 0:
		return 0, fmt.Errorf("malformed signed varint")
	default:
		d.offset += length
		return value, nil
	}
}

func (d *binaryDecoder) bytes() ([]byte, error) {
	length, err := d.uint()
	if err != nil {
		return nil, err
	}
	if length > uint64(d.remaining()) {
		return nil, fmt.Errorf("length %d exceeds %d remaining bytes", length, d.remaining())
	}
	start := d.offset
	d.offset += int(length)
	return d.payload[start:d.offset], nil
}

func (d *binaryDecoder) string() (string, error) {
	value, err := d.bytes()
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func (d *binaryDecoder) tag(want byte, value reflect.Value) error {
	tag, err := d.byte()
	if err != nil {
		return d.wrap(value.Type().String(), err)
	}
	if tag != want {
		return d.fail(value.Type().String(), "unknown or invalid type tag %q; want %q", tag, want)
	}
	return nil
}

func (d *binaryDecoder) value(target reflect.Value, depth int) error {
	if depth > maxCanonicalDecodeDepth {
		return d.fail(target.Type().String(), "nesting exceeds limit %d", maxCanonicalDecodeDepth)
	}
	switch target.Kind() {
	case reflect.Pointer:
		if err := d.tag('p', target); err != nil {
			return err
		}
		marker, err := d.byte()
		if err != nil {
			return d.wrap(target.Type().String(), err)
		}
		switch marker {
		case 0:
			target.SetZero()
			return nil
		case 1:
			target.Set(reflect.New(target.Type().Elem()))
			return d.value(target.Elem(), depth+1)
		default:
			return d.fail(target.Type().String(), "invalid pointer presence marker %d", marker)
		}
	case reflect.Struct:
		if err := d.tag('s', target); err != nil {
			return err
		}
		name, err := d.string()
		if err != nil {
			return d.wrap(target.Type().String()+" type name", err)
		}
		if name != target.Type().Name() {
			return d.fail(target.Type().String(), "unknown struct type %q; want %q", name, target.Type().Name())
		}
		count, err := d.uint()
		if err != nil {
			return d.wrap(target.Type().String()+" field count", err)
		}
		fields := make([]string, target.NumField())
		for index := range fields {
			fields[index] = target.Type().Field(index).Name
		}
		if d.frozenProjection != nil {
			frozen, ok := d.frozenProjection[name]
			if !ok {
				return d.fail(target.Type().String(), "struct is outside the frozen v%d schema", d.canonicalVersion)
			}
			fields = frozen
		} else if d.canonicalVersion == 1 || d.canonicalVersion == 2 {
			projection := historicalV1StructFields
			if d.canonicalVersion == 2 {
				projection = historicalV2StructFields
			}
			frozen, ok := projection[name]
			if !ok {
				return d.fail(target.Type().String(), "struct is outside the frozen v1 schema")
			}
			fields = frozen
		}
		if count != uint64(len(fields)) {
			return d.fail(target.Type().String(), "unknown schema shape with %d fields; want %d", count, len(fields))
		}
		for _, fieldName := range fields {
			field, ok := target.Type().FieldByName(fieldName)
			if !ok {
				return d.fail(target.Type().String(), "retained v1 destination field %s is absent", fieldName)
			}
			if !field.IsExported() {
				return d.fail(target.Type().String(), "unexported destination field %s", field.Name)
			}
			name, err := d.string()
			if err != nil {
				return d.wrap(target.Type().String()+" field name", err)
			}
			if name != field.Name {
				return d.fail(target.Type().String(), "unknown or out-of-order field %q; want %q", name, field.Name)
			}
			if err := d.value(target.FieldByIndex(field.Index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if err := d.tag('l', target); err != nil {
			return err
		}
		length, err := d.uint()
		if err != nil {
			return d.wrap(target.Type().String()+" length", err)
		}
		// Every encoded value occupies at least its one-byte type tag. This
		// both bounds allocation and guarantees conversion to int is safe.
		if length > uint64(d.remaining()) {
			return d.fail(target.Type().String(), "length %d exceeds remaining encoded values", length)
		}
		target.Set(reflect.MakeSlice(target.Type(), int(length), int(length)))
		for index := 0; index < int(length); index++ {
			if err := d.value(target.Index(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		if err := d.tag('a', target); err != nil {
			return err
		}
		length, err := d.uint()
		if err != nil {
			return d.wrap(target.Type().String()+" length", err)
		}
		if length != uint64(target.Len()) {
			return d.fail(target.Type().String(), "array length %d; want %d", length, target.Len())
		}
		for index := 0; index < target.Len(); index++ {
			if err := d.value(target.Index(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		if err := d.tag('t', target); err != nil {
			return err
		}
		value, err := d.string()
		if err != nil {
			return d.wrap(target.Type().String(), err)
		}
		target.SetString(value)
		return nil
	case reflect.Bool:
		if err := d.tag('b', target); err != nil {
			return err
		}
		value, err := d.byte()
		if err != nil {
			return d.wrap(target.Type().String(), err)
		}
		if value > 1 {
			return d.fail(target.Type().String(), "invalid boolean value %d", value)
		}
		target.SetBool(value == 1)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if err := d.tag('u', target); err != nil {
			return err
		}
		value, err := d.uint()
		if err != nil {
			return d.wrap(target.Type().String(), err)
		}
		if target.OverflowUint(value) {
			return d.fail(target.Type().String(), "unsigned integer %d overflows destination", value)
		}
		target.SetUint(value)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if err := d.tag('i', target); err != nil {
			return err
		}
		value, err := d.integer()
		if err != nil {
			return d.wrap(target.Type().String(), err)
		}
		if target.OverflowInt(value) {
			return d.fail(target.Type().String(), "signed integer %d overflows destination", value)
		}
		target.SetInt(value)
		return nil
	default:
		return d.fail(target.Type().String(), "unsupported destination kind %s", target.Kind())
	}
}
