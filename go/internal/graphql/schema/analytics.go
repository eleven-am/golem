package schema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type analyticsField struct {
	id       ir.FieldID
	name     string
	typ      ir.LogicalTypeIR
	nullable bool
}

type analyticsCategory struct {
	name   string
	fields []analyticsField
	types  map[ir.FieldID]string
}

func renderAnalyticsModel(view modelView, models map[ir.ModelID]ir.ModelDeclIR, contracts map[ir.ModelID]ir.ModelContractIR, enums map[ir.EnumID]ir.EnumContractIR) ([]string, error) {
	enabled := map[ir.Operation]bool{}
	for _, operation := range view.contract.Operations {
		enabled[operation] = true
	}
	if !enabled[ir.OperationAggregate] && !enabled[ir.OperationGroupBy] && !enabled[ir.OperationRelationGroupBy] {
		return nil, nil
	}
	if view.contract.Aggregation == nil || !view.contract.Aggregation.Enabled {
		return nil, fmt.Errorf("model %s enables GraphQL analytics without an analytics contract", view.contract.GraphQLName)
	}

	dimensions, err := localAnalyticsFields(view, view.contract.Aggregation.Dimensions, view.contract.Aggregation.DimensionsExplicit)
	if err != nil {
		return nil, err
	}
	measures, err := localAnalyticsFields(view, view.contract.Aggregation.Measures, view.contract.Aggregation.MeasuresExplicit)
	if err != nil {
		return nil, err
	}
	relationDimensions, err := relationAnalyticsFields(view, models, contracts)
	if err != nil {
		return nil, err
	}
	if enabled[ir.OperationGroupBy] && len(dimensions) == 0 {
		return nil, fmt.Errorf("model %s groupBy has no configured dimensions", view.contract.GraphQLName)
	}
	if enabled[ir.OperationRelationGroupBy] && len(relationDimensions) == 0 {
		return nil, fmt.Errorf("model %s relationGroupBy has no configured relation dimensions", view.contract.GraphQLName)
	}

	name := view.contract.GraphQLName
	categories, err := analyticsCategories(name, measures, enums)
	if err != nil {
		return nil, err
	}
	var sections []string
	for _, category := range categories {
		if len(category.fields) == 0 {
			continue
		}
		sections = append(sections,
			renderAnalyticsOutput(name, category),
			renderAnalyticsHaving(name, category, enums),
			renderAnalyticsOrder(name, category),
		)
	}
	aggregateFields := analyticsContainerFields(name, categories)
	sections = append(sections, renderAnalyticsEnvelope(name+"Aggregate", "", aggregateFields))

	if enabled[ir.OperationGroupBy] {
		sections = append(sections,
			renderAnalyticsFieldEnum(name+"GroupField", dimensions),
			renderAnalyticsKey(name+"GroupKey", dimensions, enums),
			renderAnalyticsKeyHaving(name+"GroupKeyHavingInput", dimensions, enums),
			renderAnalyticsKeyOrder(name+"GroupKeyOrderByInput", dimensions),
			renderAnalyticsEnvelope(name+"Group", "key: "+name+"GroupKey!", aggregateFields),
			renderAnalyticsRootHaving(name+"GroupHavingInput", name+"GroupKeyHavingInput", name, categories),
			renderAnalyticsRootOrder(name+"GroupOrderByInput", name+"GroupKeyOrderByInput", name, categories),
		)
	}
	if enabled[ir.OperationRelationGroupBy] {
		all := append(append([]analyticsField(nil), dimensions...), relationDimensions...)
		sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
		sections = append(sections,
			renderAnalyticsFieldEnum(name+"RelationGroupField", all),
			renderAnalyticsKey(name+"RelationGroupKey", all, enums),
			renderAnalyticsKeyHaving(name+"RelationGroupKeyHavingInput", all, enums),
			renderAnalyticsKeyOrder(name+"RelationGroupKeyOrderByInput", all),
			renderAnalyticsEnvelope(name+"RelationGroup", "key: "+name+"RelationGroupKey!", aggregateFields),
			renderAnalyticsRootHaving(name+"RelationGroupHavingInput", name+"RelationGroupKeyHavingInput", name, categories),
			renderAnalyticsRootOrder(name+"RelationGroupOrderByInput", name+"RelationGroupKeyOrderByInput", name, categories),
		)
	}
	return sections, nil
}

func localAnalyticsFields(view modelView, allow []ir.FieldID, explicit bool) ([]analyticsField, error) {
	allowed := map[ir.FieldID]bool{}
	for _, id := range allow {
		allowed[id] = true
	}
	all := !explicit
	var result []analyticsField
	for _, field := range view.model.Fields {
		contract, ok := view.fields[field.ID]
		if !ok || !ir.ModesReadable(contract.Modes) || field.Scalar == nil || !analyticsGroupable(field.Scalar.Type.Kind) {
			continue
		}
		if !all && !allowed[field.ID] {
			continue
		}
		result = append(result, analyticsField{id: field.ID, name: contract.GraphQLName, typ: field.Scalar.Type, nullable: field.Scalar.Nullable})
	}
	if !all && len(result) != len(allowed) {
		return nil, fmt.Errorf("model %s analytics allowlist contains an unavailable field", view.contract.GraphQLName)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result, nil
}

func relationAnalyticsFields(view modelView, models map[ir.ModelID]ir.ModelDeclIR, contracts map[ir.ModelID]ir.ModelContractIR) ([]analyticsField, error) {
	var result []analyticsField
	reserved := map[string]bool{}
	for _, contract := range view.fields {
		if contract.GraphQLName != "" && ir.ModesReadable(contract.Modes) {
			reserved[contract.GraphQLName] = true
		}
	}
	for _, configured := range view.contract.Aggregation.RelationDimensions {
		if reserved[configured.Name] {
			return nil, fmt.Errorf("model %s relation dimension %s collides with a GraphQL field", view.contract.GraphQLName, configured.Name)
		}
		reserved[configured.Name] = true
		current := view.model.ID
		for _, id := range configured.Path {
			relation, ok := view.relations[id]
			if !ok || relation.SourceModel != current {
				return nil, fmt.Errorf("model %s relation dimension %s has an invalid path", view.contract.GraphQLName, configured.Name)
			}
			current = relation.TargetModel
		}
		model, modelOK := models[current]
		contract, contractOK := contracts[current]
		if !modelOK || !contractOK || !contract.Exposed {
			return nil, fmt.Errorf("model %s relation dimension %s has no terminal model", view.contract.GraphQLName, configured.Name)
		}
		field := fieldByID(model, configured.TerminalField)
		fieldContract, exposed := fieldContractByID(contract, configured.TerminalField)
		if field == nil || field.Scalar == nil || !exposed || !ir.ModesReadable(fieldContract.Modes) || !analyticsGroupable(field.Scalar.Type.Kind) {
			return nil, fmt.Errorf("model %s relation dimension %s has an unavailable terminal field", view.contract.GraphQLName, configured.Name)
		}
		result = append(result, analyticsField{id: configured.TerminalField, name: configured.Name, typ: field.Scalar.Type, nullable: field.Scalar.Nullable})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result, nil
}

func analyticsGroupable(kind ir.LogicalTypeKind) bool {
	switch kind {
	case ir.TypeBool, ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64, ir.TypeDecimal, ir.TypeString, ir.TypeUUID, ir.TypeDate, ir.TypeTime, ir.TypeDateTime, ir.TypeEnum:
		return true
	default:
		return false
	}
}

func analyticsCategories(model string, fields []analyticsField, enums map[ir.EnumID]ir.EnumContractIR) ([]analyticsCategory, error) {
	values := []analyticsCategory{
		{name: "Count", types: map[ir.FieldID]string{}},
		{name: "Sum", types: map[ir.FieldID]string{}},
		{name: "Avg", types: map[ir.FieldID]string{}},
		{name: "Min", types: map[ir.FieldID]string{}},
		{name: "Max", types: map[ir.FieldID]string{}},
	}
	for _, field := range fields {
		base, _, _, err := scalarGraphQL(field.typ, enums)
		if err != nil {
			return nil, fmt.Errorf("model %s analytics field %s: %w", model, field.name, err)
		}
		values[0].fields = append(values[0].fields, field)
		values[0].types[field.id] = "BigInt"
		if analyticsNumeric(field.typ.Kind) {
			values[1].fields = append(values[1].fields, field)
			values[2].fields = append(values[2].fields, field)
			values[1].types[field.id] = analyticsSumType(field.typ.Kind)
			values[2].types[field.id] = analyticsAverageType(field.typ.Kind)
		}
		if analyticsMinMax(field.typ.Kind) {
			values[3].fields = append(values[3].fields, field)
			values[4].fields = append(values[4].fields, field)
			values[3].types[field.id], values[4].types[field.id] = base, base
		}
	}
	return values, nil
}

func analyticsNumeric(kind ir.LogicalTypeKind) bool {
	switch kind {
	case ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64, ir.TypeDecimal:
		return true
	default:
		return false
	}
}

func analyticsMinMax(kind ir.LogicalTypeKind) bool {
	return analyticsNumeric(kind) || kind == ir.TypeString || kind == ir.TypeDate || kind == ir.TypeTime || kind == ir.TypeDateTime
}

func analyticsSumType(kind ir.LogicalTypeKind) string {
	if kind == ir.TypeInt16 || kind == ir.TypeInt32 || kind == ir.TypeInt64 {
		return "BigInt"
	}
	if kind == ir.TypeDecimal {
		return "Decimal"
	}
	return "Float"
}

func analyticsAverageType(kind ir.LogicalTypeKind) string {
	if kind == ir.TypeDecimal {
		return "Decimal"
	}
	return "Float"
}

func renderAnalyticsOutput(model string, category analyticsCategory) string {
	var out strings.Builder
	fmt.Fprintf(&out, "type %s%sAggregate {\n", model, category.name)
	for _, field := range category.fields {
		nonNull := ""
		if category.name == "Count" {
			nonNull = "!"
		}
		fmt.Fprintf(&out, "  %s: %s%s\n", field.name, category.types[field.id], nonNull)
	}
	out.WriteString("}")
	return out.String()
}

func renderAnalyticsHaving(model string, category analyticsCategory, enums map[ir.EnumID]ir.EnumContractIR) string {
	var out strings.Builder
	fmt.Fprintf(&out, "input %s%sAggregateHavingInput {\n", model, category.name)
	for _, field := range category.fields {
		fmt.Fprintf(&out, "  %s: %sAnalyticsFilter\n", field.name, category.types[field.id])
	}
	out.WriteString("}")
	_ = enums
	return out.String()
}

func renderAnalyticsOrder(model string, category analyticsCategory) string {
	var out strings.Builder
	fmt.Fprintf(&out, "input %s%sAggregateOrderByInput {\n", model, category.name)
	for _, field := range category.fields {
		fmt.Fprintf(&out, "  %s: SortOrder\n", field.name)
	}
	out.WriteString("}")
	return out.String()
}

func analyticsContainerFields(model string, categories []analyticsCategory) []string {
	result := []string{"count: BigInt!"}
	for _, category := range categories {
		if len(category.fields) == 0 {
			continue
		}
		name := strings.ToLower(category.name[:1]) + category.name[1:]
		if category.name == "Count" {
			name = "countFields"
		}
		result = append(result, fmt.Sprintf("%s: %s%sAggregate!", name, model, category.name))
	}
	return result
}

func renderAnalyticsEnvelope(name, first string, fields []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "type %s {\n", name)
	if first != "" {
		fmt.Fprintf(&out, "  %s\n", first)
	}
	for _, field := range fields {
		fmt.Fprintf(&out, "  %s\n", field)
	}
	out.WriteString("}")
	return out.String()
}

func renderAnalyticsFieldEnum(name string, fields []analyticsField) string {
	var out strings.Builder
	fmt.Fprintf(&out, "enum %s {\n", name)
	for _, field := range fields {
		fmt.Fprintf(&out, "  %s\n", field.name)
	}
	out.WriteString("}")
	return out.String()
}

func renderAnalyticsKey(name string, fields []analyticsField, enums map[ir.EnumID]ir.EnumContractIR) string {
	var out strings.Builder
	fmt.Fprintf(&out, "type %s {\n", name)
	for _, field := range fields {
		base, _, _, _ := scalarGraphQL(field.typ, enums)
		fmt.Fprintf(&out, "  %s: %s%s\n", field.name, base, bang(!field.nullable))
	}
	out.WriteString("}")
	return out.String()
}

func renderAnalyticsKeyHaving(name string, fields []analyticsField, enums map[ir.EnumID]ir.EnumContractIR) string {
	var out strings.Builder
	fmt.Fprintf(&out, "input %s {\n", name)
	for _, field := range fields {
		base, _, _, _ := scalarGraphQL(field.typ, enums)
		fmt.Fprintf(&out, "  %s: %sAnalyticsFilter\n", field.name, base)
	}
	out.WriteString("}")
	return out.String()
}

func renderAnalyticsKeyOrder(name string, fields []analyticsField) string {
	var out strings.Builder
	fmt.Fprintf(&out, "input %s {\n", name)
	for _, field := range fields {
		fmt.Fprintf(&out, "  %s: SortOrder\n", field.name)
	}
	out.WriteString("}")
	return out.String()
}

func renderAnalyticsRootHaving(name, keyType, model string, categories []analyticsCategory) string {
	var out strings.Builder
	fmt.Fprintf(&out, "input %s {\n  AND: [%s!]\n  OR: [%s!]\n  NOT: %s\n  key: %s\n  count: BigIntAnalyticsFilter\n", name, name, name, name, keyType)
	for _, category := range categories {
		if len(category.fields) == 0 {
			continue
		}
		field := strings.ToLower(category.name[:1]) + category.name[1:]
		if category.name == "Count" {
			field = "countFields"
		}
		fmt.Fprintf(&out, "  %s: %s%sAggregateHavingInput\n", field, model, category.name)
	}
	out.WriteString("}")
	return out.String()
}

func renderAnalyticsRootOrder(name, keyType, model string, categories []analyticsCategory) string {
	var out strings.Builder
	fmt.Fprintf(&out, "input %s {\n  key: %s\n  count: SortOrder\n", name, keyType)
	for _, category := range categories {
		if len(category.fields) == 0 {
			continue
		}
		field := strings.ToLower(category.name[:1]) + category.name[1:]
		if category.name == "Count" {
			field = "countFields"
		}
		fmt.Fprintf(&out, "  %s: %s%sAggregateOrderByInput\n", field, model, category.name)
	}
	out.WriteString("}")
	return out.String()
}
