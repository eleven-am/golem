package physical

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
)

var canonicalMagic = []byte("golem:physical:canonical")

// CanonicalEncode validates, normalizes, and length-delimited encodes the whole
// physical schema. It does not use JSON, maps, source order, or rendered DDL.
func CanonicalEncode(schema PhysicalSchema) ([]byte, error) {
	normalized, err := Normalize(schema)
	if err != nil {
		return nil, err
	}
	return canonicalValue(normalized)
}

// CanonicalFragment encodes a detached physical semantic fragment with the
// same versioned binary algebra as CanonicalEncode. Callers must first validate
// the owning whole schema because a detached FK or expression cannot prove
// cross-object references independently.
func CanonicalFragment(value any) ([]byte, error) {
	return CanonicalFragmentVersion(value, CanonicalFormatVersion)
}

// CanonicalFragmentVersion exists solely so immutable v1 and v2 migration
// histories can reproduce their original operation digests. New authoring
// must call CanonicalFragment.
func CanonicalFragmentVersion(value any, version uint32) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("physical canonical fragment: nil root")
	}
	if version == 2 && CanonicalFormatVersion != 2 {
		return HistoricalV2CanonicalFragment(value)
	}
	if version != CanonicalFormatVersion && version != 1 {
		return nil, fmt.Errorf("physical canonical fragment version %d is unsupported", version)
	}
	return canonicalValueVersion(value, version)
}

// HistoricalV1CanonicalFragment reproduces the released v1 fragment encoding
// for the retained v1 migration planner only.
func HistoricalV1CanonicalFragment(value any) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("historical physical v1 canonical fragment: nil root")
	}
	return canonicalValueVersion(value, 1)
}

// HistoricalV2CanonicalFragment reproduces the immutable v2 fragment algebra.
func HistoricalV2CanonicalFragment(value any) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("historical physical v2 canonical fragment: nil root")
	}
	if err := validateHistoricalV2SchemaShape(); err != nil {
		return nil, err
	}
	if err := validateHistoricalV2ClosedValues(reflect.ValueOf(value)); err != nil {
		return nil, fmt.Errorf("historical physical v2 canonical fragment: %w", err)
	}
	return canonicalValueVersion(value, 2)
}

// CanonicalEncodeHistoricalV2 validates, normalizes, and encodes only a
// frozen v2 physical snapshot. Current-only fields must be zero.
func CanonicalEncodeHistoricalV2(schema PhysicalSchema) ([]byte, error) {
	normalized, err := NormalizeHistoricalV2(schema)
	if err != nil {
		return nil, err
	}
	return canonicalValueVersion(normalized, 2)
}

// HistoricalV3CanonicalFragment reproduces the released v3 fragment algebra
// through the frozen v3 projection even while v3 is also the current format.
func HistoricalV3CanonicalFragment(value any) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("historical physical v3 canonical fragment: nil root")
	}
	if err := validateHistoricalV3SchemaShape(); err != nil {
		return nil, err
	}
	if err := validateHistoricalV3ClosedValues(reflect.ValueOf(value)); err != nil {
		return nil, fmt.Errorf("historical physical v3 canonical fragment: %w", err)
	}
	return canonicalHistoricalValueVersion(value, 3, historicalV3StructFields)
}

// CanonicalEncodeHistoricalV3 validates, normalizes, and encodes only the
// independently frozen v3 physical snapshot.
func CanonicalEncodeHistoricalV3(schema PhysicalSchema) ([]byte, error) {
	normalized, err := NormalizeHistoricalV3(schema)
	if err != nil {
		return nil, err
	}
	return canonicalHistoricalValueVersion(normalized, 3, historicalV3StructFields)
}

func canonicalValue(value any) ([]byte, error) {
	return canonicalValueVersion(value, CanonicalFormatVersion)
}

func canonicalValueVersion(value any, version uint32) ([]byte, error) {
	return canonicalHistoricalValueVersion(value, version, nil)
}

func canonicalHistoricalValueVersion(value any, version uint32, projection map[string][]string) ([]byte, error) {
	var buffer bytes.Buffer
	writeBytes(&buffer, canonicalMagic)
	writeUint(&buffer, uint64(version))
	encoder := binaryEncoder{buffer: &buffer, canonicalVersion: version, frozenProjection: projection}
	if err := encoder.value(reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

type binaryEncoder struct {
	buffer           *bytes.Buffer
	canonicalVersion uint32
	frozenProjection map[string][]string
}

func (e binaryEncoder) value(value reflect.Value) error {
	if !value.IsValid() {
		e.buffer.WriteByte(0)
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			e.buffer.WriteByte(0)
			return nil
		}
		return e.value(value.Elem())
	}
	switch value.Kind() {
	case reflect.Pointer:
		e.buffer.WriteByte('p')
		if value.IsNil() {
			e.buffer.WriteByte(0)
			return nil
		}
		e.buffer.WriteByte(1)
		return e.value(value.Elem())
	case reflect.Struct:
		e.buffer.WriteByte('s')
		writeString(e.buffer, value.Type().Name())
		fields := make([]string, value.NumField())
		for index := range fields {
			fields[index] = value.Type().Field(index).Name
		}
		if e.frozenProjection != nil && value.Type().Name() != "" {
			frozen, ok := e.frozenProjection[value.Type().Name()]
			if !ok {
				return fmt.Errorf("physical canonical v%d encoding: struct %s is outside the frozen schema", e.canonicalVersion, value.Type().Name())
			}
			fields = frozen
		} else if (e.canonicalVersion == 1 || e.canonicalVersion == 2) && value.Type().Name() != "" {
			projection := historicalV1StructFields
			if e.canonicalVersion == 2 {
				projection = historicalV2StructFields
			}
			frozen, ok := projection[value.Type().Name()]
			if !ok {
				return fmt.Errorf("physical canonical v1 encoding: struct %s is outside the frozen schema", value.Type().Name())
			}
			fields = frozen
		}
		writeUint(e.buffer, uint64(len(fields)))
		for _, fieldName := range fields {
			field, ok := value.Type().FieldByName(fieldName)
			if !ok {
				return fmt.Errorf("physical canonical v1 encoding: retained field %s.%s is absent", value.Type().Name(), fieldName)
			}
			if !field.IsExported() {
				return fmt.Errorf("physical canonical encoding: unexported field %s.%s", value.Type().Name(), field.Name)
			}
			writeString(e.buffer, field.Name)
			if err := e.value(value.FieldByIndex(field.Index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		e.buffer.WriteByte('l')
		writeUint(e.buffer, uint64(value.Len()))
		for index := 0; index < value.Len(); index++ {
			if err := e.value(value.Index(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		e.buffer.WriteByte('a')
		writeUint(e.buffer, uint64(value.Len()))
		for index := 0; index < value.Len(); index++ {
			if err := e.value(value.Index(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		e.buffer.WriteByte('t')
		writeString(e.buffer, value.String())
		return nil
	case reflect.Bool:
		e.buffer.WriteByte('b')
		if value.Bool() {
			e.buffer.WriteByte(1)
		} else {
			e.buffer.WriteByte(0)
		}
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		e.buffer.WriteByte('u')
		writeUint(e.buffer, value.Uint())
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.buffer.WriteByte('i')
		writeInt(e.buffer, value.Int())
		return nil
	default:
		return fmt.Errorf("physical canonical encoding: unsupported kind %s", value.Kind())
	}
}

func writeBytes(buffer *bytes.Buffer, value []byte) {
	writeUint(buffer, uint64(len(value)))
	buffer.Write(value)
}

func writeString(buffer *bytes.Buffer, value string) {
	writeBytes(buffer, []byte(value))
}

func writeUint(buffer *bytes.Buffer, value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	length := binary.PutUvarint(encoded[:], value)
	buffer.Write(encoded[:length])
}

func writeInt(buffer *bytes.Buffer, value int64) {
	var encoded [binary.MaxVarintLen64]byte
	length := binary.PutVarint(encoded[:], value)
	buffer.Write(encoded[:length])
}
