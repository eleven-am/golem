package physical

// Retained physical-v2 normalization adaptation, pinned by the v3 closure
// provenance test. Equivalence is owned by released artifact and behavior
// goldens, not a nonexistent upstream source tag.

import (
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// normalizeHistoricalV2Tagged returns a deeply detached schema in canonical collection order and
// validates the complete semantic graph. Ordered components (key/index/FK
// columns and expression operands) retain their authored order.
func normalizeHistoricalV2Tagged(schema PhysicalSchema) (PhysicalSchema, error) {
	normalized := historicalV2CloneSchema(schema)
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
		historicalV2SortRequirements(table.RequiredCapabilities)
		for index := range table.Columns {
			historicalV2SortRequirements(table.Columns[index].RequiredCapabilities)
		}
		if table.PrimaryKey != nil {
			historicalV2SortRequirements(table.PrimaryKey.RequiredCapabilities)
		}
		for index := range table.Uniques {
			historicalV2SortRequirements(table.Uniques[index].RequiredCapabilities)
		}
		for index := range table.ForeignKeys {
			historicalV2SortRequirements(table.ForeignKeys[index].RequiredCapabilities)
		}
		for index := range table.Checks {
			historicalV2SortRequirements(table.Checks[index].RequiredCapabilities)
		}
		for index := range table.Indexes {
			historicalV2SortRequirements(table.Indexes[index].RequiredCapabilities)
		}
	}
	sort.Slice(normalized.Extensions, func(i, j int) bool { return normalized.Extensions[i].ID < normalized.Extensions[j].ID })
	for index := range normalized.Extensions {
		historicalV2SortAttributes(normalized.Extensions[index].Attributes)
		historicalV2SortRequirements(normalized.Extensions[index].RequiredCapabilities)
	}
	sort.Slice(normalized.System.Objects, func(i, j int) bool { return normalized.System.Objects[i].ID < normalized.System.Objects[j].ID })
	for index := range normalized.System.Objects {
		historicalV2SortAttributes(normalized.System.Objects[index].Attributes)
		historicalV2SortRequirements(normalized.System.Objects[index].RequiredCapabilities)
	}
	sort.Slice(normalized.Unmanaged, func(i, j int) bool {
		if normalized.Unmanaged[i].Kind != normalized.Unmanaged[j].Kind {
			return normalized.Unmanaged[i].Kind < normalized.Unmanaged[j].Kind
		}
		return normalized.Unmanaged[i].Name < normalized.Unmanaged[j].Name
	})
	if err := validateHistoricalV2Tagged(normalized); err != nil {
		return PhysicalSchema{}, err
	}
	return normalized, nil
}

func historicalV2SortRequirements(values []CapabilityRequirement) {
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

func historicalV2SortAttributes(values []Attribute) {
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
}

func historicalV2CloneSchema(schema PhysicalSchema) PhysicalSchema {
	result := schema
	result.Provider.Capabilities = append([]CapabilityFact(nil), schema.Provider.Capabilities...)
	result.Tables = make([]PhysicalTable, len(schema.Tables))
	for index, table := range schema.Tables {
		result.Tables[index] = historicalV2CloneTable(table)
	}
	result.Extensions = make([]Extension, len(schema.Extensions))
	for index, extension := range schema.Extensions {
		result.Extensions[index] = extension
		result.Extensions[index].Attributes = historicalV2CloneAttributes(extension.Attributes)
		result.Extensions[index].RequiredCapabilities = append([]CapabilityRequirement(nil), extension.RequiredCapabilities...)
	}
	result.System.Objects = make([]SystemObject, len(schema.System.Objects))
	for index, object := range schema.System.Objects {
		result.System.Objects[index] = object
		result.System.Objects[index].Attributes = historicalV2CloneAttributes(object.Attributes)
		result.System.Objects[index].RequiredCapabilities = append([]CapabilityRequirement(nil), object.RequiredCapabilities...)
	}
	result.Unmanaged = append([]UnmanagedObject(nil), schema.Unmanaged...)
	return result
}

func historicalV2CloneTable(table PhysicalTable) PhysicalTable {
	result := table
	result.RequiredCapabilities = append([]CapabilityRequirement(nil), table.RequiredCapabilities...)
	result.Columns = make([]PhysicalColumn, len(table.Columns))
	for index, column := range table.Columns {
		result.Columns[index] = column
		result.Columns[index].RequiredCapabilities = append([]CapabilityRequirement(nil), column.RequiredCapabilities...)
		result.Columns[index].Storage.Symbol = historicalV2CloneSymbol(column.Storage.Symbol)
		result.Columns[index].Collation = historicalV2CloneSymbol(column.Collation)
		result.Columns[index].Default = historicalV2CloneDefault(column.Default)
		if column.Generated != nil {
			generated := *column.Generated
			generated.Expression = historicalV2CloneExpression(column.Generated.Expression)
			result.Columns[index].Generated = &generated
		}
	}
	if table.PrimaryKey != nil {
		key := historicalV2CloneKey(*table.PrimaryKey)
		result.PrimaryKey = &key
	}
	result.Uniques = make([]PhysicalKey, len(table.Uniques))
	for index, key := range table.Uniques {
		result.Uniques[index] = historicalV2CloneKey(key)
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
		result.Checks[index].Expression = historicalV2CloneExpression(check.Expression)
		result.Checks[index].RequiredCapabilities = append([]CapabilityRequirement(nil), check.RequiredCapabilities...)
	}
	result.Indexes = make([]PhysicalIndex, len(table.Indexes))
	for index, physicalIndex := range table.Indexes {
		result.Indexes[index] = physicalIndex
		result.Indexes[index].Keys = make([]IndexKey, len(physicalIndex.Keys))
		for keyIndex, key := range physicalIndex.Keys {
			result.Indexes[index].Keys[keyIndex] = key
			result.Indexes[index].Keys[keyIndex].Column = historicalV2CloneFieldID(key.Column)
			if key.Expression != nil {
				expression := historicalV2CloneExpression(*key.Expression)
				result.Indexes[index].Keys[keyIndex].Expression = &expression
			}
			result.Indexes[index].Keys[keyIndex].Collation = historicalV2CloneSymbol(key.Collation)
			result.Indexes[index].Keys[keyIndex].OpClass = historicalV2CloneSymbol(key.OpClass)
		}
		result.Indexes[index].Include = append([]ir.FieldID(nil), physicalIndex.Include...)
		if physicalIndex.Predicate != nil {
			expression := historicalV2CloneExpression(*physicalIndex.Predicate)
			result.Indexes[index].Predicate = &expression
		}
		result.Indexes[index].RequiredCapabilities = append([]CapabilityRequirement(nil), physicalIndex.RequiredCapabilities...)
	}
	return result
}

func historicalV2CloneKey(key PhysicalKey) PhysicalKey {
	key.Columns = append([]ir.FieldID(nil), key.Columns...)
	key.RequiredCapabilities = append([]CapabilityRequirement(nil), key.RequiredCapabilities...)
	return key
}

func historicalV2CloneDefault(value PhysicalDefault) PhysicalDefault {
	result := value
	if value.Literal != nil {
		literal := *value.Literal
		result.Literal = &literal
	}
	if value.Expression != nil {
		expression := historicalV2CloneExpression(*value.Expression)
		result.Expression = &expression
	}
	return result
}

func historicalV2CloneExpression(value Expression) Expression {
	result := value
	result.Symbol = historicalV2CloneSymbol(value.Symbol)
	result.Column = historicalV2CloneFieldID(value.Column)
	if value.Literal != nil {
		literal := *value.Literal
		result.Literal = &literal
	}
	result.historicalV2StorageClone()
	result.Operands = make([]Expression, len(value.Operands))
	for index, operand := range value.Operands {
		result.Operands[index] = historicalV2CloneExpression(operand)
	}
	return result
}

func (expression *Expression) historicalV2StorageClone() {
	expression.Type.Symbol = historicalV2CloneSymbol(expression.Type.Symbol)
}

func historicalV2CloneSymbol(value *SemanticSymbol) *SemanticSymbol {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func historicalV2CloneFieldID(value *ir.FieldID) *ir.FieldID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func historicalV2CloneAttributes(values []Attribute) []Attribute {
	result := make([]Attribute, len(values))
	for index, attribute := range values {
		result[index] = attribute
		result[index].Value = historicalV2CloneSemanticValue(attribute.Value)
	}
	return result
}

func historicalV2CloneSemanticValue(value SemanticValue) SemanticValue {
	value.List = make([]SemanticValue, len(value.List))
	for index, item := range value.List {
		value.List[index] = historicalV2CloneSemanticValue(item)
	}
	return value
}
