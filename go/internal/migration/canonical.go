package migration

// Frozen migration-entry-v1 canonical encoder adapted from the original Go
// migration publication source at commit
// 6babdef35497aea4cd968d3260587ded05d117c0. The source canonical.go was 73
// lines with SHA-256 97bf7a3e7727bc19410f595e5fb05c675fb817e90e2f429f30c750d0b10ab291.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	historicalMigrationEntryV1UpstreamCommit = "6babdef35497aea4cd968d3260587ded05d117c0"
	historicalMigrationEntryV1UpstreamSHA256 = "97bf7a3e7727bc19410f595e5fb05c675fb817e90e2f429f30c750d0b10ab291"
	historicalMigrationEntryV1UpstreamLines  = 73

	// This digest pins only the selected v1 fields and their exact Go type
	// descriptors. A later zero-valued field may be ignored without rewriting
	// released history; a selected field removal or type change fails closed.
	historicalMigrationEntryV1ShapeSHA256 = "42609befb47303a8638898101191675ede74db593ae84db2874af3c8afe8f56c"
)

var (
	physicalSchemaType = reflect.TypeOf(physical.PhysicalSchema{})

	historicalMigrationEntryV1StructFields = map[reflect.Type][]string{
		reflect.TypeOf(ManifestEntry{}): {
			"ID", "ParentID", "ParentChainHash", "ChainHash", "Files", "Manual", "Operations", "Phases", "Risks", "Approvals",
			"BeforeModel", "AfterModel", "BeforePhysical", "AfterPhysical", "BeforeSnapshot", "AfterSnapshot", "UnmanagedAllowlistDigest",
		},
		reflect.TypeOf(FileChecksum{}):    {"Path", "SHA256"},
		reflect.TypeOf(ManualCompanion{}): {"OperationID", "File", "Postcondition"},
		reflect.TypeOf(Operation{}): {
			"ID", "Kind", "Stage", "ObjectID", "Before", "After", "Dependencies", "Capabilities", "Mode", "Risk", "Transform", "Resume", "LogicalPath",
		},
		reflect.TypeOf(DataTransform{}):  {"Kind", "Input"},
		reflect.TypeOf(ResumeMetadata{}): {"DetectionKind", "ExpectedFingerprint"},
		reflect.TypeOf(Phase{}):          {"Ordinal", "Mode", "Operations", "BeforeFingerprint", "AfterFingerprint"},
		reflect.TypeOf(OperationRisk{}):  {"OperationID", "Risk"},
		reflect.TypeOf(Approval{}):       {"OperationID", "Risk", "Before", "After"},
	}
)

func canonicalEntry(entry ManifestEntry) ([]byte, error) {
	if err := validateHistoricalMigrationEntryV1Shape(historicalMigrationEntryV1StructFields, historicalMigrationEntryV1ShapeSHA256); err != nil {
		return nil, err
	}
	entry.ChainHash = ""
	var out bytes.Buffer
	out.WriteString("golem:migration-entry:v1\x00")
	if err := encodeHistoricalMigrationEntryV1Value(&out, reflect.ValueOf(entry), historicalMigrationEntryV1StructFields); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func validateHistoricalMigrationEntryV1Shape(projection map[reflect.Type][]string, want string) error {
	types := make([]reflect.Type, 0, len(projection))
	for typeOf := range projection {
		if typeOf.Kind() != reflect.Struct || typeOf.Name() == "" {
			return fmt.Errorf("historical migration entry v1 projection type %s is unavailable", typeOf)
		}
		types = append(types, typeOf)
	}
	sort.Slice(types, func(left, right int) bool {
		return historicalMigrationEntryV1TypeName(types[left]) < historicalMigrationEntryV1TypeName(types[right])
	})
	var descriptor strings.Builder
	for _, typeOf := range types {
		descriptor.WriteString(historicalMigrationEntryV1TypeName(typeOf))
		descriptor.WriteByte('{')
		for _, fieldName := range projection[typeOf] {
			field, exists := typeOf.FieldByName(fieldName)
			if !exists || !field.IsExported() {
				return fmt.Errorf("historical migration entry v1 field %s.%s is unavailable", typeOf.Name(), fieldName)
			}
			descriptor.WriteString(fieldName)
			descriptor.WriteByte(':')
			descriptor.WriteString(historicalMigrationEntryV1TypeDescriptor(field.Type))
			descriptor.WriteByte(';')
		}
		descriptor.WriteString("}\n")
	}
	digest := sha256.Sum256([]byte(descriptor.String()))
	if got := hex.EncodeToString(digest[:]); got != want {
		return fmt.Errorf("historical migration entry v1 shape changed: got %s", got)
	}
	return nil
}

func historicalMigrationEntryV1TypeName(typeOf reflect.Type) string {
	return typeOf.PkgPath() + "." + typeOf.Name()
}

func historicalMigrationEntryV1TypeDescriptor(typeOf reflect.Type) string {
	switch typeOf.Kind() {
	case reflect.Pointer:
		return "*" + historicalMigrationEntryV1TypeDescriptor(typeOf.Elem())
	case reflect.Slice:
		return "[]" + historicalMigrationEntryV1TypeDescriptor(typeOf.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", typeOf.Len(), historicalMigrationEntryV1TypeDescriptor(typeOf.Elem()))
	}
	if typeOf.Name() != "" {
		return typeOf.PkgPath() + "." + typeOf.Name() + "<" + typeOf.Kind().String() + ">"
	}
	return typeOf.Kind().String()
}

func encodeHistoricalMigrationEntryV1Value(out *bytes.Buffer, value reflect.Value, projection map[reflect.Type][]string) error {
	if value.IsValid() && value.Type() == physicalSchemaType {
		schema := value.Interface().(physical.PhysicalSchema)
		normalized, err := physical.NormalizeHistorical(schema)
		if err != nil {
			return fmt.Errorf("historical migration entry v1 physical snapshot: %w", err)
		}
		physicalProjection, err := physical.HistoricalStructFieldProjection(normalized)
		if err != nil {
			return err
		}
		return encodeProjectedPhysicalValue(out, value, physicalProjection)
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			out.WriteByte(0)
			return nil
		}
		out.WriteByte(1)
		return encodeHistoricalMigrationEntryV1Value(out, value.Elem(), projection)
	}
	switch value.Kind() {
	case reflect.Struct:
		fields, exists := projection[value.Type()]
		if !exists {
			return fmt.Errorf("historical migration entry v1 projection is missing struct %s", value.Type())
		}
		if err := validateHistoricalMigrationEntryV1ZeroOutsideProjection(value, fields); err != nil {
			return err
		}
		for _, fieldName := range fields {
			field, exists := value.Type().FieldByName(fieldName)
			if !exists || !field.IsExported() {
				return fmt.Errorf("historical migration entry v1 field %s.%s is unavailable", value.Type().Name(), fieldName)
			}
			if err := encodeHistoricalMigrationEntryV1Value(out, value.FieldByIndex(field.Index), projection); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			writeLength(out, 0)
			out.WriteByte(0)
			return nil
		}
		writeLength(out, uint64(value.Len()))
		out.WriteByte(1)
		for index := 0; index < value.Len(); index++ {
			if err := encodeHistoricalMigrationEntryV1Value(out, value.Index(index), projection); err != nil {
				return err
			}
		}
	case reflect.String:
		text := value.String()
		writeLength(out, uint64(len(text)))
		out.WriteString(text)
	case reflect.Bool:
		if value.Bool() {
			out.WriteByte(1)
		} else {
			out.WriteByte(0)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		writeLength(out, value.Uint())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if err := binary.Write(out, binary.BigEndian, value.Int()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported historical migration entry v1 value kind %s", value.Kind())
	}
	return nil
}

func validateHistoricalMigrationEntryV1ZeroOutsideProjection(value reflect.Value, fields []string) error {
	selected := make(map[string]struct{}, len(fields))
	for _, fieldName := range fields {
		selected[fieldName] = struct{}{}
	}
	for index := 0; index < value.NumField(); index++ {
		field := value.Type().Field(index)
		if _, exists := selected[field.Name]; !exists && !value.Field(index).IsZero() {
			return fmt.Errorf("historical migration entry v1 %s.%s is outside the frozen projection and must be zero", value.Type().Name(), field.Name)
		}
	}
	return nil
}

func encodeProjectedPhysicalValue(out *bytes.Buffer, value reflect.Value, projection map[string][]string) error {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			out.WriteByte(0)
			return nil
		}
		out.WriteByte(1)
		return encodeProjectedPhysicalValue(out, value.Elem(), projection)
	}
	switch value.Kind() {
	case reflect.Struct:
		fields, exists := projection[value.Type().Name()]
		if !exists {
			return fmt.Errorf("historical physical canonical projection is missing struct %s", value.Type())
		}
		for _, fieldName := range fields {
			field, exists := value.Type().FieldByName(fieldName)
			if !exists || !field.IsExported() {
				return fmt.Errorf("historical physical canonical field %s.%s is unavailable", value.Type().Name(), fieldName)
			}
			if err := encodeProjectedPhysicalValue(out, value.FieldByIndex(field.Index), projection); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			writeLength(out, 0)
			out.WriteByte(0)
			return nil
		}
		writeLength(out, uint64(value.Len()))
		out.WriteByte(1)
		for index := 0; index < value.Len(); index++ {
			if err := encodeProjectedPhysicalValue(out, value.Index(index), projection); err != nil {
				return err
			}
		}
	case reflect.String:
		text := value.String()
		writeLength(out, uint64(len(text)))
		out.WriteString(text)
	case reflect.Bool:
		if value.Bool() {
			out.WriteByte(1)
		} else {
			out.WriteByte(0)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		writeLength(out, value.Uint())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if err := binary.Write(out, binary.BigEndian, value.Int()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported historical physical canonical value kind %s", value.Kind())
	}
	return nil
}

func writeLength(out *bytes.Buffer, value uint64) {
	var data [10]byte
	n := binary.PutUvarint(data[:], value)
	out.Write(data[:n])
}
