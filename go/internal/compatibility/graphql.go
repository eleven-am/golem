package compatibility

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

const GraphQLInventoryFormatVersion uint16 = 1

type GraphQLInventory struct {
	FormatVersion uint16              `json:"formatVersion"`
	Roots         []GraphQLRoot       `json:"roots"`
	Definitions   []GraphQLDefinition `json:"definitions"`
	Directives    []GraphQLDirective  `json:"directives"`
}

type GraphQLRoot struct {
	Operation string `json:"operation"`
	Type      string `json:"type"`
}

type GraphQLDefinition struct {
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Interfaces []string       `json:"interfaces"`
	Types      []string       `json:"types"`
	EnumValues []string       `json:"enumValues"`
	Fields     []GraphQLField `json:"fields"`
	Directives []string       `json:"directives"`
}

type GraphQLField struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Default    *string           `json:"default"`
	Arguments  []GraphQLArgument `json:"arguments"`
	Directives []string          `json:"directives"`
}

type GraphQLArgument struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Default    *string  `json:"default"`
	Directives []string `json:"directives"`
}

type GraphQLDirective struct {
	Name       string            `json:"name"`
	Repeatable bool              `json:"repeatable"`
	Locations  []string          `json:"locations"`
	Arguments  []GraphQLArgument `json:"arguments"`
}

func BuildGraphQLInventory(source []byte) (GraphQLInventory, error) {
	document, err := parser.ParseSchema(&ast.Source{Name: "compatibility.graphqls", Input: string(source)})
	if err != nil {
		return GraphQLInventory{}, errorsNoDetails("parse GraphQL inventory")
	}
	result := GraphQLInventory{
		FormatVersion: GraphQLInventoryFormatVersion,
		Roots:         []GraphQLRoot{}, Definitions: []GraphQLDefinition{}, Directives: []GraphQLDirective{},
	}
	for _, schema := range append(append(ast.SchemaDefinitionList(nil), document.Schema...), document.SchemaExtension...) {
		for _, operation := range schema.OperationTypes {
			result.Roots = append(result.Roots, GraphQLRoot{Operation: string(operation.Operation), Type: operation.Type})
		}
	}
	for _, definition := range append(append(ast.DefinitionList(nil), document.Definitions...), document.Extensions...) {
		value := GraphQLDefinition{
			Name: definition.Name, Kind: string(definition.Kind),
			Interfaces: sortedStrings(definition.Interfaces), Types: sortedStrings(definition.Types),
			EnumValues: []string{}, Fields: []GraphQLField{}, Directives: graphqlDirectiveUses(definition.Directives),
		}
		for _, enum := range definition.EnumValues {
			value.EnumValues = append(value.EnumValues, enum.Name+directiveSuffix(enum.Directives))
		}
		sort.Strings(value.EnumValues)
		for _, field := range definition.Fields {
			value.Fields = append(value.Fields, GraphQLField{
				Name: field.Name, Type: field.Type.String(), Default: graphqlDefault(field.DefaultValue),
				Arguments: graphqlArguments(field.Arguments), Directives: graphqlDirectiveUses(field.Directives),
			})
		}
		sort.Slice(value.Fields, func(i, j int) bool { return value.Fields[i].Name < value.Fields[j].Name })
		result.Definitions = append(result.Definitions, value)
	}
	merged := make(map[string]GraphQLDefinition, len(result.Definitions))
	for _, value := range result.Definitions {
		current, exists := merged[value.Name]
		if !exists {
			merged[value.Name] = value
			continue
		}
		if current.Kind != value.Kind {
			return GraphQLInventory{}, fail(ReasonInvalidManifest)
		}
		current.Interfaces = mergeStrings(current.Interfaces, value.Interfaces)
		current.Types = mergeStrings(current.Types, value.Types)
		current.EnumValues = append(current.EnumValues, value.EnumValues...)
		current.Fields = append(current.Fields, value.Fields...)
		current.Directives = mergeStrings(current.Directives, value.Directives)
		sort.Strings(current.EnumValues)
		sort.Slice(current.Fields, func(i, j int) bool { return current.Fields[i].Name < current.Fields[j].Name })
		merged[value.Name] = current
	}
	result.Definitions = result.Definitions[:0]
	for _, value := range merged {
		result.Definitions = append(result.Definitions, value)
	}
	if len(result.Roots) == 0 {
		declared := make(map[string]bool, len(result.Definitions))
		for _, definition := range result.Definitions {
			declared[definition.Name] = true
		}
		for _, candidate := range []GraphQLRoot{{Operation: "mutation", Type: "Mutation"}, {Operation: "query", Type: "Query"}, {Operation: "subscription", Type: "Subscription"}} {
			if declared[candidate.Type] {
				result.Roots = append(result.Roots, candidate)
			}
		}
	}
	for _, directive := range document.Directives {
		locations := make([]string, len(directive.Locations))
		for index, location := range directive.Locations {
			locations[index] = string(location)
		}
		sort.Strings(locations)
		result.Directives = append(result.Directives, GraphQLDirective{
			Name: directive.Name, Repeatable: directive.IsRepeatable,
			Locations: locations, Arguments: graphqlArguments(directive.Arguments),
		})
	}
	sort.Slice(result.Roots, func(i, j int) bool { return result.Roots[i].Operation < result.Roots[j].Operation })
	sort.Slice(result.Definitions, func(i, j int) bool {
		if result.Definitions[i].Name != result.Definitions[j].Name {
			return result.Definitions[i].Name < result.Definitions[j].Name
		}
		return result.Definitions[i].Kind < result.Definitions[j].Kind
	})
	sort.Slice(result.Directives, func(i, j int) bool { return result.Directives[i].Name < result.Directives[j].Name })
	if !canonicalGraphQL(result) {
		return GraphQLInventory{}, fail(ReasonInvalidManifest)
	}
	return result, nil
}

func EncodeGraphQLInventory(value GraphQLInventory) ([]byte, error) {
	if value.FormatVersion != GraphQLInventoryFormatVersion || !canonicalGraphQL(value) {
		return nil, fail(ReasonInvalidManifest)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail(ReasonInvalidEncoding)
	}
	return append(encoded, '\n'), nil
}

func ParseGraphQLInventory(encoded []byte) (GraphQLInventory, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value GraphQLInventory
	if err := decoder.Decode(&value); err != nil {
		return GraphQLInventory{}, fail(ReasonInvalidEncoding)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return GraphQLInventory{}, fail(ReasonInvalidEncoding)
	}
	canonical, err := EncodeGraphQLInventory(value)
	if err != nil {
		return GraphQLInventory{}, err
	}
	if !bytes.Equal(canonical, encoded) {
		return GraphQLInventory{}, fail(ReasonNoncanonical)
	}
	return value, nil
}

func CompareGraphQL(previous, current GraphQLInventory) LayerChange {
	if !canonicalGraphQL(previous) || !canonicalGraphQL(current) {
		return LayerBreaking
	}
	change := compareNamed(previous.Roots, current.Roots, func(value GraphQLRoot) string { return value.Operation })
	if change == LayerBreaking {
		return change
	}
	definitions := make(map[string]GraphQLDefinition, len(current.Definitions))
	for _, value := range current.Definitions {
		definitions[value.Name] = value
	}
	seen := make(map[string]bool, len(previous.Definitions))
	for _, before := range previous.Definitions {
		seen[before.Name] = true
		after, exists := definitions[before.Name]
		if !exists || before.Kind != after.Kind || !equalStrings(before.Interfaces, after.Interfaces) || !equalStrings(before.Directives, after.Directives) {
			return LayerBreaking
		}
		classified := compareGraphQLDefinition(before, after)
		if classified == LayerBreaking {
			return LayerBreaking
		}
		if classified == LayerAdditive {
			change = LayerAdditive
		}
	}
	for _, value := range current.Definitions {
		if !seen[value.Name] {
			change = LayerAdditive
		}
	}
	if classified := compareDirectives(previous.Directives, current.Directives); classified == LayerBreaking {
		return LayerBreaking
	} else if classified == LayerAdditive {
		change = LayerAdditive
	}
	return change
}

func compareGraphQLDefinition(previous, current GraphQLDefinition) LayerChange {
	change := LayerUnchanged
	if classified := additiveStrings(previous.Types, current.Types); classified == LayerBreaking {
		return LayerBreaking
	} else if classified == LayerAdditive {
		change = LayerAdditive
	}
	if classified := additiveStrings(previous.EnumValues, current.EnumValues); classified == LayerBreaking {
		return LayerBreaking
	} else if classified == LayerAdditive {
		change = LayerAdditive
	}
	fields := make(map[string]GraphQLField, len(current.Fields))
	for _, field := range current.Fields {
		fields[field.Name] = field
	}
	seen := make(map[string]bool, len(previous.Fields))
	for _, before := range previous.Fields {
		seen[before.Name] = true
		after, exists := fields[before.Name]
		if !exists || before.Type != after.Type || !equalOptional(before.Default, after.Default) || !equalStrings(before.Directives, after.Directives) {
			return LayerBreaking
		}
		classified := compareGraphQLArguments(before.Arguments, after.Arguments)
		if classified == LayerBreaking {
			return LayerBreaking
		}
		if classified == LayerAdditive {
			change = LayerAdditive
		}
	}
	for _, field := range current.Fields {
		if seen[field.Name] {
			continue
		}
		if previous.Kind == string(ast.InputObject) && requiredGraphQLType(field.Type) && field.Default == nil {
			return LayerBreaking
		}
		change = LayerAdditive
	}
	return change
}

func compareGraphQLArguments(previous, current []GraphQLArgument) LayerChange {
	change := LayerUnchanged
	values := make(map[string]GraphQLArgument, len(current))
	for _, value := range current {
		values[value.Name] = value
	}
	seen := make(map[string]bool, len(previous))
	for _, before := range previous {
		seen[before.Name] = true
		after, exists := values[before.Name]
		if !exists || before.Type != after.Type || !equalOptional(before.Default, after.Default) || !equalStrings(before.Directives, after.Directives) {
			return LayerBreaking
		}
	}
	for _, value := range current {
		if seen[value.Name] {
			continue
		}
		if requiredGraphQLType(value.Type) && value.Default == nil {
			return LayerBreaking
		}
		change = LayerAdditive
	}
	return change
}

func compareDirectives(previous, current []GraphQLDirective) LayerChange {
	values := make(map[string]GraphQLDirective, len(current))
	for _, value := range current {
		values[value.Name] = value
	}
	seen := make(map[string]bool, len(previous))
	change := LayerUnchanged
	for _, before := range previous {
		seen[before.Name] = true
		after, exists := values[before.Name]
		if !exists || before.Repeatable && !after.Repeatable {
			return LayerBreaking
		}
		if !before.Repeatable && after.Repeatable {
			change = LayerAdditive
		}
		if classified := additiveStrings(before.Locations, after.Locations); classified == LayerBreaking {
			return LayerBreaking
		} else if classified == LayerAdditive {
			change = LayerAdditive
		}
		if classified := compareGraphQLArguments(before.Arguments, after.Arguments); classified == LayerBreaking {
			return LayerBreaking
		} else if classified == LayerAdditive {
			change = LayerAdditive
		}
	}
	for _, value := range current {
		if !seen[value.Name] {
			change = LayerAdditive
		}
	}
	return change
}

func graphqlArguments(values ast.ArgumentDefinitionList) []GraphQLArgument {
	result := make([]GraphQLArgument, len(values))
	for index, value := range values {
		result[index] = GraphQLArgument{
			Name: value.Name, Type: value.Type.String(), Default: graphqlDefault(value.DefaultValue),
			Directives: graphqlDirectiveUses(value.Directives),
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func graphqlDefault(value *ast.Value) *string {
	if value == nil {
		return nil
	}
	result := canonicalGraphQLValue(value)
	return &result
}

func graphqlDirectiveUses(values ast.DirectiveList) []string {
	result := make([]string, len(values))
	for index, value := range values {
		arguments := make([]string, len(value.Arguments))
		for argumentIndex, argument := range value.Arguments {
			arguments[argumentIndex] = argument.Name + "=" + canonicalGraphQLValue(argument.Value)
		}
		sort.Strings(arguments)
		result[index] = value.Name + "(" + strings.Join(arguments, ",") + ")"
	}
	sort.Strings(result)
	return result
}

func canonicalGraphQLValue(value *ast.Value) string {
	if value == nil {
		return "<nil>"
	}
	switch value.Kind {
	case ast.ListValue:
		items := make([]string, len(value.Children))
		for index, child := range value.Children {
			items[index] = canonicalGraphQLValue(child.Value)
		}
		return "[" + strings.Join(items, ",") + "]"
	case ast.ObjectValue:
		items := make([]string, len(value.Children))
		for index, child := range value.Children {
			items[index] = child.Name + ":" + canonicalGraphQLValue(child.Value)
		}
		sort.Strings(items)
		return "{" + strings.Join(items, ",") + "}"
	default:
		return value.String()
	}
}

func mergeStrings(left, right []string) []string {
	seen := make(map[string]bool, len(left)+len(right))
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		seen[value] = true
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func directiveSuffix(values ast.DirectiveList) string {
	directives := graphqlDirectiveUses(values)
	if len(directives) == 0 {
		return ""
	}
	return "@" + strings.Join(directives, "@")
}

func requiredGraphQLType(value string) bool { return strings.HasSuffix(value, "!") }

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalOptional(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func additiveStrings(previous, current []string) LayerChange {
	set := make(map[string]bool, len(current))
	for _, value := range current {
		set[value] = true
	}
	seen := make(map[string]bool, len(previous))
	for _, value := range previous {
		seen[value] = true
		if !set[value] {
			return LayerBreaking
		}
	}
	for _, value := range current {
		if !seen[value] {
			return LayerAdditive
		}
	}
	return LayerUnchanged
}

func compareNamed[T any](previous, current []T, name func(T) string) LayerChange {
	values := make(map[string]T, len(current))
	for _, value := range current {
		values[name(value)] = value
	}
	for _, value := range previous {
		currentValue, exists := values[name(value)]
		if !exists || fmt.Sprint(value) != fmt.Sprint(currentValue) {
			return LayerBreaking
		}
	}
	if len(current) > len(previous) {
		return LayerAdditive
	}
	return LayerUnchanged
}

func canonicalGraphQL(value GraphQLInventory) bool {
	if value.FormatVersion != GraphQLInventoryFormatVersion {
		return false
	}
	for index, root := range value.Roots {
		if root.Operation == "" || root.Type == "" || index > 0 && value.Roots[index-1].Operation >= root.Operation {
			return false
		}
	}
	for index, definition := range value.Definitions {
		if definition.Name == "" || definition.Kind == "" || index > 0 && value.Definitions[index-1].Name >= definition.Name || !strictStrings(definition.Interfaces) || !strictStrings(definition.Types) || !strictStrings(definition.EnumValues) || !strictStrings(definition.Directives) || !canonicalGraphQLFields(definition.Fields) {
			return false
		}
	}
	for index, directive := range value.Directives {
		if directive.Name == "" || index > 0 && value.Directives[index-1].Name >= directive.Name || !strictStrings(directive.Locations) || !canonicalGraphQLArguments(directive.Arguments) {
			return false
		}
	}
	return true
}

func canonicalGraphQLFields(values []GraphQLField) bool {
	for index, value := range values {
		if value.Name == "" || value.Type == "" || index > 0 && values[index-1].Name >= value.Name || !canonicalGraphQLArguments(value.Arguments) || !strictStrings(value.Directives) {
			return false
		}
	}
	return true
}

func canonicalGraphQLArguments(values []GraphQLArgument) bool {
	for index, value := range values {
		if value.Name == "" || value.Type == "" || index > 0 && values[index-1].Name >= value.Name || !strictStrings(value.Directives) {
			return false
		}
	}
	return true
}

func strictStrings(values []string) bool {
	for index, value := range values {
		if value == "" || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}
