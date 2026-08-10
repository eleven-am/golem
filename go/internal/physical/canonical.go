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
	if value == nil {
		return nil, fmt.Errorf("physical canonical fragment: nil root")
	}
	return canonicalValue(value)
}

func canonicalValue(value any) ([]byte, error) {
	var buffer bytes.Buffer
	writeBytes(&buffer, canonicalMagic)
	writeUint(&buffer, uint64(CanonicalFormatVersion))
	encoder := binaryEncoder{buffer: &buffer}
	if err := encoder.value(reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

type binaryEncoder struct {
	buffer *bytes.Buffer
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
		writeUint(e.buffer, uint64(value.NumField()))
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !field.IsExported() {
				return fmt.Errorf("physical canonical encoding: unexported field %s.%s", value.Type().Name(), field.Name)
			}
			writeString(e.buffer, field.Name)
			if err := e.value(value.Field(index)); err != nil {
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
