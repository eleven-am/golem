package physical

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// The selected v3 projection is independently owned so later current-format
// fields cannot silently enter released v3 bytes.
const historicalV3SchemaShapeSHA256 = "b81a75331ae755dda8eaf5f9d2e33bb889cc7b10c39bc2102c734b7a42431c11"

// LatestFrozenPhysicalFormatVersion is the highest physical format safe to
// advertise publicly. It advances only after the retained v3
// normalize/validate/canonical/fingerprint profile is independently frozen.
const LatestFrozenPhysicalFormatVersion uint16 = 3

var historicalV3StructFields = map[string][]string{
	"Version":               {"Major", "Minor", "Patch"},
	"DriverIdentity":        {"Module", "Adapter"},
	"CapabilityFact":        {"ID", "Version", "Verification"},
	"ProviderManifest":      {"Provider", "Driver", "MinimumVersion", "Capabilities"},
	"Namespace":             {"Name"},
	"ObjectRef":             {"Kind", "ModelID", "FieldID", "ObjectID"},
	"CapabilityRequirement": {"Capability", "Owner"},
	"SemanticSymbol":        {"Identity", "Kind", "Version", "Provider"},
	"StorageType":           {"Kind", "Precision", "Scale", "Length", "Symbol"},
	"Expression":            {"Kind", "Type", "Nullable", "Symbol", "Column", "Literal", "Operands"},
	"TypedLiteralIR":        {"Kind", "Canonical"},
	"PhysicalDefault":       {"Kind", "Literal", "Expression"},
	"GeneratedExpression":   {"Kind", "Expression"},
	"PhysicalColumn":        {"ID", "Name", "Ordinal", "Storage", "Nullable", "Default", "Generated", "Collation", "RequiredCapabilities"},
	"PhysicalKey":           {"ID", "Name", "Columns", "RequiredCapabilities"},
	"PhysicalForeignKey":    {"ID", "Name", "Columns", "ReferencedTable", "ReferencedColumns", "OnUpdate", "OnDelete", "Deferrable", "RequiredCapabilities"},
	"PhysicalCheck":         {"ID", "Name", "Expression", "RequiredCapabilities"},
	"IndexKey":              {"Column", "Expression", "Direction", "Nulls", "Collation", "OpClass"},
	"PhysicalIndex":         {"ID", "Name", "Unique", "Method", "Keys", "Include", "Predicate", "CreationMode", "RequiredCapabilities"},
	"PhysicalTable":         {"ID", "Name", "OptimisticConcurrency", "Columns", "PrimaryKey", "Uniques", "ForeignKeys", "Checks", "Indexes", "RequiredCapabilities"},
	"SemanticValue":         {"Kind", "Bool", "Integer", "String", "List"},
	"Attribute":             {"Name", "Value"},
	"Extension":             {"ID", "Provider", "Kind", "Version", "Owner", "Attributes", "RequiredCapabilities"},
	"SystemObject":          {"ID", "Kind", "Version", "Name", "Attributes", "RequiredCapabilities"},
	"SystemSchema":          {"Version", "Namespace", "Objects"},
	"UnmanagedObject":       {"Kind", "Name"},
	"PhysicalSchema":        {"Version", "CanonicalVersion", "Provider", "Namespace", "Tables", "Extensions", "System", "Unmanaged"},
}

var historicalV3StructTypes = map[string]reflect.Type{
	"Version": reflect.TypeOf(Version{}), "DriverIdentity": reflect.TypeOf(DriverIdentity{}),
	"CapabilityFact": reflect.TypeOf(CapabilityFact{}), "ProviderManifest": reflect.TypeOf(ProviderManifest{}),
	"Namespace": reflect.TypeOf(Namespace{}), "ObjectRef": reflect.TypeOf(ObjectRef{}),
	"CapabilityRequirement": reflect.TypeOf(CapabilityRequirement{}), "SemanticSymbol": reflect.TypeOf(SemanticSymbol{}),
	"StorageType": reflect.TypeOf(StorageType{}), "Expression": reflect.TypeOf(Expression{}),
	"TypedLiteralIR": reflect.TypeOf(ir.TypedLiteralIR{}), "PhysicalDefault": reflect.TypeOf(PhysicalDefault{}),
	"GeneratedExpression": reflect.TypeOf(GeneratedExpression{}), "PhysicalColumn": reflect.TypeOf(PhysicalColumn{}),
	"PhysicalKey": reflect.TypeOf(PhysicalKey{}), "PhysicalForeignKey": reflect.TypeOf(PhysicalForeignKey{}),
	"PhysicalCheck": reflect.TypeOf(PhysicalCheck{}), "IndexKey": reflect.TypeOf(IndexKey{}),
	"PhysicalIndex": reflect.TypeOf(PhysicalIndex{}), "PhysicalTable": reflect.TypeOf(PhysicalTable{}),
	"SemanticValue": reflect.TypeOf(SemanticValue{}), "Attribute": reflect.TypeOf(Attribute{}),
	"Extension": reflect.TypeOf(Extension{}), "SystemObject": reflect.TypeOf(SystemObject{}),
	"SystemSchema": reflect.TypeOf(SystemSchema{}), "UnmanagedObject": reflect.TypeOf(UnmanagedObject{}),
	"PhysicalSchema": reflect.TypeOf(PhysicalSchema{}),
}

func validateHistoricalV3SchemaShape() error {
	names := make([]string, 0, len(historicalV3StructFields))
	for name := range historicalV3StructFields {
		names = append(names, name)
	}
	sort.Strings(names)
	var descriptor strings.Builder
	for _, name := range names {
		typeOf, exists := historicalV3StructTypes[name]
		if !exists || typeOf.Kind() != reflect.Struct || typeOf.Name() != name {
			return fmt.Errorf("historical physical v3 schema type %s is unavailable", name)
		}
		descriptor.WriteString(name)
		descriptor.WriteByte('{')
		for _, fieldName := range historicalV3StructFields[name] {
			field, exists := typeOf.FieldByName(fieldName)
			if !exists || !field.IsExported() {
				return fmt.Errorf("historical physical v3 schema field %s.%s is unavailable", name, fieldName)
			}
			descriptor.WriteString(fieldName)
			descriptor.WriteByte(':')
			descriptor.WriteString(historicalV1TypeDescriptor(field.Type))
			descriptor.WriteByte(';')
		}
		descriptor.WriteString("}\n")
	}
	digest := sha256.Sum256([]byte(descriptor.String()))
	if got := hex.EncodeToString(digest[:]); got != historicalV3SchemaShapeSHA256 {
		return fmt.Errorf("historical physical v3 schema shape changed: got %s", got)
	}
	return nil
}

var historicalV3ClosedStrings = map[reflect.Type]map[string]struct{}{
	reflect.TypeOf(CapabilityVerification("")): stringSet("", "version_floor", "runtime_probe", "compile_probe", "extension"),
	reflect.TypeOf(StorageKind("")):            stringSet("", "sqlite.integer", "sqlite.real", "sqlite.text", "sqlite.blob", "postgresql.boolean", "postgresql.smallint", "postgresql.integer", "postgresql.bigint", "postgresql.real", "postgresql.double_precision", "postgresql.numeric", "postgresql.varchar", "postgresql.text", "postgresql.bytea", "postgresql.uuid", "postgresql.date", "postgresql.time", "postgresql.timestamptz", "postgresql.jsonb", "provider.extension"),
	reflect.TypeOf(ExpressionKind("")):         stringSet("", "column", "literal", "operator", "function", "cast"),
	reflect.TypeOf(DefaultKind("")):            stringSet("", "none", "literal", "provider"),
	reflect.TypeOf(GeneratedKind("")):          stringSet("", "stored", "virtual"),
	reflect.TypeOf(IndexMethod("")):            stringSet("", "btree", "hash", "gin", "gist", "brin"),
	reflect.TypeOf(IndexCreationMode("")):      stringSet("", "transactional", "autocommit_only"),
	reflect.TypeOf(SemanticValueKind("")):      stringSet("", "bool", "integer", "string", "id", "list"),
	reflect.TypeOf(SystemObjectKind("")):       stringSet("", "migration_ledger", "migration_lock", "outbox", "outbox_delivery", "upsert_guard"),
	reflect.TypeOf(ir.Provider("")):            stringSet("", "sqlite", "postgresql"),
	reflect.TypeOf(ir.ObjectKind("")):          stringSet("", "schema", "model", "field", "relation", "enum", "enum-value", "key", "index", "check", "foreign-key", "extension"),
	reflect.TypeOf(ir.SchemaSymbolKind("")):    stringSet("", "operator", "function", "cast"),
	reflect.TypeOf(ir.ProviderScope("")):       stringSet("", "portable", "sqlite", "postgresql"),
	reflect.TypeOf(ir.LiteralKind("")):         stringSet("", "bool", "integer", "float", "decimal", "string", "bytes", "uuid", "date", "time", "dateTime", "json", "enum", "list"),
	reflect.TypeOf(ir.SortDirection("")):       stringSet("", "asc", "desc"),
	reflect.TypeOf(ir.NullsOrder("")):          stringSet("", "default", "first", "last"),
	reflect.TypeOf(ir.ReferentialAction("")):   stringSet("", "noAction", "restrict", "cascade", "setNull", "setDefault"),
	reflect.TypeOf(ir.Deferrability("")):       stringSet("", "notDeferrable", "initiallyImmediate", "initiallyDeferred"),
}

func validateHistoricalV3ClosedValues(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return validateHistoricalV3ClosedValues(value.Elem())
	}
	if allowed, exists := historicalV3ClosedStrings[value.Type()]; exists {
		if _, accepted := allowed[value.String()]; !accepted {
			return fmt.Errorf("historical physical v3 value %s(%q) is outside the frozen vocabulary", value.Type(), value.String())
		}
		return nil
	}
	switch value.Kind() {
	case reflect.Struct:
		fields, frozen := historicalV3StructFields[value.Type().Name()]
		if !frozen {
			return fmt.Errorf("historical physical v3 value type %s is outside the frozen schema", value.Type())
		}
		if err := validateHistoricalV3ZeroOutsideFrozenFields(value, fields); err != nil {
			return err
		}
		for _, fieldName := range fields {
			if err := validateHistoricalV3ClosedValues(value.FieldByName(fieldName)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateHistoricalV3ClosedValues(value.Index(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateHistoricalV3ZeroOutsideFrozenFields(value reflect.Value, fields []string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for index := 0; index < value.NumField(); index++ {
		field := value.Type().Field(index)
		if _, retained := allowed[field.Name]; !retained && !value.Field(index).IsZero() {
			return fmt.Errorf("historical physical v3 %s.%s is outside the frozen schema and must be zero", value.Type().Name(), field.Name)
		}
	}
	return nil
}

func validateHistoricalV3(schema PhysicalSchema) error {
	if schema.Version != 3 || schema.CanonicalVersion != 3 {
		return fmt.Errorf("historical physical schema is not v3/v3")
	}
	if err := validateHistoricalV3SchemaShape(); err != nil {
		return err
	}
	if err := validateHistoricalV3ClosedValues(reflect.ValueOf(schema)); err != nil {
		return err
	}
	return validateHistoricalV3Tagged(schema)
}
