// Package schema lowers normalized ContractIR plus logical ModelIR into a
// deterministic provider-neutral GraphQL schema. It never inspects Go source,
// SQL names, or provider catalogs.
package schema

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type Document struct{ SDL string }

type modelView struct {
	model     ir.ModelDeclIR
	contract  ir.ModelContractIR
	fields    map[ir.FieldID]ir.FieldContractIR
	relations map[ir.RelationID]ir.RelationIR
}

func Build(compilation ir.CompilationIR) (Document, error) {
	if compilation.Contract.GraphQLABIVersion == 0 {
		return Document{}, fmt.Errorf("GraphQL ContractIR is not normalized")
	}
	models := make(map[ir.ModelID]ir.ModelDeclIR, len(compilation.Model.Models))
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	relations := make(map[ir.RelationID]ir.RelationIR, len(compilation.Model.Relations))
	for _, relation := range compilation.Model.Relations {
		relations[relation.ID] = relation
	}
	contracts := append([]ir.ModelContractIR(nil), compilation.Contract.Models...)
	sort.Slice(contracts, func(i, j int) bool {
		if contracts[i].GraphQLName != contracts[j].GraphQLName {
			return contracts[i].GraphQLName < contracts[j].GraphQLName
		}
		return contracts[i].ModelID < contracts[j].ModelID
	})
	views := make([]modelView, 0, len(contracts))
	for _, contract := range contracts {
		if !contract.Exposed {
			continue
		}
		model, ok := models[contract.ModelID]
		if !ok {
			return Document{}, fmt.Errorf("GraphQL contract references unknown model %s", contract.ModelID)
		}
		fieldContracts := make(map[ir.FieldID]ir.FieldContractIR, len(contract.Fields))
		for _, field := range contract.Fields {
			fieldContracts[field.FieldID] = field
		}
		views = append(views, modelView{model: model, contract: contract, fields: fieldContracts, relations: relations})
	}
	contractByModel := make(map[ir.ModelID]ir.ModelContractIR, len(contracts))
	for _, value := range contracts {
		contractByModel[value.ModelID] = value
	}
	enumByID := make(map[ir.EnumID]ir.EnumContractIR, len(compilation.Contract.Enums))
	for _, value := range compilation.Contract.Enums {
		enumByID[value.EnumID] = value
	}
	var sections []string
	sections = append(sections, prelude())
	for _, enum := range sortedEnums(compilation.Contract.Enums) {
		sections = append(sections, renderEnum(enum))
	}
	for _, view := range views {
		section, err := renderModel(view, models, contractByModel, enumByID)
		if err != nil {
			return Document{}, err
		}
		sections = append(sections, section...)
	}
	withoutInputs, err := allRelationWithoutInputTypes(views, models, contractByModel, enumByID, relations)
	if err != nil {
		return Document{}, err
	}
	sections = append(sections, withoutInputs...)
	sections = append(sections, allRelationFilterTypes(views, contractByModel)...)
	query, mutation, err := renderRoots(views, compilation.Contract.CustomOperations)
	if err != nil {
		return Document{}, err
	}
	if query == "" {
		return Document{}, fmt.Errorf("generated GraphQL schema requires at least one enabled query root")
	}
	sections = append(sections, query)
	if mutation != "" {
		sections = append(sections, mutation, "type BatchPayload {\n  count: Int!\n}")
	}
	return Document{SDL: strings.Join(sections, "\n\n") + "\n"}, nil
}

func prelude() string {
	base := `directive @oneOf on INPUT_OBJECT

scalar BigInt
scalar Decimal
scalar UUID
scalar Date
scalar Time
scalar DateTime
scalar Bytes
scalar JSON

enum SortOrder { asc desc }

enum QueryMode { sensitive insensitive }`
	var out strings.Builder
	out.WriteString(base)
	for _, scalar := range scalarDefinitions() {
		fmt.Fprintf(&out, "\n%s", renderScalarFilter(scalar, false))
		fmt.Fprintf(&out, "\n%s", renderScalarFilter(scalar, true))
		fmt.Fprintf(&out, "\n%s", renderUpdateEnvelope(scalar.name, scalar.name, scalar.numeric, false))
		fmt.Fprintf(&out, "\n%s", renderUpdateEnvelope("Nullable"+scalar.name, scalar.name, scalar.numeric, true))
	}
	for _, scalar := range listElementDefinitions() {
		fmt.Fprintf(&out, "\n%s", renderListFilter(scalar.name, scalar.name, false))
		fmt.Fprintf(&out, "\n%s", renderListFilter("Nullable"+scalar.name, scalar.name, true))
		fmt.Fprintf(&out, "\n%s", renderUpdateEnvelope(scalar.name+"List", "["+scalar.name+"!]", false, false))
		fmt.Fprintf(&out, "\n%s", renderUpdateEnvelope("Nullable"+scalar.name+"List", "["+scalar.name+"!]", false, true))
	}
	fmt.Fprintf(&out, "\n%s", renderJSONFilter(false))
	fmt.Fprintf(&out, "\n%s", renderJSONFilter(true))
	fmt.Fprintf(&out, "\n%s", renderUpdateEnvelope("JSON", "JSON", false, false))
	fmt.Fprintf(&out, "\n%s", renderUpdateEnvelope("NullableJSON", "JSON", false, true))
	return out.String()
}

type scalarDefinition struct {
	name    string
	ordered bool
	text    bool
	numeric bool
}

func scalarDefinitions() []scalarDefinition {
	return []scalarDefinition{
		{name: "Boolean"},
		{name: "Int", ordered: true, numeric: true},
		{name: "BigInt", ordered: true, numeric: true},
		{name: "Float", ordered: true, numeric: true},
		{name: "Decimal", ordered: true, numeric: true},
		{name: "String", ordered: true, text: true},
		{name: "UUID"},
		{name: "Date", ordered: true},
		{name: "Time", ordered: true},
		{name: "DateTime", ordered: true},
		{name: "Bytes"},
	}
}

// Scalar-list operators are portable only for the element kinds accepted by
// P2's closed operator registry. Bytes and JSON are intentionally absent.
func listElementDefinitions() []scalarDefinition {
	values := scalarDefinitions()
	result := make([]scalarDefinition, 0, len(values)-1)
	for _, value := range values {
		if value.name != "Bytes" {
			result = append(result, value)
		}
	}
	return result
}

func renderScalarFilter(definition scalarDefinition, nullable bool) string {
	name := definition.name
	if nullable {
		name = "Nullable" + name
	}
	fields := []string{
		"equals: " + definition.name,
		"not: " + definition.name,
		"in: [" + definition.name + "!]",
		"notIn: [" + definition.name + "!]",
	}
	if definition.ordered {
		fields = append(fields, "lt: "+definition.name, "lte: "+definition.name, "gt: "+definition.name, "gte: "+definition.name)
	}
	if definition.text {
		fields = append(fields, "contains: String", "startsWith: String", "endsWith: String", "mode: QueryMode")
	}
	if nullable {
		fields = append(fields, "isNull: Boolean")
	}
	return fmt.Sprintf("input %sFilter { %s }", name, strings.Join(fields, " "))
}

func renderListFilter(name, element string, nullable bool) string {
	fields := []string{"equals: [" + element + "!]", "has: " + element, "hasEvery: [" + element + "!]", "hasSome: [" + element + "!]", "isEmpty: Boolean"}
	if nullable {
		fields = append(fields, "isNull: Boolean")
	}
	return fmt.Sprintf("input %sListFilter { %s }", name, strings.Join(fields, " "))
}

func renderJSONFilter(nullable bool) string {
	name := "JSON"
	if nullable {
		name = "NullableJSON"
	}
	fields := []string{
		"equals: JSON", "not: JSON", "lt: JSON", "lte: JSON", "gt: JSON", "gte: JSON",
		"stringContains: String", "stringStartsWith: String", "stringEndsWith: String",
		"arrayContains: JSON", "arrayStartsWith: JSON", "arrayEndsWith: JSON",
		"path: [String!]", "mode: QueryMode",
	}
	if nullable {
		fields = append(fields, "isNull: Boolean")
	}
	return fmt.Sprintf("input %sFilter { %s }", name, strings.Join(fields, " "))
}

func renderUpdateEnvelope(name, value string, numeric, nullable bool) string {
	fields := []string{"set: " + value}
	if nullable {
		fields = append(fields, "setNull: Boolean")
	}
	if numeric {
		fields = append(fields, "increment: "+value, "decrement: "+value)
	}
	return fmt.Sprintf("input %sUpdateOperationsInput @oneOf { %s }", name, strings.Join(fields, " "))
}

func sortedEnums(values []ir.EnumContractIR) []ir.EnumContractIR {
	result := append([]ir.EnumContractIR(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].GraphQLName != result[j].GraphQLName {
			return result[i].GraphQLName < result[j].GraphQLName
		}
		return result[i].EnumID < result[j].EnumID
	})
	return result
}

func renderEnum(enum ir.EnumContractIR) string {
	values := append([]ir.EnumValueContractIR(nil), enum.Values...)
	sort.Slice(values, func(i, j int) bool { return values[i].GraphQLName < values[j].GraphQLName })
	var out strings.Builder
	fmt.Fprintf(&out, "enum %s {\n", enum.GraphQLName)
	for _, value := range values {
		fmt.Fprintf(&out, "  %s\n", value.GraphQLName)
	}
	out.WriteString("}")
	fmt.Fprintf(&out, "\n\ninput %sFilter { equals: %s not: %s in: [%s!] notIn: [%s!] }", enum.GraphQLName, enum.GraphQLName, enum.GraphQLName, enum.GraphQLName, enum.GraphQLName)
	fmt.Fprintf(&out, "\n\ninput Nullable%sFilter { equals: %s not: %s in: [%s!] notIn: [%s!] isNull: Boolean }", enum.GraphQLName, enum.GraphQLName, enum.GraphQLName, enum.GraphQLName, enum.GraphQLName)
	fmt.Fprintf(&out, "\n\n%s", renderUpdateEnvelope(enum.GraphQLName, enum.GraphQLName, false, false))
	fmt.Fprintf(&out, "\n\n%s", renderUpdateEnvelope("Nullable"+enum.GraphQLName, enum.GraphQLName, false, true))
	fmt.Fprintf(&out, "\n\n%s", renderListFilter(enum.GraphQLName, enum.GraphQLName, false))
	fmt.Fprintf(&out, "\n\n%s", renderListFilter("Nullable"+enum.GraphQLName, enum.GraphQLName, true))
	fmt.Fprintf(&out, "\n\n%s", renderUpdateEnvelope(enum.GraphQLName+"List", "["+enum.GraphQLName+"!]", false, false))
	fmt.Fprintf(&out, "\n\n%s", renderUpdateEnvelope("Nullable"+enum.GraphQLName+"List", "["+enum.GraphQLName+"!]", false, true))
	return out.String()
}

func renderModel(view modelView, models map[ir.ModelID]ir.ModelDeclIR, contracts map[ir.ModelID]ir.ModelContractIR, enums map[ir.EnumID]ir.EnumContractIR) ([]string, error) {
	name := view.contract.GraphQLName
	relationCreateFields, relationUpdateFields, err := relationOwnedMutationFields(view.model, view.contract, models, view.relations, nil)
	if err != nil {
		return nil, err
	}
	var output, count, where, order, scalarEnum, unique, create, update, updateMany strings.Builder
	fmt.Fprintf(&output, "type %s {\n", name)
	fmt.Fprintf(&where, "input %sWhereInput {\n  all: Boolean\n  AND: [%sWhereInput!]\n  OR: [%sWhereInput!]\n  NOT: [%sWhereInput!]\n", name, name, name, name)
	fmt.Fprintf(&order, "input %sOrderByInput {\n", name)
	fmt.Fprintf(&scalarEnum, "enum %sScalarField {\n", name)
	fmt.Fprintf(&unique, "input %sWhereUniqueInput @oneOf {\n", name)
	fmt.Fprintf(&create, "input %sCreateInput {\n", name)
	fmt.Fprintf(&update, "input %sUpdateInput {\n", name)
	fmt.Fprintf(&updateMany, "input %sUpdateManyInput {\n", name)
	var countFields []string
	readScalarCount := 0
	createCount, updateCount, updateManyCount := 0, 0, 0
	fields := append([]ir.FieldIR(nil), view.model.Fields...)
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].DeclarationOrder != fields[j].DeclarationOrder {
			return fields[i].DeclarationOrder < fields[j].DeclarationOrder
		}
		return fields[i].ID < fields[j].ID
	})
	for _, field := range fields {
		fc, ok := view.fields[field.ID]
		if !ok {
			return nil, fmt.Errorf("model %s field %s has no GraphQL contract", name, field.ID)
		}
		if hidden(fc.Modes) {
			continue
		}
		readable := !hasMode(fc.Modes, ir.ModeWriteOnly)
		writableCreate := !hasMode(fc.Modes, ir.ModeReadOnly)
		writableUpdate := writableCreate && !hasMode(fc.Modes, ir.ModeImmutable)
		if field.Kind == ir.FieldRelation {
			relation := relationForField(field, view.relations)
			if relation == nil {
				return nil, fmt.Errorf("model %s relation field %s has no relation edge", name, field.ID)
			}
			targetID := relation.TargetModel
			if field.Relation.Role == ir.RelationInverse {
				targetID = relation.SourceModel
			}
			target, exists := contracts[targetID]
			if !exists || !target.Exposed {
				return nil, fmt.Errorf("exposed relation %s.%s targets hidden model %s", name, fc.GraphQLName, targetID)
			}
			many := field.Relation.Kind == ir.RelationHasMany
			if readable {
				if many {
					fmt.Fprintf(&output, "  %s(where: %sWhereInput, orderBy: [%sOrderByInput!], cursor: %sWhereUniqueInput, distinct: [%sScalarField!], skip: Int, take: Int = %d): [%s!]\n", fc.GraphQLName, target.GraphQLName, target.GraphQLName, target.GraphQLName, target.GraphQLName, view.contract.Limits.DefaultPageSize, target.GraphQLName)
					countFields = append(countFields, fmt.Sprintf("  %s(where: %sWhereInput): Int", fc.GraphQLName, target.GraphQLName))
					fmt.Fprintf(&where, "  %s: %sListRelationFilter\n", fc.GraphQLName, target.GraphQLName)
				} else {
					fmt.Fprintf(&output, "  %s: %s\n", fc.GraphQLName, target.GraphQLName)
					fmt.Fprintf(&where, "  %s: %sRelationFilter\n", fc.GraphQLName, target.GraphQLName)
				}
			}
			capabilities, capabilityErr := relationMutationCapabilitiesFor(field, view.model, view.contract, models, view.relations)
			if capabilityErr != nil {
				return nil, capabilityErr
			}
			if capabilities.create {
				required := capabilities.required && !capabilities.many && !capabilities.inverse
				fmt.Fprintf(&create, "  %s: %s%sCreateRelationInput%s\n", fc.GraphQLName, name, exported(fc.GraphQLName), bang(required))
				createCount++
			}
			if capabilities.update {
				fmt.Fprintf(&update, "  %s: %s%sUpdateRelationInput\n", fc.GraphQLName, name, exported(fc.GraphQLName))
				updateCount++
			}
			continue
		}
		writableCreate = writableCreate && !field.Scalar.DatabaseReadOnly && field.Scalar.Generation == nil
		writableUpdate = writableUpdate && !field.Scalar.DatabaseReadOnly && field.Scalar.Generation == nil
		base, filter, updateType, scalarErr := scalarGraphQL(field.Scalar.Type, enums)
		if scalarErr != nil {
			return nil, fmt.Errorf("model %s field %s: %w", name, fc.GraphQLName, scalarErr)
		}
		if field.Scalar.Nullable {
			filter = "Nullable" + filter
			updateType = "Nullable" + updateType
		}
		if readable {
			fmt.Fprintf(&output, "  %s: %s\n", fc.GraphQLName, base)
			fmt.Fprintf(&where, "  %s: %s\n", fc.GraphQLName, filter)
			fmt.Fprintf(&order, "  %s: SortOrder\n", fc.GraphQLName)
			fmt.Fprintf(&scalarEnum, "  %s\n", fc.GraphQLName)
			readScalarCount++
		}
		if writableCreate && !relationCreateFields[field.ID] {
			required := field.Scalar != nil && !field.Scalar.Nullable && field.Scalar.Default == nil
			fmt.Fprintf(&create, "  %s: %s%s\n", fc.GraphQLName, base, bang(required))
			createCount++
		}
		if writableUpdate {
			if !relationUpdateFields[field.ID] {
				fmt.Fprintf(&update, "  %s: %s\n", fc.GraphQLName, updateType)
				updateCount++
			}
			fmt.Fprintf(&updateMany, "  %s: %s\n", fc.GraphQLName, updateType)
			updateManyCount++
		}
	}
	computed := append([]ir.ComputedFieldContractIR(nil), view.contract.Computed...)
	sort.Slice(computed, func(i, j int) bool {
		if computed[i].Name != computed[j].Name {
			return computed[i].Name < computed[j].Name
		}
		return computed[i].ExtensionID < computed[j].ExtensionID
	})
	for _, field := range computed {
		result, typeErr := graphQLTypeSDL(field.Result)
		if typeErr != nil {
			return nil, fmt.Errorf("model %s computed field %s: %w", name, field.Name, typeErr)
		}
		arguments, argumentErr := renderGraphQLArguments(field.Arguments)
		if argumentErr != nil {
			return nil, fmt.Errorf("model %s computed field %s: %w", name, field.Name, argumentErr)
		}
		fmt.Fprintf(&output, "  %s%s: %s\n", field.Name, arguments, result)
	}
	if len(countFields) > 0 {
		fmt.Fprintf(&output, "  _count: %sCountOutput\n", name)
		fmt.Fprintf(&count, "type %sCountOutput {\n%s\n}", name, strings.Join(countFields, "\n"))
	}
	output.WriteString("}")
	where.WriteString("}")
	order.WriteString("}")
	scalarEnum.WriteString("}")
	create.WriteString("}")
	update.WriteString("}")
	updateMany.WriteString("}")
	compound := []string{}
	selectors := append([]ir.SelectorContractIR(nil), view.contract.Selectors...)
	sort.Slice(selectors, func(i, j int) bool {
		if selectors[i].Name != selectors[j].Name {
			return selectors[i].Name < selectors[j].Name
		}
		return selectors[i].KeyID < selectors[j].KeyID
	})
	for _, selector := range selectors {
		if len(selector.Fields) == 1 {
			field := fieldByID(view.model, selector.Fields[0])
			fc := view.fields[selector.Fields[0]]
			if field == nil || field.Scalar == nil || hidden(fc.Modes) || hasMode(fc.Modes, ir.ModeWriteOnly) {
				return nil, fmt.Errorf("selector %s.%s contains an unexposed field", name, selector.Name)
			}
			base, _, _, err := scalarGraphQL(field.Scalar.Type, enums)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&unique, "  %s: %s\n", selector.Name, base)
		} else {
			compoundName := name + exported(selector.Name) + "CompoundUniqueInput"
			var body strings.Builder
			fmt.Fprintf(&body, "input %s {\n", compoundName)
			for _, id := range selector.Fields {
				field := fieldByID(view.model, id)
				fc := view.fields[id]
				if field == nil || field.Scalar == nil || hidden(fc.Modes) || hasMode(fc.Modes, ir.ModeWriteOnly) {
					return nil, fmt.Errorf("selector %s.%s contains an unexposed field", name, selector.Name)
				}
				base, _, _, err := scalarGraphQL(field.Scalar.Type, enums)
				if err != nil {
					return nil, err
				}
				fmt.Fprintf(&body, "  %s: %s!\n", fc.GraphQLName, base)
			}
			body.WriteString("}")
			compound = append(compound, body.String())
			fmt.Fprintf(&unique, "  %s: %s\n", selector.Name, compoundName)
		}
	}
	unique.WriteString("}")
	if len(view.contract.Selectors) == 0 {
		return nil, fmt.Errorf("model %s has no public unique selector", name)
	}
	sections := []string{output.String()}
	if count.Len() > 0 {
		sections = append(sections, count.String())
	}
	sections = append(sections, where.String())
	if readScalarCount > 0 {
		sections = append(sections, order.String(), scalarEnum.String())
	}
	sections = append(sections, unique.String())
	sections = append(sections, compound...)
	if createCount > 0 {
		sections = append(sections, create.String())
	}
	if updateCount > 0 {
		sections = append(sections, update.String())
	}
	if updateManyCount > 0 {
		sections = append(sections, updateMany.String())
	}
	sections = append(sections, relationMutationTypes(view, contracts, models)...)
	return sections, nil
}

func renderRoots(views []modelView, custom []ir.CustomOperationContractIR) (string, string, error) {
	var query, mutation bytes.Buffer
	query.WriteString("type Query {\n")
	mutation.WriteString("type Mutation {\n")
	queryCount, mutationCount := 0, 0
	for _, view := range views {
		enabled := map[ir.Operation]bool{}
		for _, operation := range view.contract.Operations {
			enabled[operation] = true
		}
		name, roots, limit := view.contract.GraphQLName, view.contract.Roots, view.contract.Limits.DefaultPageSize
		hasCreate, hasUpdate, hasUpdateMany := modelInputCapabilities(view.model, view.contract)
		if enabled[ir.OperationFindOne] {
			fmt.Fprintf(&query, "  %s(where: %sWhereUniqueInput!): %s\n", roots.FindOne, name, name)
			queryCount++
		}
		if enabled[ir.OperationFindMany] {
			fmt.Fprintf(&query, "  %s(where: %sWhereInput, orderBy: [%sOrderByInput!], cursor: %sWhereUniqueInput, distinct: [%sScalarField!], skip: Int, take: Int = %d): [%s!]!\n", roots.FindMany, name, name, name, name, limit, name)
			queryCount++
		}
		if enabled[ir.OperationCreate] && hasCreate {
			fmt.Fprintf(&mutation, "  %s(data: %sCreateInput!): %s!\n", roots.Create, name, name)
			mutationCount++
		}
		if enabled[ir.OperationUpdate] && hasUpdate {
			fmt.Fprintf(&mutation, "  %s(where: %sWhereUniqueInput!, data: %sUpdateInput!): %s!\n", roots.Update, name, name, name)
			mutationCount++
		}
		if enabled[ir.OperationUpsert] && hasCreate && hasUpdate {
			fmt.Fprintf(&mutation, "  %s(where: %sWhereUniqueInput!, create: %sCreateInput!, update: %sUpdateInput!): %s!\n", roots.Upsert, name, name, name, name)
			mutationCount++
		}
		if enabled[ir.OperationDelete] {
			fmt.Fprintf(&mutation, "  %s(where: %sWhereUniqueInput!): %s!\n", roots.Delete, name, name)
			mutationCount++
		}
		if enabled[ir.OperationUpdateMany] && hasUpdateMany {
			fmt.Fprintf(&mutation, "  %s(where: %sWhereInput!, data: %sUpdateManyInput!): BatchPayload!\n", roots.UpdateMany, name, name)
			mutationCount++
		}
		if enabled[ir.OperationDeleteMany] {
			fmt.Fprintf(&mutation, "  %s(where: %sWhereInput!): BatchPayload!\n", roots.DeleteMany, name)
			mutationCount++
		}
	}
	operations := append([]ir.CustomOperationContractIR(nil), custom...)
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].Operation != operations[j].Operation {
			return operations[i].Operation < operations[j].Operation
		}
		if operations[i].Name != operations[j].Name {
			return operations[i].Name < operations[j].Name
		}
		return operations[i].ExtensionID < operations[j].ExtensionID
	})
	for _, operation := range operations {
		result, err := graphQLTypeSDL(operation.Result)
		if err != nil {
			return "", "", fmt.Errorf("custom %s %s result: %w", operation.Operation, operation.Name, err)
		}
		arguments, err := renderGraphQLArguments(operation.Arguments)
		if err != nil {
			return "", "", fmt.Errorf("custom %s %s: %w", operation.Operation, operation.Name, err)
		}
		switch operation.Operation {
		case ir.CustomOperationQuery:
			fmt.Fprintf(&query, "  %s%s: %s\n", operation.Name, arguments, result)
			queryCount++
		case ir.CustomOperationMutation:
			fmt.Fprintf(&mutation, "  %s%s: %s\n", operation.Name, arguments, result)
			mutationCount++
		default:
			return "", "", fmt.Errorf("custom operation %s has unsupported kind %q", operation.Name, operation.Operation)
		}
	}
	query.WriteString("}")
	mutation.WriteString("}")
	if queryCount == 0 {
		return "", "", nil
	}
	if mutationCount == 0 {
		return query.String(), "", nil
	}
	return query.String(), mutation.String(), nil
}

func renderGraphQLArguments(arguments []ir.GraphQLArgumentContractIR) (string, error) {
	if len(arguments) == 0 {
		return "", nil
	}
	values := make([]string, len(arguments))
	for index, argument := range arguments {
		typeName, err := graphQLTypeSDL(argument.Type)
		if err != nil {
			return "", fmt.Errorf("argument %s: %w", argument.Name, err)
		}
		values[index] = argument.Name + ": " + typeName
	}
	return "(" + strings.Join(values, ", ") + ")", nil
}

func graphQLTypeSDL(value ir.GraphQLTypeIR) (string, error) {
	var base string
	switch value.Kind {
	case ir.GraphQLTypeScalar, ir.GraphQLTypeEnum, ir.GraphQLTypeModel:
		base = value.Name
	case ir.GraphQLTypePredicate:
		base = value.Name + "WhereInput"
	case ir.GraphQLTypeSelector:
		base = value.Name + "WhereUniqueInput"
	case ir.GraphQLTypeCreateInput:
		base = value.Name + "CreateInput"
	case ir.GraphQLTypeUpdateInput:
		base = value.Name + "UpdateInput"
	case ir.GraphQLTypeUpdateManyInput:
		base = value.Name + "UpdateManyInput"
	case ir.GraphQLTypeList:
		if value.Element == nil {
			return "", fmt.Errorf("list type has no element")
		}
		element, err := graphQLTypeSDL(*value.Element)
		if err != nil {
			return "", err
		}
		base = "[" + element + "]"
	default:
		return "", fmt.Errorf("unsupported GraphQL type kind %q", value.Kind)
	}
	if base == "" {
		return "", fmt.Errorf("GraphQL type name is empty")
	}
	if !value.Nullable {
		base += "!"
	}
	return base, nil
}

func allRelationFilterTypes(views []modelView, contracts map[ir.ModelID]ir.ModelContractIR) []string {
	seen := map[string]string{}
	for _, view := range views {
		for _, field := range view.model.Fields {
			if field.Kind != ir.FieldRelation {
				continue
			}
			fc := view.fields[field.ID]
			if hidden(fc.Modes) || hasMode(fc.Modes, ir.ModeWriteOnly) {
				continue
			}
			relation := relationForField(field, view.relations)
			if relation == nil {
				continue
			}
			targetID := relation.TargetModel
			if field.Relation.Role == ir.RelationInverse {
				targetID = relation.SourceModel
			}
			target := contracts[targetID]
			many := field.Relation.Kind == ir.RelationHasMany
			name := target.GraphQLName + "RelationFilter"
			body := fmt.Sprintf("input %s {\n  is: %sWhereInput\n  isNot: %sWhereInput\n}", name, target.GraphQLName, target.GraphQLName)
			if many {
				name = target.GraphQLName + "ListRelationFilter"
				body = fmt.Sprintf("input %s {\n  some: %sWhereInput\n  every: %sWhereInput\n  none: %sWhereInput\n}", name, target.GraphQLName, target.GraphQLName, target.GraphQLName)
			}
			seen[name] = body
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, len(names))
	for index, name := range names {
		result[index] = seen[name]
	}
	return result
}

type nestedInputNames struct {
	create              string
	update              string
	connectOrCreate     string
	updateWithWhere     string
	updateManyWithWhere string
	upsertWithWhere     string
	upsert              string
	excluded            []ir.FieldID
}

func relationNestedInputNames(view modelView, field ir.FieldIR, target ir.ModelContractIR) (nestedInputNames, error) {
	relation := relationForField(field, view.relations)
	if relation == nil {
		return nestedInputNames{}, fmt.Errorf("GraphQL relation field %s.%s has no relation", view.contract.GraphQLName, field.ID)
	}
	var backField ir.FieldID
	if field.Relation.Role == ir.RelationSource {
		if relation.InverseField != nil {
			backField = *relation.InverseField
		}
	} else {
		backField = relation.SourceField
	}
	suffix := ""
	if backField != "" {
		back, ok := fieldContractByID(target, backField)
		if !ok || back.GraphQLName == "" {
			return nestedInputNames{}, fmt.Errorf("GraphQL relation %s back field %s has no contract", relation.ID, backField)
		}
		suffix = exported(back.GraphQLName)
	} else {
		// A unidirectional edge has no immediate back field to remove. Retain a
		// relation-specific name so two independent edges cannot collide.
		ownerField, _ := fieldContractByID(view.contract, field.ID)
		suffix = exported(view.contract.GraphQLName) + exported(ownerField.GraphQLName)
	}
	prefix := target.GraphQLName
	without := "Without" + suffix + "Input"
	excluded := []ir.FieldID{}
	if backField != "" {
		excluded = append(excluded, backField)
	}
	if field.Relation.Role == ir.RelationInverse {
		excluded = append(excluded, relation.LocalFields...)
	}
	return nestedInputNames{
		create:              prefix + "Create" + without,
		update:              prefix + "Update" + without,
		connectOrCreate:     prefix + "ConnectOrCreate" + without,
		updateWithWhere:     prefix + "UpdateWithWhere" + without,
		updateManyWithWhere: prefix + "UpdateManyWithWhere" + without,
		upsertWithWhere:     prefix + "UpsertWithWhere" + without,
		upsert:              prefix + "Upsert" + without,
		excluded:            excluded,
	}, nil
}

func allRelationWithoutInputTypes(views []modelView, models map[ir.ModelID]ir.ModelDeclIR, contracts map[ir.ModelID]ir.ModelContractIR, enums map[ir.EnumID]ir.EnumContractIR, relations map[ir.RelationID]ir.RelationIR) ([]string, error) {
	types := map[string]string{}
	add := func(name, body string) error {
		if existing, exists := types[name]; exists && existing != body {
			return fmt.Errorf("GraphQL nested input type %s has conflicting relation definitions", name)
		}
		types[name] = body
		return nil
	}
	for _, view := range views {
		fields := append([]ir.FieldIR(nil), view.model.Fields...)
		sort.Slice(fields, func(i, j int) bool {
			if fields[i].DeclarationOrder != fields[j].DeclarationOrder {
				return fields[i].DeclarationOrder < fields[j].DeclarationOrder
			}
			return fields[i].ID < fields[j].ID
		})
		for _, field := range fields {
			if field.Relation == nil {
				continue
			}
			capabilities, err := relationMutationCapabilitiesFor(field, view.model, view.contract, models, relations)
			if err != nil {
				return nil, err
			}
			if !capabilities.create && !capabilities.update {
				continue
			}
			relation := relationForField(field, relations)
			targetID := relation.TargetModel
			if field.Relation.Role == ir.RelationInverse {
				targetID = relation.SourceModel
			}
			targetContract, ok := contracts[targetID]
			targetModel, modelOK := models[targetID]
			if !ok || !modelOK || !targetContract.Exposed {
				continue
			}
			names, err := relationNestedInputNames(view, field, targetContract)
			if err != nil {
				return nil, err
			}
			createBody, updateBody, hasCreate, hasUpdate, err := renderWithoutInputPair(targetModel, targetContract, names, models, relations, enums)
			if err != nil {
				return nil, err
			}
			if hasCreate {
				if err := add(names.create, createBody); err != nil {
					return nil, err
				}
			}
			if hasUpdate {
				if err := add(names.update, updateBody); err != nil {
					return nil, err
				}
			}
		}
	}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, len(names))
	for index, name := range names {
		result[index] = types[name]
	}
	return result, nil
}

func renderWithoutInputPair(model ir.ModelDeclIR, contract ir.ModelContractIR, names nestedInputNames, models map[ir.ModelID]ir.ModelDeclIR, relations map[ir.RelationID]ir.RelationIR, enums map[ir.EnumID]ir.EnumContractIR) (createBody, updateBody string, hasCreate, hasUpdate bool, err error) {
	relationCreateFields, relationUpdateFields, err := relationOwnedMutationFields(model, contract, models, relations, names.excluded)
	if err != nil {
		return "", "", false, false, err
	}
	var create, update strings.Builder
	fmt.Fprintf(&create, "input %s {\n", names.create)
	fmt.Fprintf(&update, "input %s {\n", names.update)
	fields := append([]ir.FieldIR(nil), model.Fields...)
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].DeclarationOrder != fields[j].DeclarationOrder {
			return fields[i].DeclarationOrder < fields[j].DeclarationOrder
		}
		return fields[i].ID < fields[j].ID
	})
	for _, field := range fields {
		if containsFieldID(names.excluded, field.ID) {
			continue
		}
		fc, ok := fieldContractByID(contract, field.ID)
		if !ok || hidden(fc.Modes) || hasMode(fc.Modes, ir.ModeReadOnly) {
			continue
		}
		if field.Relation != nil {
			capabilities, capabilityErr := relationMutationCapabilitiesFor(field, model, contract, models, relations)
			if capabilityErr != nil {
				return "", "", false, false, capabilityErr
			}
			prefix := contract.GraphQLName + exported(fc.GraphQLName)
			if capabilities.create {
				required := capabilities.required && !capabilities.many && !capabilities.inverse
				fmt.Fprintf(&create, "  %s: %sCreateRelationInput%s\n", fc.GraphQLName, prefix, bang(required))
				hasCreate = true
			}
			if capabilities.update {
				fmt.Fprintf(&update, "  %s: %sUpdateRelationInput\n", fc.GraphQLName, prefix)
				hasUpdate = true
			}
			continue
		}
		if field.Scalar == nil || field.Scalar.DatabaseReadOnly || field.Scalar.Generation != nil {
			continue
		}
		base, _, updateType, scalarErr := scalarGraphQL(field.Scalar.Type, enums)
		if scalarErr != nil {
			return "", "", false, false, scalarErr
		}
		if field.Scalar.Nullable {
			updateType = "Nullable" + updateType
		}
		required := !field.Scalar.Nullable && field.Scalar.Default == nil
		if !relationCreateFields[field.ID] {
			fmt.Fprintf(&create, "  %s: %s%s\n", fc.GraphQLName, base, bang(required))
			hasCreate = true
		}
		if !hasMode(fc.Modes, ir.ModeImmutable) && !relationUpdateFields[field.ID] {
			fmt.Fprintf(&update, "  %s: %s\n", fc.GraphQLName, updateType)
			hasUpdate = true
		}
	}
	create.WriteString("}")
	update.WriteString("}")
	return create.String(), update.String(), hasCreate, hasUpdate, nil
}

// relationOwnedMutationFields identifies scalar correlation fields whose
// values are authored exclusively through an emitted source relation envelope.
// Emitting both forms would make a valid GraphQL input freeze into the
// P4_NESTED_SHAPE overlap that P4 deliberately rejects. Excluded/read-only/
// hidden relation fields do not own an input position and therefore do not
// suppress their scalar correlation fields here.
func relationOwnedMutationFields(model ir.ModelDeclIR, contract ir.ModelContractIR, models map[ir.ModelID]ir.ModelDeclIR, relations map[ir.RelationID]ir.RelationIR, excluded []ir.FieldID) (map[ir.FieldID]bool, map[ir.FieldID]bool, error) {
	createFields := map[ir.FieldID]bool{}
	updateFields := map[ir.FieldID]bool{}
	for _, field := range model.Fields {
		if field.Relation == nil || containsFieldID(excluded, field.ID) {
			continue
		}
		relation, ok := relations[field.Relation.RelationID]
		if !ok {
			return nil, nil, fmt.Errorf("GraphQL relation %s is absent", field.Relation.RelationID)
		}
		if relation.SourceModel != model.ID || relation.SourceField != field.ID {
			continue
		}
		capabilities, err := relationMutationCapabilitiesFor(field, model, contract, models, relations)
		if err != nil {
			return nil, nil, err
		}
		for _, local := range relation.LocalFields {
			if capabilities.create {
				createFields[local] = true
			}
			if capabilities.update {
				updateFields[local] = true
			}
		}
	}
	return createFields, updateFields, nil
}

func relationMutationTypes(view modelView, contracts map[ir.ModelID]ir.ModelContractIR, models map[ir.ModelID]ir.ModelDeclIR) []string {
	var result []string
	fields := append([]ir.FieldIR(nil), view.model.Fields...)
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].DeclarationOrder != fields[j].DeclarationOrder {
			return fields[i].DeclarationOrder < fields[j].DeclarationOrder
		}
		return fields[i].ID < fields[j].ID
	})
	for _, field := range fields {
		if field.Kind != ir.FieldRelation {
			continue
		}
		fc := view.fields[field.ID]
		if hidden(fc.Modes) || hasMode(fc.Modes, ir.ModeReadOnly) {
			continue
		}
		relation := relationForField(field, view.relations)
		if relation == nil {
			continue
		}
		targetID := relation.TargetModel
		if field.Relation.Role == ir.RelationInverse {
			targetID = relation.SourceModel
		}
		target := contracts[targetID]
		targetModel, exists := models[targetID]
		if !exists || !target.Exposed {
			continue
		}
		capabilities, err := relationMutationCapabilitiesFor(field, view.model, view.contract, models, view.relations)
		if err != nil || (!capabilities.create && !capabilities.update) {
			continue
		}
		nestedNames, err := relationNestedInputNames(view, field, target)
		if err != nil {
			continue
		}
		targetCreate, targetUpdate, targetUpdateMany := modelInputCapabilitiesWithout(targetModel, target, nestedNames.excluded)
		prefix := view.contract.GraphQLName + exported(fc.GraphQLName)
		if capabilities.many {
			if capabilities.create {
				fields := []string{"  connect: [" + target.GraphQLName + "WhereUniqueInput!]"}
				if targetCreate {
					fields = append([]string{"  create: [" + nestedNames.create + "!]", "  createMany: [" + nestedNames.create + "!]"}, fields...)
					fields = append(fields, "  connectOrCreate: ["+nestedNames.connectOrCreate+"!]")
				}
				body := fmt.Sprintf("input %sCreateRelationInput {\n%s\n}", prefix, strings.Join(fields, "\n"))
				if targetCreate {
					body += fmt.Sprintf("\n\ninput %s {\n  where: %sWhereUniqueInput!\n  create: %s!\n}", nestedNames.connectOrCreate, target.GraphQLName, nestedNames.create)
				}
				result = append(result, body)
			}
			if capabilities.update {
				fields := []string{
					"  connect: [" + target.GraphQLName + "WhereUniqueInput!]",
					"  disconnect: [" + target.GraphQLName + "WhereUniqueInput!]",
					"  set: [" + target.GraphQLName + "WhereUniqueInput!]",
					"  delete: [" + target.GraphQLName + "WhereUniqueInput!]",
					"  deleteMany: [" + target.GraphQLName + "WhereInput!]",
				}
				var helpers []string
				if targetCreate {
					fields = append([]string{"  create: [" + nestedNames.create + "!]", "  createMany: [" + nestedNames.create + "!]"}, fields...)
					fields = append(fields, "  connectOrCreate: ["+nestedNames.connectOrCreate+"!]")
				}
				if targetUpdate {
					fields = append(fields, "  update: ["+nestedNames.updateWithWhere+"!]")
					helpers = append(helpers, fmt.Sprintf("input %s { where: %sWhereUniqueInput! data: %s! }", nestedNames.updateWithWhere, target.GraphQLName, nestedNames.update))
				}
				if targetUpdateMany {
					fields = append(fields, "  updateMany: ["+nestedNames.updateManyWithWhere+"!]")
					helpers = append(helpers, fmt.Sprintf("input %s { where: %sWhereInput! data: %sUpdateManyInput! }", nestedNames.updateManyWithWhere, target.GraphQLName, target.GraphQLName))
				}
				if targetCreate && targetUpdate {
					fields = append(fields, "  upsert: ["+nestedNames.upsertWithWhere+"!]")
					helpers = append(helpers, fmt.Sprintf("input %s { where: %sWhereUniqueInput! create: %s! update: %s! }", nestedNames.upsertWithWhere, target.GraphQLName, nestedNames.create, nestedNames.update))
				}
				body := fmt.Sprintf("input %sUpdateRelationInput {\n%s\n}", prefix, strings.Join(fields, "\n"))
				if len(helpers) > 0 {
					body += "\n\n" + strings.Join(helpers, "\n\n")
				}
				result = append(result, body)
			}
			continue
		}
		if capabilities.create {
			fields := []string{"  connect: " + target.GraphQLName + "WhereUniqueInput"}
			if targetCreate {
				fields = append([]string{"  create: " + nestedNames.create}, fields...)
				fields = append(fields, "  connectOrCreate: "+nestedNames.connectOrCreate)
			}
			body := fmt.Sprintf("input %sCreateRelationInput @oneOf {\n%s\n}", prefix, strings.Join(fields, "\n"))
			if targetCreate {
				body += fmt.Sprintf("\n\ninput %s { where: %sWhereUniqueInput! create: %s! }", nestedNames.connectOrCreate, target.GraphQLName, nestedNames.create)
			}
			result = append(result, body)
		}
		if capabilities.update {
			fields := []string{"  connect: " + target.GraphQLName + "WhereUniqueInput"}
			var helpers []string
			if targetCreate {
				fields = append([]string{"  create: " + nestedNames.create}, fields...)
				fields = append(fields, "  connectOrCreate: "+nestedNames.connectOrCreate)
			}
			if targetUpdate {
				fields = append(fields, "  update: "+nestedNames.update)
			}
			if targetCreate && targetUpdate {
				fields = append(fields, "  upsert: "+nestedNames.upsert)
				helpers = append(helpers, fmt.Sprintf("input %s { create: %s! update: %s! }", nestedNames.upsert, nestedNames.create, nestedNames.update))
			}
			if !capabilities.required {
				fields = append(fields, "  disconnect: Boolean")
			}
			if !capabilities.required || capabilities.inverse {
				fields = append(fields, "  delete: Boolean")
			}
			body := fmt.Sprintf("input %sUpdateRelationInput {\n%s\n}", prefix, strings.Join(fields, "\n"))
			if len(helpers) > 0 {
				body += "\n\n" + strings.Join(helpers, "\n\n")
			}
			result = append(result, body)
		}
	}
	return result
}

type relationMutationCapabilities struct {
	create   bool
	update   bool
	many     bool
	required bool
	inverse  bool
}

func relationMutationCapabilitiesFor(field ir.FieldIR, model ir.ModelDeclIR, contract ir.ModelContractIR, models map[ir.ModelID]ir.ModelDeclIR, relations map[ir.RelationID]ir.RelationIR) (relationMutationCapabilities, error) {
	if field.Relation == nil {
		return relationMutationCapabilities{}, nil
	}
	fc, ok := fieldContractByID(contract, field.ID)
	if !ok || hidden(fc.Modes) || hasMode(fc.Modes, ir.ModeReadOnly) {
		return relationMutationCapabilities{}, nil
	}
	result := relationMutationCapabilities{
		create:  true,
		update:  !hasMode(fc.Modes, ir.ModeImmutable),
		many:    field.Relation.Kind == ir.RelationHasMany,
		inverse: field.Relation.Role == ir.RelationInverse,
	}
	if result.many {
		return result, nil
	}
	relation, ok := relations[field.Relation.RelationID]
	if !ok {
		return relationMutationCapabilities{}, fmt.Errorf("GraphQL relation %s is absent", field.Relation.RelationID)
	}
	if len(relation.LocalFields) == 0 {
		result.required = true
		return result, nil
	}
	ownerID := model.ID
	if result.inverse {
		ownerID = relation.SourceModel
	}
	owner, ok := models[ownerID]
	if !ok {
		return relationMutationCapabilities{}, fmt.Errorf("GraphQL relation %s owner model %s is absent", relation.ID, ownerID)
	}
	ownerFields := make(map[ir.FieldID]ir.FieldIR, len(owner.Fields))
	for _, candidate := range owner.Fields {
		ownerFields[candidate.ID] = candidate
	}
	nullable, required := false, false
	for _, id := range relation.LocalFields {
		local, exists := ownerFields[id]
		if !exists || local.Scalar == nil {
			return relationMutationCapabilities{}, fmt.Errorf("GraphQL relation %s local field %s is unavailable", relation.ID, id)
		}
		if local.Scalar.Nullable {
			nullable = true
		} else {
			required = true
		}
	}
	if nullable && required {
		return relationMutationCapabilities{}, fmt.Errorf("GraphQL relation %s has mixed local nullability", relation.ID)
	}
	result.required = required
	return result, nil
}

func modelInputCapabilities(model ir.ModelDeclIR, contract ir.ModelContractIR) (create, update, updateMany bool) {
	return modelInputCapabilitiesWithout(model, contract, nil)
}

func modelInputCapabilitiesWithout(model ir.ModelDeclIR, contract ir.ModelContractIR, excluded []ir.FieldID) (create, update, updateMany bool) {
	for _, field := range model.Fields {
		if containsFieldID(excluded, field.ID) {
			continue
		}
		fc, ok := fieldContractByID(contract, field.ID)
		if !ok || hidden(fc.Modes) || hasMode(fc.Modes, ir.ModeReadOnly) {
			continue
		}
		if field.Scalar != nil {
			if field.Scalar.DatabaseReadOnly || field.Scalar.Generation != nil {
				continue
			}
			create = true
			if !hasMode(fc.Modes, ir.ModeImmutable) {
				update, updateMany = true, true
			}
			continue
		}
		if field.Relation != nil {
			create = true
			if !hasMode(fc.Modes, ir.ModeImmutable) {
				update = true
			}
		}
	}
	return create, update, updateMany
}

func containsFieldID(values []ir.FieldID, id ir.FieldID) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}

func fieldContractByID(contract ir.ModelContractIR, id ir.FieldID) (ir.FieldContractIR, bool) {
	for _, field := range contract.Fields {
		if field.FieldID == id {
			return field, true
		}
	}
	return ir.FieldContractIR{}, false
}

func scalarGraphQL(logical ir.LogicalTypeIR, enums map[ir.EnumID]ir.EnumContractIR) (base, filter, update string, err error) {
	switch logical.Kind {
	case ir.TypeBool:
		return "Boolean", "BooleanFilter", "BooleanUpdateOperationsInput", nil
	case ir.TypeInt16, ir.TypeInt32:
		return "Int", "IntFilter", "IntUpdateOperationsInput", nil
	case ir.TypeInt64:
		return "BigInt", "BigIntFilter", "BigIntUpdateOperationsInput", nil
	case ir.TypeFloat32, ir.TypeFloat64:
		return "Float", "FloatFilter", "FloatUpdateOperationsInput", nil
	case ir.TypeDecimal:
		return "Decimal", "DecimalFilter", "DecimalUpdateOperationsInput", nil
	case ir.TypeString:
		return "String", "StringFilter", "StringUpdateOperationsInput", nil
	case ir.TypeBytes:
		return "Bytes", "BytesFilter", "BytesUpdateOperationsInput", nil
	case ir.TypeUUID:
		return "UUID", "UUIDFilter", "UUIDUpdateOperationsInput", nil
	case ir.TypeDate:
		return "Date", "DateFilter", "DateUpdateOperationsInput", nil
	case ir.TypeTime:
		return "Time", "TimeFilter", "TimeUpdateOperationsInput", nil
	case ir.TypeDateTime:
		return "DateTime", "DateTimeFilter", "DateTimeUpdateOperationsInput", nil
	case ir.TypeJSON:
		return "JSON", "JSONFilter", "JSONUpdateOperationsInput", nil
	case ir.TypeEnum:
		if logical.EnumID == nil {
			return "", "", "", fmt.Errorf("enum type has no identity")
		}
		enum, ok := enums[*logical.EnumID]
		if !ok {
			return "", "", "", fmt.Errorf("unknown enum %s", *logical.EnumID)
		}
		return enum.GraphQLName, enum.GraphQLName + "Filter", enum.GraphQLName + "UpdateOperationsInput", nil
	case ir.TypeScalarList:
		if logical.Element == nil {
			return "", "", "", fmt.Errorf("scalar list has no element")
		}
		element, _, _, nestedErr := scalarGraphQL(*logical.Element, enums)
		if nestedErr != nil {
			return "", "", "", nestedErr
		}
		return "[" + element + "!]", element + "ListFilter", element + "ListUpdateOperationsInput", nil
	default:
		return "", "", "", fmt.Errorf("unsupported logical GraphQL type %s", logical.Kind)
	}
}

func relationForField(field ir.FieldIR, relations map[ir.RelationID]ir.RelationIR) *ir.RelationIR {
	if field.Relation == nil {
		return nil
	}
	value, ok := relations[field.Relation.RelationID]
	if !ok {
		return nil
	}
	return &value
}

func fieldByID(model ir.ModelDeclIR, id ir.FieldID) *ir.FieldIR {
	for index := range model.Fields {
		if model.Fields[index].ID == id {
			return &model.Fields[index]
		}
	}
	return nil
}
func hasMode(values []ir.FieldMode, mode ir.FieldMode) bool {
	for _, value := range values {
		if value == mode {
			return true
		}
	}
	return false
}
func hidden(values []ir.FieldMode) bool { return hasMode(values, ir.ModeHidden) }
func exported(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
func bang(value bool) string {
	if value {
		return "!"
	}
	return ""
}
