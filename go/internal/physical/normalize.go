package physical

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// Normalize returns a deeply detached schema in canonical collection order and
// validates the complete semantic graph. Ordered components (key/index/FK
// columns and expression operands) retain their authored order.
func Normalize(schema PhysicalSchema) (PhysicalSchema, error) {
	normalized := normalizeCollections(schema)
	if err := Validate(normalized); err != nil {
		return PhysicalSchema{}, err
	}
	return normalized, nil
}

// NormalizeHistorical normalizes the explicitly retained reviewed-history
// profiles. The original v1 decoder remains stricter: reviewed history also
// contains the sealed v1 SQLite runtime transition whose after-snapshot names
// the current ncruces driver. Callers must not use this function for active
// generated provider documents.
func NormalizeHistorical(schema PhysicalSchema) (PhysicalSchema, error) {
	if schema.Version == 3 && schema.CanonicalVersion == 3 {
		return NormalizeHistoricalV3(schema)
	}
	if schema.Version != 1 || schema.CanonicalVersion != 1 {
		if schema.Version == 2 && schema.CanonicalVersion == 2 {
			return NormalizeHistoricalV2(schema)
		}
		return PhysicalSchema{}, fmt.Errorf("unsupported historical physical format/canonical versions %d/%d", schema.Version, schema.CanonicalVersion)
	}
	if schema.Provider.Provider == ir.SQLite && schema.Provider.Driver == (DriverIdentity{Module: "github.com/ncruces/go-sqlite3", Adapter: "sqlx"}) {
		reviewed := schema
		reviewed.Provider.Driver = DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
		normalized, err := NormalizeHistoricalV1(reviewed)
		if err != nil {
			return PhysicalSchema{}, err
		}
		normalized.Provider.Driver = schema.Provider.Driver
		return normalized, nil
	}
	return NormalizeHistoricalV1(schema)
}

// NormalizeHistoricalV3 is the independent frozen v3 normalization boundary.
// It remains separate from Normalize even while v3 is current so reviewed
// history cannot silently adopt later current rules.
func NormalizeHistoricalV3(schema PhysicalSchema) (PhysicalSchema, error) {
	if schema.Version != 3 || schema.CanonicalVersion != 3 {
		return PhysicalSchema{}, fmt.Errorf("historical v3 normalization requires exact 3/3 versions")
	}
	if err := validateHistoricalV3SchemaShape(); err != nil {
		return PhysicalSchema{}, err
	}
	if err := validateHistoricalV3ClosedValues(reflect.ValueOf(schema)); err != nil {
		return PhysicalSchema{}, err
	}
	return normalizeHistoricalV3Tagged(schema)
}

// NormalizeHistoricalV2 is the independent frozen v2 normalization boundary.
// It rejects every nonzero current-only field before sorting or validation.
func NormalizeHistoricalV2(schema PhysicalSchema) (PhysicalSchema, error) {
	if schema.Version != 2 || schema.CanonicalVersion != 2 {
		return PhysicalSchema{}, fmt.Errorf("historical v2 normalization requires exact 2/2 versions")
	}
	if err := validateHistoricalV2SchemaShape(); err != nil {
		return PhysicalSchema{}, err
	}
	if err := validateHistoricalV2ClosedValues(reflect.ValueOf(schema)); err != nil {
		return PhysicalSchema{}, err
	}
	return normalizeHistoricalV2Tagged(schema)
}

// NormalizeHistoricalV1 is the closed original v1 vocabulary boundary. It
// deliberately refuses current documents and later reviewed runtime profiles.
func NormalizeHistoricalV1(schema PhysicalSchema) (PhysicalSchema, error) {
	if schema.Version != 1 || schema.CanonicalVersion != 1 {
		return PhysicalSchema{}, fmt.Errorf("historical v1 normalization requires exact 1/1 versions")
	}
	normalized, err := normalizeHistoricalV1Tagged(schema)
	if err != nil {
		return PhysicalSchema{}, err
	}
	if err := validateHistoricalV1SchemaShape(); err != nil {
		return PhysicalSchema{}, err
	}
	if err := validateHistoricalV1ClosedValues(reflect.ValueOf(normalized)); err != nil {
		return PhysicalSchema{}, err
	}
	if err := walkHistoricalV1Storage(reflect.ValueOf(normalized)); err != nil {
		return PhysicalSchema{}, err
	}
	return normalized, nil
}

func normalizeCollections(schema PhysicalSchema) PhysicalSchema {
	normalized := cloneSchema(schema)
	sort.Slice(normalized.Provider.Capabilities, func(i, j int) bool {
		a, b := normalized.Provider.Capabilities[i], normalized.Provider.Capabilities[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Verification < b.Verification
	})
	sort.Slice(normalized.Tables, func(i, j int) bool {
		if normalized.Tables[i].Name != normalized.Tables[j].Name {
			return normalized.Tables[i].Name < normalized.Tables[j].Name
		}
		return normalized.Tables[i].ID < normalized.Tables[j].ID
	})
	for tableIndex := range normalized.Tables {
		table := &normalized.Tables[tableIndex]
		sort.Slice(table.Columns, func(i, j int) bool {
			if table.Columns[i].Ordinal != table.Columns[j].Ordinal {
				return table.Columns[i].Ordinal < table.Columns[j].Ordinal
			}
			return table.Columns[i].ID < table.Columns[j].ID
		})
		sort.Slice(table.Uniques, func(i, j int) bool { return table.Uniques[i].ID < table.Uniques[j].ID })
		sort.Slice(table.ForeignKeys, func(i, j int) bool { return table.ForeignKeys[i].ID < table.ForeignKeys[j].ID })
		sort.Slice(table.Checks, func(i, j int) bool { return table.Checks[i].ID < table.Checks[j].ID })
		sort.Slice(table.Indexes, func(i, j int) bool { return table.Indexes[i].ID < table.Indexes[j].ID })
		sortRequirements(table.RequiredCapabilities)
		for index := range table.Columns {
			sortRequirements(table.Columns[index].RequiredCapabilities)
		}
		if table.PrimaryKey != nil {
			sortRequirements(table.PrimaryKey.RequiredCapabilities)
		}
		for index := range table.Uniques {
			sortRequirements(table.Uniques[index].RequiredCapabilities)
		}
		for index := range table.ForeignKeys {
			sortRequirements(table.ForeignKeys[index].RequiredCapabilities)
		}
		for index := range table.Checks {
			sortRequirements(table.Checks[index].RequiredCapabilities)
		}
		for index := range table.Indexes {
			sortRequirements(table.Indexes[index].RequiredCapabilities)
		}
	}
	sort.Slice(normalized.Extensions, func(i, j int) bool { return normalized.Extensions[i].ID < normalized.Extensions[j].ID })
	for index := range normalized.Extensions {
		sortAttributes(normalized.Extensions[index].Attributes)
		sortRequirements(normalized.Extensions[index].RequiredCapabilities)
	}
	sort.Slice(normalized.System.Objects, func(i, j int) bool { return normalized.System.Objects[i].ID < normalized.System.Objects[j].ID })
	for index := range normalized.System.Objects {
		sortAttributes(normalized.System.Objects[index].Attributes)
		sortRequirements(normalized.System.Objects[index].RequiredCapabilities)
	}
	sort.Slice(normalized.Unmanaged, func(i, j int) bool {
		if normalized.Unmanaged[i].Kind != normalized.Unmanaged[j].Kind {
			return normalized.Unmanaged[i].Kind < normalized.Unmanaged[j].Kind
		}
		return normalized.Unmanaged[i].Name < normalized.Unmanaged[j].Name
	})
	return normalized
}

func sortRequirements(values []CapabilityRequirement) {
	sort.Slice(values, func(i, j int) bool {
		a, b := values[i], values[j]
		if a.Capability != b.Capability {
			return a.Capability < b.Capability
		}
		if a.Owner.Kind != b.Owner.Kind {
			return a.Owner.Kind < b.Owner.Kind
		}
		if a.Owner.ModelID != b.Owner.ModelID {
			return a.Owner.ModelID < b.Owner.ModelID
		}
		if a.Owner.FieldID != b.Owner.FieldID {
			return a.Owner.FieldID < b.Owner.FieldID
		}
		return a.Owner.ObjectID < b.Owner.ObjectID
	})
}

func sortAttributes(values []Attribute) {
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
}

func cloneSchema(schema PhysicalSchema) PhysicalSchema {
	result := schema
	result.Provider.Capabilities = append([]CapabilityFact(nil), schema.Provider.Capabilities...)
	result.Tables = make([]PhysicalTable, len(schema.Tables))
	for index, table := range schema.Tables {
		result.Tables[index] = cloneTable(table)
	}
	result.Extensions = make([]Extension, len(schema.Extensions))
	for index, extension := range schema.Extensions {
		result.Extensions[index] = extension
		result.Extensions[index].Attributes = cloneAttributes(extension.Attributes)
		result.Extensions[index].RequiredCapabilities = append([]CapabilityRequirement(nil), extension.RequiredCapabilities...)
	}
	result.System.Objects = make([]SystemObject, len(schema.System.Objects))
	for index, object := range schema.System.Objects {
		result.System.Objects[index] = object
		result.System.Objects[index].Attributes = cloneAttributes(object.Attributes)
		result.System.Objects[index].RequiredCapabilities = append([]CapabilityRequirement(nil), object.RequiredCapabilities...)
	}
	result.Unmanaged = append([]UnmanagedObject(nil), schema.Unmanaged...)
	return result
}

func cloneTable(table PhysicalTable) PhysicalTable {
	result := table
	result.OptimisticConcurrency = cloneFieldID(table.OptimisticConcurrency)
	result.RequiredCapabilities = append([]CapabilityRequirement(nil), table.RequiredCapabilities...)
	result.Columns = make([]PhysicalColumn, len(table.Columns))
	for index, column := range table.Columns {
		result.Columns[index] = column
		result.Columns[index].RequiredCapabilities = append([]CapabilityRequirement(nil), column.RequiredCapabilities...)
		result.Columns[index].Storage.Symbol = cloneSymbol(column.Storage.Symbol)
		result.Columns[index].Collation = cloneSymbol(column.Collation)
		result.Columns[index].Default = cloneDefault(column.Default)
		if column.Generated != nil {
			generated := *column.Generated
			generated.Expression = cloneExpression(column.Generated.Expression)
			result.Columns[index].Generated = &generated
		}
	}
	if table.PrimaryKey != nil {
		key := cloneKey(*table.PrimaryKey)
		result.PrimaryKey = &key
	}
	result.Uniques = make([]PhysicalKey, len(table.Uniques))
	for index, key := range table.Uniques {
		result.Uniques[index] = cloneKey(key)
	}
	result.ForeignKeys = make([]PhysicalForeignKey, len(table.ForeignKeys))
	for index, key := range table.ForeignKeys {
		result.ForeignKeys[index] = key
		result.ForeignKeys[index].Columns = append([]ir.FieldID(nil), key.Columns...)
		result.ForeignKeys[index].ReferencedColumns = append([]ir.FieldID(nil), key.ReferencedColumns...)
		result.ForeignKeys[index].RequiredCapabilities = append([]CapabilityRequirement(nil), key.RequiredCapabilities...)
	}
	result.Checks = make([]PhysicalCheck, len(table.Checks))
	for index, check := range table.Checks {
		result.Checks[index] = check
		result.Checks[index].Expression = cloneExpression(check.Expression)
		result.Checks[index].RequiredCapabilities = append([]CapabilityRequirement(nil), check.RequiredCapabilities...)
	}
	result.Indexes = make([]PhysicalIndex, len(table.Indexes))
	for index, physicalIndex := range table.Indexes {
		result.Indexes[index] = physicalIndex
		result.Indexes[index].Keys = make([]IndexKey, len(physicalIndex.Keys))
		for keyIndex, key := range physicalIndex.Keys {
			result.Indexes[index].Keys[keyIndex] = key
			result.Indexes[index].Keys[keyIndex].Column = cloneFieldID(key.Column)
			if key.Expression != nil {
				expression := cloneExpression(*key.Expression)
				result.Indexes[index].Keys[keyIndex].Expression = &expression
			}
			result.Indexes[index].Keys[keyIndex].Collation = cloneSymbol(key.Collation)
			result.Indexes[index].Keys[keyIndex].OpClass = cloneSymbol(key.OpClass)
		}
		result.Indexes[index].Include = append([]ir.FieldID(nil), physicalIndex.Include...)
		if physicalIndex.Predicate != nil {
			expression := cloneExpression(*physicalIndex.Predicate)
			result.Indexes[index].Predicate = &expression
		}
		result.Indexes[index].RequiredCapabilities = append([]CapabilityRequirement(nil), physicalIndex.RequiredCapabilities...)
	}
	return result
}

func cloneKey(key PhysicalKey) PhysicalKey {
	key.Columns = append([]ir.FieldID(nil), key.Columns...)
	key.RequiredCapabilities = append([]CapabilityRequirement(nil), key.RequiredCapabilities...)
	return key
}

func cloneDefault(value PhysicalDefault) PhysicalDefault {
	result := value
	if value.Literal != nil {
		literal := *value.Literal
		result.Literal = &literal
	}
	if value.Expression != nil {
		expression := cloneExpression(*value.Expression)
		result.Expression = &expression
	}
	return result
}

func cloneExpression(value Expression) Expression {
	result := value
	result.Symbol = cloneSymbol(value.Symbol)
	result.Column = cloneFieldID(value.Column)
	if value.Literal != nil {
		literal := *value.Literal
		result.Literal = &literal
	}
	result.StorageClone()
	result.Operands = make([]Expression, len(value.Operands))
	for index, operand := range value.Operands {
		result.Operands[index] = cloneExpression(operand)
	}
	return result
}

func (expression *Expression) StorageClone() {
	expression.Type.Symbol = cloneSymbol(expression.Type.Symbol)
}

func cloneSymbol(value *SemanticSymbol) *SemanticSymbol {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneFieldID(value *ir.FieldID) *ir.FieldID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneAttributes(values []Attribute) []Attribute {
	result := make([]Attribute, len(values))
	for index, attribute := range values {
		result[index] = attribute
		result[index].Value = cloneSemanticValue(attribute.Value)
	}
	return result
}

func cloneSemanticValue(value SemanticValue) SemanticValue {
	// Canonical physical format v1 historically normalized list payloads to
	// their zero values. Preserve those bytes until a versioned v2 decoder and
	// migration exists; new extensions must use closed scalar encodings.
	value.List = make([]SemanticValue, len(value.List))
	for index, item := range value.List {
		value.List[index] = cloneSemanticValue(item)
	}
	return value
}
