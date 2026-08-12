package physical

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
)

var historicalJSONFieldNames = map[string]map[string]string{
	"TypedLiteralIR": {"Kind": "kind", "Canonical": "canonical"},
}

// MarshalHistoricalSnapshotJSON reproduces the exact JSON struct projection
// owned by a retained physical format. It is intentionally separate from the
// current-schema JSON encoder: zero-valued fields added to mutable current Go
// structs must never appear in immutable reviewed migration snapshots.
func MarshalHistoricalSnapshotJSON(schema PhysicalSchema) ([]byte, error) {
	normalized, err := NormalizeHistorical(schema)
	if err != nil {
		return nil, err
	}
	projection, err := historicalStructFieldProjection(normalized)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := writeHistoricalJSONValue(&compact, reflect.ValueOf(normalized), projection); err != nil {
		return nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact.Bytes(), "", "  "); err != nil {
		return nil, fmt.Errorf("indent historical physical snapshot: %w", err)
	}
	indented.WriteByte('\n')
	return indented.Bytes(), nil
}

func historicalStructFieldProjection(schema PhysicalSchema) (map[string][]string, error) {
	switch {
	case schema.Version == 1 && schema.CanonicalVersion == 1:
		return historicalV1StructFields, nil
	case schema.Version == 2 && schema.CanonicalVersion == 2:
		return historicalV2StructFields, nil
	case schema.Version == 3 && schema.CanonicalVersion == 3:
		return historicalV3StructFields, nil
	default:
		return nil, fmt.Errorf("unsupported historical physical format/canonical versions %d/%d", schema.Version, schema.CanonicalVersion)
	}
}

// HistoricalStructFieldProjection returns a detached copy of the exact struct
// field projection owned by a retained physical schema version. Migration's
// canonical entry-v1 encoder uses the same authority as snapshot JSON, so the
// two immutable representations cannot drift when current Go structs grow.
func HistoricalStructFieldProjection(schema PhysicalSchema) (map[string][]string, error) {
	projection, err := historicalStructFieldProjection(schema)
	if err != nil {
		return nil, err
	}
	detached := make(map[string][]string, len(projection))
	for name, fields := range projection {
		detached[name] = append([]string(nil), fields...)
	}
	return detached, nil
}

func writeHistoricalJSONValue(buffer *bytes.Buffer, value reflect.Value, projection map[string][]string) error {
	if !value.IsValid() {
		buffer.WriteString("null")
		return nil
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Struct:
		fields, exists := projection[value.Type().Name()]
		if !exists {
			return fmt.Errorf("historical physical JSON projection is missing struct %s", value.Type())
		}
		buffer.WriteByte('{')
		for index, fieldName := range fields {
			field, exists := value.Type().FieldByName(fieldName)
			if !exists || !field.IsExported() {
				return fmt.Errorf("historical physical JSON field %s.%s is unavailable", value.Type().Name(), fieldName)
			}
			if index != 0 {
				buffer.WriteByte(',')
			}
			name := field.Name
			if names := historicalJSONFieldNames[value.Type().Name()]; names != nil {
				frozen, exists := names[fieldName]
				if !exists {
					return fmt.Errorf("historical physical JSON name %s.%s is unavailable", value.Type().Name(), fieldName)
				}
				name = frozen
			}
			encodedName, _ := json.Marshal(name)
			buffer.Write(encodedName)
			buffer.WriteByte(':')
			if err := writeHistoricalJSONValue(buffer, value.FieldByIndex(field.Index), projection); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
		return nil
	case reflect.Slice:
		if value.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			encoded, err := json.Marshal(value.Interface())
			if err != nil {
				return err
			}
			buffer.Write(encoded)
			return nil
		}
		fallthrough
	case reflect.Array:
		buffer.WriteByte('[')
		for index := 0; index < value.Len(); index++ {
			if index != 0 {
				buffer.WriteByte(',')
			}
			if err := writeHistoricalJSONValue(buffer, value.Index(index), projection); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
		return nil
	case reflect.String:
		encoded, _ := json.Marshal(value.String())
		buffer.Write(encoded)
		return nil
	case reflect.Bool:
		buffer.WriteString(strconv.FormatBool(value.Bool()))
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		buffer.WriteString(strconv.FormatInt(value.Int(), 10))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		buffer.WriteString(strconv.FormatUint(value.Uint(), 10))
		return nil
	case reflect.Float32, reflect.Float64:
		encoded, err := json.Marshal(value.Interface())
		if err != nil {
			return err
		}
		buffer.Write(encoded)
		return nil
	default:
		return fmt.Errorf("historical physical JSON type %s is unsupported", value.Type())
	}
}
