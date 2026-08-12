package physical

// Retained normalization adaptation of go/v0.0.1 normalize.go.
// Tagged source SHA-256: e2af7ff44eec451af2fbb4e3bc018c6dc60237821da65b079818c47e6a024714.

import (
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	historicalV1NormalizeUpstreamSHA256 = "e2af7ff44eec451af2fbb4e3bc018c6dc60237821da65b079818c47e6a024714"
	historicalV1NormalizeUpstreamLines  = 261
)

// normalizeHistoricalV1Tagged returns a deeply detached schema in canonical collection order and
// validates the complete semantic graph. Ordered components (key/index/FK
// columns and expression operands) retain their authored order.
func normalizeHistoricalV1Tagged(schema PhysicalSchema) (PhysicalSchema, error) {
	normalized := historicalV1CloneSchema(schema)
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
		historicalV1SortRequirements(table.RequiredCapabilities)
		for index := range table.Columns {
			historicalV1SortRequirements(table.Columns[index].RequiredCapabilities)
		}
		if table.PrimaryKey != nil {
			historicalV1SortRequirements(table.PrimaryKey.RequiredCapabilities)
		}
		for index := range table.Uniques {
			historicalV1SortRequirements(table.Uniques[index].RequiredCapabilities)
		}
		for index := range table.ForeignKeys {
			historicalV1SortRequirements(table.ForeignKeys[index].RequiredCapabilities)
		}
		for index := range table.Checks {
			historicalV1SortRequirements(table.Checks[index].RequiredCapabilities)
		}
		for index := range table.Indexes {
			historicalV1SortRequirements(table.Indexes[index].RequiredCapabilities)
		}
	}
	sort.Slice(normalized.Extensions, func(i, j int) bool { return normalized.Extensions[i].ID < normalized.Extensions[j].ID })
	for index := range normalized.Extensions {
		historicalV1SortAttributes(normalized.Extensions[index].Attributes)
		historicalV1SortRequirements(normalized.Extensions[index].RequiredCapabilities)
	}
	sort.Slice(normalized.System.Objects, func(i, j int) bool { return normalized.System.Objects[i].ID < normalized.System.Objects[j].ID })
	for index := range normalized.System.Objects {
		historicalV1SortAttributes(normalized.System.Objects[index].Attributes)
		historicalV1SortRequirements(normalized.System.Objects[index].RequiredCapabilities)
	}
	sort.Slice(normalized.Unmanaged, func(i, j int) bool {
		if normalized.Unmanaged[i].Kind != normalized.Unmanaged[j].Kind {
			return normalized.Unmanaged[i].Kind < normalized.Unmanaged[j].Kind
		}
		return normalized.Unmanaged[i].Name < normalized.Unmanaged[j].Name
	})
	if err := validateHistoricalV1Tagged(normalized); err != nil {
		return PhysicalSchema{}, err
	}
	return normalized, nil
}

func historicalV1SortRequirements(values []CapabilityRequirement) {
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

func historicalV1SortAttributes(values []Attribute) {
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
}

func historicalV1CloneSchema(schema PhysicalSchema) PhysicalSchema {
	result := schema
	result.Provider.Capabilities = append([]CapabilityFact(nil), schema.Provider.Capabilities...)
	result.Tables = make([]PhysicalTable, len(schema.Tables))
	for index, table := range schema.Tables {
		result.Tables[index] = historicalV1CloneTable(table)
	}
	result.Extensions = make([]Extension, len(schema.Extensions))
	for index, extension := range schema.Extensions {
		result.Extensions[index] = extension
		result.Extensions[index].Attributes = historicalV1CloneAttributes(extension.Attributes)
		result.Extensions[index].RequiredCapabilities = append([]CapabilityRequirement(nil), extension.RequiredCapabilities...)
	}
	result.System.Objects = make([]SystemObject, len(schema.System.Objects))
	for index, object := range schema.System.Objects {
		result.System.Objects[index] = object
		result.System.Objects[index].Attributes = historicalV1CloneAttributes(object.Attributes)
		result.System.Objects[index].RequiredCapabilities = append([]CapabilityRequirement(nil), object.RequiredCapabilities...)
	}
	result.Unmanaged = append([]UnmanagedObject(nil), schema.Unmanaged...)
	return result
}

func historicalV1CloneTable(table PhysicalTable) PhysicalTable {
	result := table
	result.RequiredCapabilities = append([]CapabilityRequirement(nil), table.RequiredCapabilities...)
	result.Columns = make([]PhysicalColumn, len(table.Columns))
	for index, column := range table.Columns {
		result.Columns[index] = column
		result.Columns[index].RequiredCapabilities = append([]CapabilityRequirement(nil), column.RequiredCapabilities...)
		result.Columns[index].Storage.Symbol = historicalV1CloneSymbol(column.Storage.Symbol)
		result.Columns[index].Collation = historicalV1CloneSymbol(column.Collation)
		result.Columns[index].Default = historicalV1CloneDefault(column.Default)
		if column.Generated != nil {
			generated := *column.Generated
			generated.Expression = historicalV1CloneExpression(column.Generated.Expression)
			result.Columns[index].Generated = &generated
		}
	}
	if table.PrimaryKey != nil {
		key := historicalV1CloneKey(*table.PrimaryKey)
		result.PrimaryKey = &key
	}
	result.Uniques = make([]PhysicalKey, len(table.Uniques))
	for index, key := range table.Uniques {
		result.Uniques[index] = historicalV1CloneKey(key)
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
		result.Checks[index].Expression = historicalV1CloneExpression(check.Expression)
		result.Checks[index].RequiredCapabilities = append([]CapabilityRequirement(nil), check.RequiredCapabilities...)
	}
	result.Indexes = make([]PhysicalIndex, len(table.Indexes))
	for index, physicalIndex := range table.Indexes {
		result.Indexes[index] = physicalIndex
		result.Indexes[index].Keys = make([]IndexKey, len(physicalIndex.Keys))
		for keyIndex, key := range physicalIndex.Keys {
			result.Indexes[index].Keys[keyIndex] = key
			result.Indexes[index].Keys[keyIndex].Column = historicalV1CloneFieldID(key.Column)
			if key.Expression != nil {
				expression := historicalV1CloneExpression(*key.Expression)
				result.Indexes[index].Keys[keyIndex].Expression = &expression
			}
			result.Indexes[index].Keys[keyIndex].Collation = historicalV1CloneSymbol(key.Collation)
			result.Indexes[index].Keys[keyIndex].OpClass = historicalV1CloneSymbol(key.OpClass)
		}
		result.Indexes[index].Include = append([]ir.FieldID(nil), physicalIndex.Include...)
		if physicalIndex.Predicate != nil {
			expression := historicalV1CloneExpression(*physicalIndex.Predicate)
			result.Indexes[index].Predicate = &expression
		}
		result.Indexes[index].RequiredCapabilities = append([]CapabilityRequirement(nil), physicalIndex.RequiredCapabilities...)
	}
	return result
}

func historicalV1CloneKey(key PhysicalKey) PhysicalKey {
	key.Columns = append([]ir.FieldID(nil), key.Columns...)
	key.RequiredCapabilities = append([]CapabilityRequirement(nil), key.RequiredCapabilities...)
	return key
}

func historicalV1CloneDefault(value PhysicalDefault) PhysicalDefault {
	result := value
	if value.Literal != nil {
		literal := *value.Literal
		result.Literal = &literal
	}
	if value.Expression != nil {
		expression := historicalV1CloneExpression(*value.Expression)
		result.Expression = &expression
	}
	return result
}

func historicalV1CloneExpression(value Expression) Expression {
	result := value
	result.Symbol = historicalV1CloneSymbol(value.Symbol)
	result.Column = historicalV1CloneFieldID(value.Column)
	if value.Literal != nil {
		literal := *value.Literal
		result.Literal = &literal
	}
	result.historicalV1StorageClone()
	result.Operands = make([]Expression, len(value.Operands))
	for index, operand := range value.Operands {
		result.Operands[index] = historicalV1CloneExpression(operand)
	}
	return result
}

func (expression *Expression) historicalV1StorageClone() {
	expression.Type.Symbol = historicalV1CloneSymbol(expression.Type.Symbol)
}

func historicalV1CloneSymbol(value *SemanticSymbol) *SemanticSymbol {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func historicalV1CloneFieldID(value *ir.FieldID) *ir.FieldID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func historicalV1CloneAttributes(values []Attribute) []Attribute {
	result := make([]Attribute, len(values))
	for index, attribute := range values {
		result[index] = attribute
		result[index].Value = historicalV1CloneSemanticValue(attribute.Value)
	}
	return result
}

func historicalV1CloneSemanticValue(value SemanticValue) SemanticValue {
	value.List = make([]SemanticValue, len(value.List))
	for index, item := range value.List {
		value.List[index] = historicalV1CloneSemanticValue(item)
	}
	return value
}
