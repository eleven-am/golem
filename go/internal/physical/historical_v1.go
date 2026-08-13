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

// historicalV1SchemaShapeSHA256 freezes both the canonical field projection
// and the exact Go kinds/named types used to decode it. The v1 decoder checks
// this independently before touching caller-controlled bytes, so a later
// current-schema field type change cannot silently reinterpret released v1
// artifacts. A deliberate new historical decoder must use a new DTO/schema
// revision instead of updating this digest.
const historicalV1SchemaShapeSHA256 = "8caff75a0ecfe1bd23dec2f259c8a75f61ad32597ae96e127eba3f6c186dcc7f"

// historicalV1StructFields is the frozen canonical v1 schema projection. A
// future field added to current PhysicalSchema structs is neither decoded from
// nor encoded into v1 bytes; removing/renaming a retained field fails closed.
var historicalV1StructFields = map[string][]string{
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
	"PhysicalTable":         {"ID", "Name", "Columns", "PrimaryKey", "Uniques", "ForeignKeys", "Checks", "Indexes", "RequiredCapabilities"},
	"SemanticValue":         {"Kind", "Bool", "Integer", "String", "List"},
	"Attribute":             {"Name", "Value"},
	"Extension":             {"ID", "Provider", "Kind", "Version", "Owner", "Attributes", "RequiredCapabilities"},
	"SystemObject":          {"ID", "Kind", "Version", "Name", "Attributes", "RequiredCapabilities"},
	"SystemSchema":          {"Version", "Namespace", "Objects"},
	"UnmanagedObject":       {"Kind", "Name"},
	"PhysicalSchema":        {"Version", "CanonicalVersion", "Provider", "Namespace", "Tables", "Extensions", "System", "Unmanaged"},
}

var historicalV1StructTypes = map[string]reflect.Type{
	"Version":               reflect.TypeOf(Version{}),
	"DriverIdentity":        reflect.TypeOf(DriverIdentity{}),
	"CapabilityFact":        reflect.TypeOf(CapabilityFact{}),
	"ProviderManifest":      reflect.TypeOf(ProviderManifest{}),
	"Namespace":             reflect.TypeOf(Namespace{}),
	"ObjectRef":             reflect.TypeOf(ObjectRef{}),
	"CapabilityRequirement": reflect.TypeOf(CapabilityRequirement{}),
	"SemanticSymbol":        reflect.TypeOf(SemanticSymbol{}),
	"StorageType":           reflect.TypeOf(StorageType{}),
	"Expression":            reflect.TypeOf(Expression{}),
	"TypedLiteralIR":        reflect.TypeOf(ir.TypedLiteralIR{}),
	"PhysicalDefault":       reflect.TypeOf(PhysicalDefault{}),
	"GeneratedExpression":   reflect.TypeOf(GeneratedExpression{}),
	"PhysicalColumn":        reflect.TypeOf(PhysicalColumn{}),
	"PhysicalKey":           reflect.TypeOf(PhysicalKey{}),
	"PhysicalForeignKey":    reflect.TypeOf(PhysicalForeignKey{}),
	"PhysicalCheck":         reflect.TypeOf(PhysicalCheck{}),
	"IndexKey":              reflect.TypeOf(IndexKey{}),
	"PhysicalIndex":         reflect.TypeOf(PhysicalIndex{}),
	"PhysicalTable":         reflect.TypeOf(PhysicalTable{}),
	"SemanticValue":         reflect.TypeOf(SemanticValue{}),
	"Attribute":             reflect.TypeOf(Attribute{}),
	"Extension":             reflect.TypeOf(Extension{}),
	"SystemObject":          reflect.TypeOf(SystemObject{}),
	"SystemSchema":          reflect.TypeOf(SystemSchema{}),
	"UnmanagedObject":       reflect.TypeOf(UnmanagedObject{}),
	"PhysicalSchema":        reflect.TypeOf(PhysicalSchema{}),
}

func validateHistoricalV1SchemaShape() error {
	names := make([]string, 0, len(historicalV1StructFields))
	for name := range historicalV1StructFields {
		names = append(names, name)
	}
	sort.Strings(names)
	var descriptor strings.Builder
	for _, name := range names {
		typeOf, exists := historicalV1StructTypes[name]
		if !exists || typeOf.Kind() != reflect.Struct || typeOf.Name() != name {
			return fmt.Errorf("historical physical v1 schema type %s is unavailable", name)
		}
		descriptor.WriteString(name)
		descriptor.WriteByte('{')
		for _, fieldName := range historicalV1StructFields[name] {
			field, exists := typeOf.FieldByName(fieldName)
			if !exists || !field.IsExported() {
				return fmt.Errorf("historical physical v1 schema field %s.%s is unavailable", name, fieldName)
			}
			descriptor.WriteString(fieldName)
			descriptor.WriteByte(':')
			descriptor.WriteString(historicalV1TypeDescriptor(field.Type))
			descriptor.WriteByte(';')
		}
		descriptor.WriteString("}\n")
	}
	digest := sha256.Sum256([]byte(descriptor.String()))
	got := hex.EncodeToString(digest[:])
	if got != historicalV1SchemaShapeSHA256 {
		return fmt.Errorf("historical physical v1 schema shape changed: got %s", got)
	}
	return nil
}

func historicalV1TypeDescriptor(typeOf reflect.Type) string {
	switch typeOf.Kind() {
	case reflect.Pointer:
		return "*" + historicalV1TypeDescriptor(typeOf.Elem())
	case reflect.Slice:
		return "[]" + historicalV1TypeDescriptor(typeOf.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", typeOf.Len(), historicalV1TypeDescriptor(typeOf.Elem()))
	}
	if typeOf.Name() != "" {
		return typeOf.PkgPath() + "." + typeOf.Name() + "<" + typeOf.Kind().String() + ">"
	}
	return typeOf.Kind().String()
}

var historicalV1ClosedStrings = map[reflect.Type]map[string]struct{}{
	reflect.TypeOf(CapabilityVerification("")): stringSet("", "version_floor", "runtime_probe", "compile_probe", "extension"),
	reflect.TypeOf(StorageKind("")):            stringSet("", "sqlite.integer", "sqlite.real", "sqlite.text", "sqlite.blob", "postgresql.boolean", "postgresql.smallint", "postgresql.integer", "postgresql.bigint", "postgresql.real", "postgresql.double_precision", "postgresql.numeric", "postgresql.text", "postgresql.bytea", "postgresql.uuid", "postgresql.date", "postgresql.time", "postgresql.timestamptz", "postgresql.jsonb", "provider.extension"),
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

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func validateHistoricalV1ClosedValues(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return validateHistoricalV1ClosedValues(value.Elem())
	}
	if allowed, exists := historicalV1ClosedStrings[value.Type()]; exists {
		actual := value.String()
		if _, accepted := allowed[actual]; !accepted {
			if value.Type() == reflect.TypeOf(StorageKind("")) {
				return fmt.Errorf("historical physical v1 storage %q is outside the frozen vocabulary", actual)
			}
			return fmt.Errorf("historical physical v1 value %s(%q) is outside the frozen vocabulary", value.Type(), actual)
		}
		return nil
	}
	switch value.Kind() {
	case reflect.Struct:
		fields, frozen := historicalV1StructFields[value.Type().Name()]
		if !frozen {
			return fmt.Errorf("historical physical v1 value type %s is outside the frozen schema", value.Type())
		}
		if err := validateHistoricalV1ZeroOutsideFrozenFields(value, fields); err != nil {
			return err
		}
		for _, fieldName := range fields {
			if err := validateHistoricalV1ClosedValues(value.FieldByName(fieldName)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateHistoricalV1ClosedValues(value.Index(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateHistoricalV1ZeroOutsideFrozenFields(value reflect.Value, fields []string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	typeOf := value.Type()
	for index := 0; index < value.NumField(); index++ {
		field := typeOf.Field(index)
		if _, retained := allowed[field.Name]; retained {
			continue
		}
		// Future zero-valued fields do not change released bytes. Any nonzero
		// current-only fact would otherwise be silently ignored by the v1
		// projection and must fail closed before normalization/fingerprinting.
		if !value.Field(index).IsZero() {
			return fmt.Errorf("historical physical v1 %s.%s is outside the frozen schema and must be zero", typeOf.Name(), field.Name)
		}
	}
	return nil
}

// HistoricalV1MaxLengthCheckIdentity reproduces the released v1 PostgreSQL
// synthetic max-length check identity and name. Both v2 lowering (when it
// still needs the bytes check) and the v1->v2 migration proof share this sole
// owner so the historical representation cannot drift.
func HistoricalV1MaxLengthCheckIdentity(model ir.ModelID, field ir.FieldID) (ir.CheckID, PhysicalName) {
	hash := sha256.New()
	for _, part := range []string{"check", string(model), string(field), "max_length"} {
		hash.Write([]byte{0})
		hash.Write([]byte(part))
	}
	id := hex.EncodeToString(hash.Sum(nil)[:16])
	return ir.CheckID(id), PhysicalName("ck_max_length_" + id[:12])
}
