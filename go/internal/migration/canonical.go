package migration

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
)

func canonicalEntry(entry ManifestEntry) ([]byte, error) {
	entry.ChainHash = ""
	var out bytes.Buffer
	out.WriteString("golem:migration-entry:v1\x00")
	if err := encodeValue(&out, reflect.ValueOf(entry)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func encodeValue(out *bytes.Buffer, v reflect.Value) error {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			out.WriteByte(0)
			return nil
		}
		out.WriteByte(1)
		return encodeValue(out, v.Elem())
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := encodeValue(out, v.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if v.IsNil() {
			writeLength(out, 0)
			out.WriteByte(0)
			return nil
		}
		writeLength(out, uint64(v.Len()))
		out.WriteByte(1)
		for i := 0; i < v.Len(); i++ {
			if err := encodeValue(out, v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.String:
		s := v.String()
		writeLength(out, uint64(len(s)))
		out.WriteString(s)
	case reflect.Bool:
		if v.Bool() {
			out.WriteByte(1)
		} else {
			out.WriteByte(0)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		writeLength(out, v.Uint())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if err := binary.Write(out, binary.BigEndian, v.Int()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported canonical migration value kind %s", v.Kind())
	}
	return nil
}
func writeLength(out *bytes.Buffer, value uint64) {
	var data [10]byte
	n := binary.PutUvarint(data[:], value)
	out.Write(data[:n])
}
