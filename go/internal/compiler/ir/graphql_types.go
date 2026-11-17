package ir

import (
	"fmt"
	"go/token"
	"regexp"
	"strings"
)

var graphQLDefinitionName = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

// ValidateGraphQLTypeDefinitions checks the named DTO graph and every use of
// its references. Model and enum authority remains with compilation validation.
func ValidateGraphQLTypeDefinitions(contract ContractIR) error {
	if err := ValidateDataIncarnationContract(contract); err != nil {
		return err
	}
	definitions := make(map[string]GraphQLTypeDefinitionIR, len(contract.GraphQLTypes))
	reserved := map[string]bool{"Query": true, "Mutation": true, "Subscription": true, "ID": true}
	models, enums := make(map[string]bool), make(map[string]bool)
	for _, name := range ScalarGraphQLNames() {
		reserved[name] = true
	}
	for _, model := range contract.Models {
		reserved[model.GraphQLName] = true
		models[model.GraphQLName] = model.Exposed
	}
	for _, enum := range contract.Enums {
		reserved[enum.GraphQLName] = true
		enums[enum.GraphQLName] = true
	}
	goTypes := make(map[string]bool)
	for _, definition := range contract.GraphQLTypes {
		if !validGraphQLDefinitionName(definition.Name) {
			return fmt.Errorf("GraphQL definition name %q is invalid", definition.Name)
		}
		if reserved[definition.Name] {
			return fmt.Errorf("GraphQL definition %q collides with a reserved type", definition.Name)
		}
		if _, exists := definitions[definition.Name]; exists {
			return fmt.Errorf("duplicate GraphQL definition %q", definition.Name)
		}
		if definition.Kind != GraphQLTypeObject && definition.Kind != GraphQLTypeInputObject {
			return fmt.Errorf("GraphQL definition %q has invalid kind %q", definition.Name, definition.Kind)
		}
		if !definition.Page && (definition.Go.PackagePath == "" || !token.IsIdentifier(definition.Go.Name) || !token.IsExported(definition.Go.Name)) {
			return fmt.Errorf("GraphQL definition %q requires an exported named Go type", definition.Name)
		}
		goKey := string(definition.Kind) + "\x00" + definition.Go.PackagePath + "\x00" + definition.Go.Name
		if !definition.Page && goTypes[goKey] {
			return fmt.Errorf("duplicate GraphQL registration of Go type %s.%s as %s", definition.Go.PackagePath, definition.Go.Name, definition.Kind)
		}
		if !definition.Page {
			goTypes[goKey] = true
		}
		if len(definition.Fields) == 0 {
			return fmt.Errorf("GraphQL definition %q requires at least one field", definition.Name)
		}
		if definition.OneOf && definition.Kind != GraphQLTypeInputObject {
			return fmt.Errorf("GraphQL one-of %s must be an input object", definition.Name)
		}
		fields, goFields := make(map[string]bool), make(map[string]bool)
		for _, field := range definition.Fields {
			if field.PresenceAware && (definition.Kind != GraphQLTypeInputObject || !field.Type.CanTrackInputPresence()) {
				return fmt.Errorf("GraphQL field %s.%s has invalid input presence metadata", definition.Name, field.Name)
			}
			if definition.OneOf && !field.Type.Nullable {
				return fmt.Errorf("GraphQL one-of %s requires nullable members", definition.Name)
			}
			if !validGraphQLDefinitionName(field.Name) {
				return fmt.Errorf("GraphQL definition %q has invalid field name %q", definition.Name, field.Name)
			}
			if fields[field.Name] {
				return fmt.Errorf("GraphQL definition %q has duplicate field %q", definition.Name, field.Name)
			}
			if !token.IsIdentifier(field.GoName) || !token.IsExported(field.GoName) || goFields[field.GoName] {
				return fmt.Errorf("GraphQL definition %q has invalid or duplicate Go field %q", definition.Name, field.GoName)
			}
			fields[field.Name], goFields[field.GoName] = true, true
		}
		if definition.RowGate != "" {
			valid := false
			for _, field := range definition.Fields {
				if field.Name == definition.RowGate && field.Type.Kind == GraphQLTypeModel {
					valid = true
					break
				}
			}
			if definition.Kind != GraphQLTypeObject || !valid {
				return fmt.Errorf("GraphQL definition %s row gate must name a model row field", definition.Name)
			}
		}
		definitions[definition.Name] = definition
	}
	state := make(map[string]uint8, len(definitions))
	var path []string
	var visit func(string) error
	var check func(GraphQLTypeIR, bool, bool) error
	check = func(value GraphQLTypeIR, input, field bool) error {
		if value.Kind == GraphQLTypeList {
			if value.Element == nil || value.Name != "" {
				return fmt.Errorf("GraphQL list requires one element and no name")
			}
			if field && ((!input && value.MaxItems == 0) || (input && value.MaxItems != 0)) {
				return fmt.Errorf("GraphQL list maximum is missing or invalid in this position")
			}
			return check(*value.Element, input, field)
		}
		if value.Element != nil || value.Name == "" {
			return fmt.Errorf("GraphQL leaf requires a name and no element")
		}
		if field && value.MaxItems != 0 {
			return fmt.Errorf("GraphQL non-list field cannot carry a maximum")
		}
		switch value.Kind {
		case GraphQLTypeObject, GraphQLTypeInputObject:
			definition, exists := definitions[value.Name]
			if !exists {
				return fmt.Errorf("unknown GraphQL definition %q", value.Name)
			}
			if definition.Kind != value.Kind {
				return fmt.Errorf("GraphQL reference %q has kind %s, expected %s", value.Name, value.Kind, definition.Kind)
			}
			if input && value.Kind != GraphQLTypeInputObject {
				return fmt.Errorf("GraphQL output object %q cannot appear in an input", value.Name)
			}
			if !input && value.Kind != GraphQLTypeObject {
				return fmt.Errorf("GraphQL input object %q cannot appear in an output", value.Name)
			}
			return visit(value.Name)
		case GraphQLTypeScalar:
			if field && !IsScalarGraphQLName(value.Name) {
				return fmt.Errorf("unknown GraphQL scalar %q", value.Name)
			}
		case GraphQLTypeEnum:
			if field && !enums[value.Name] {
				return fmt.Errorf("unknown GraphQL enum %q", value.Name)
			}
		case GraphQLTypeModel:
			if field && input {
				return fmt.Errorf("GraphQL model %q cannot appear in an input object", value.Name)
			}
			if field && !models[value.Name] {
				return fmt.Errorf("unknown or hidden GraphQL model %q", value.Name)
			}
		default:
			if field {
				return fmt.Errorf("GraphQL field has unsupported kind %q", value.Kind)
			}
		}
		return nil
	}
	visit = func(name string) error {
		if state[name] == 2 {
			return nil
		}
		if state[name] == 1 {
			return fmt.Errorf("recursive GraphQL definition: %s", strings.Join(append(path, name), " -> "))
		}
		state[name] = 1
		path = append(path, name)
		definition := definitions[name]
		for _, field := range definition.Fields {
			if err := check(field.Type, definition.Kind == GraphQLTypeInputObject, true); err != nil {
				return fmt.Errorf("GraphQL field %s.%s: %w", name, field.Name, err)
			}
		}
		path = path[:len(path)-1]
		state[name] = 2
		return nil
	}
	for _, definition := range contract.GraphQLTypes {
		if err := visit(definition.Name); err != nil {
			return err
		}
	}
	checkOperation := func(result GraphQLTypeIR, arguments []GraphQLArgumentContractIR) error {
		if err := check(result, false, false); err != nil {
			return err
		}
		for _, argument := range arguments {
			if argument.PresenceAware && !argument.Type.CanTrackInputPresence() {
				return fmt.Errorf("argument %s has invalid input presence metadata", argument.Name)
			}
			if err := check(argument.Type, true, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, operation := range contract.CustomOperations {
		if err := checkOperation(operation.Result, operation.Arguments); err != nil {
			return fmt.Errorf("GraphQL custom operation %s: %w", operation.Name, err)
		}
	}
	for _, model := range contract.Models {
		for _, computed := range model.Computed {
			if err := checkOperation(computed.Result, computed.Arguments); err != nil {
				return fmt.Errorf("GraphQL computed field %s.%s: %w", model.GraphQLName, computed.Name, err)
			}
		}
	}
	if err := ValidateGraphQLPages(contract); err != nil {
		return err
	}
	return ValidateGraphQLInvalidations(contract)
}

// CanTrackInputPresence restricts the wrapper to nullable native input values.
// Model-derived capabilities keep their existing explicit binding contracts.
func (value GraphQLTypeIR) CanTrackInputPresence() bool {
	if !value.Nullable {
		return false
	}
	for value.Kind == GraphQLTypeList {
		if value.Element == nil {
			return false
		}
		value = *value.Element
	}
	return value.Kind == GraphQLTypeScalar || value.Kind == GraphQLTypeEnum || value.Kind == GraphQLTypeInputObject
}

func validGraphQLDefinitionName(name string) bool {
	return graphQLDefinitionName.MatchString(name) && !strings.HasPrefix(name, "__")
}
