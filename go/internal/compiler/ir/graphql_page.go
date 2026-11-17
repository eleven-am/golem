package ir

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/eleven-am/golem/go/internal/pagecontract"
)

func (page CustomPaginationIR) Validate() error {
	if page.DefaultTake == 0 || page.DefaultTake > page.MaxTake || page.MaxTake > pagecontract.MaxItems || page.MaxCandidates < page.MaxTake || page.MaxCandidates > pagecontract.MaxCandidates {
		return fmt.Errorf("page limits require 1 <= defaultTake <= maxTake <= %d and maxTake <= maxCandidates <= %d", pagecontract.MaxItems, pagecontract.MaxCandidates)
	}
	if strings.TrimSpace(page.Revision) == "" || len(page.Revision) > 256 {
		return fmt.Errorf("page revision must be nonempty and at most 256 bytes")
	}
	if page.Item.Nullable || page.Item.Element != nil || page.Item.MaxItems != 0 || page.Item.Name == "" {
		return fmt.Errorf("page items must be non-null leaves")
	}
	switch page.Item.Kind {
	case GraphQLTypeScalar, GraphQLTypeEnum, GraphQLTypeObject, GraphQLTypeModel:
		return nil
	default:
		return fmt.Errorf("page item has unsupported kind %q", page.Item.Kind)
	}
}

func IsPageArgumentType(value GraphQLTypeIR) bool {
	for value.Kind == GraphQLTypeList {
		if value.Element == nil {
			return false
		}
		value = *value.Element
	}
	return value.Kind == GraphQLTypeScalar && value.Name != "JSON" || value.Kind == GraphQLTypeEnum || value.Kind == GraphQLTypeInputObject
}

// GraphQLPageDefinition is the single compiler-owned wire representation. Its
// Go witness is deliberately absent: a PageBatch is normalized by page glue.
func GraphQLPageDefinition(name string, page CustomPaginationIR) GraphQLTypeDefinitionIR {
	item := page.Item
	return GraphQLTypeDefinitionIR{Kind: GraphQLTypeObject, Name: name, Page: true, Fields: []GraphQLObjectFieldIR{
		{Name: "items", GoName: "Items", Type: GraphQLTypeIR{Kind: GraphQLTypeList, Element: &item, MaxItems: page.MaxTake}},
		{Name: "next", GoName: "Next", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "String", Nullable: true}},
	}}
}

func ValidateGraphQLPages(contract ContractIR) error {
	for _, operation := range contract.CustomOperations {
		if operation.Pagination != nil && contract.DataIncarnation == nil {
			return fmt.Errorf("PageQuery requires WithDataIncarnation")
		}
	}
	pages := make(map[string]GraphQLTypeDefinitionIR)
	for _, definition := range contract.GraphQLTypes {
		if definition.Page {
			pages[definition.Name] = definition
		}
	}
	pageName := func(value GraphQLTypeIR) string {
		for value.Kind == GraphQLTypeList && value.Element != nil {
			value = *value.Element
		}
		if _, ok := pages[value.Name]; ok && value.Kind == GraphQLTypeObject {
			return value.Name
		}
		return ""
	}
	for _, definition := range contract.GraphQLTypes {
		for _, field := range definition.Fields {
			if pageName(field.Type) != "" {
				return fmt.Errorf("page types cannot be nested in GraphQL fields")
			}
		}
	}
	used := make(map[string]bool)
	for _, operation := range contract.CustomOperations {
		name := pageName(operation.Result)
		if operation.Pagination == nil {
			if name != "" {
				return fmt.Errorf("custom operation %s uses a page without pagination metadata", operation.Name)
			}
			continue
		}
		page := *operation.Pagination
		if err := page.Validate(); err != nil {
			return fmt.Errorf("custom page %s: %w", operation.Name, err)
		}
		if operation.Operation != CustomOperationQuery || operation.Result.Kind != GraphQLTypeObject || operation.Result.Nullable || name == "" || used[name] {
			return fmt.Errorf("custom page %s requires its own non-null compiler-owned query result", operation.Name)
		}
		expected := GraphQLPageDefinition(name, page)
		actual := pages[name]
		if actual.Kind != expected.Kind || actual.Go != (GoNamedTypeIR{}) || actual.OneOf || actual.RowGate != "" || len(actual.Fields) != 2 {
			return fmt.Errorf("custom page %s has invalid synthetic definition", operation.Name)
		}
		for _, field := range expected.Fields {
			matched := false
			for _, found := range actual.Fields {
				if reflect.DeepEqual(found, field) {
					matched = true
				}
			}
			if !matched {
				return fmt.Errorf("custom page %s has invalid %s field", operation.Name, field.Name)
			}
		}
		take, after := false, false
		for _, argument := range operation.Arguments {
			switch argument.Name {
			case "take", "after":
				want := "Int"
				if argument.Name == "after" {
					want = "String"
				}
				if argument.PresenceAware || !reflect.DeepEqual(argument.Type, GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: want, Nullable: true}) {
					return fmt.Errorf("custom page %s has invalid %s argument", operation.Name, argument.Name)
				}
				if argument.Name == "take" {
					if take {
						return fmt.Errorf("duplicate page take")
					}
					take = true
				} else {
					if after {
						return fmt.Errorf("duplicate page after")
					}
					after = true
				}
			default:
				if !IsPageArgumentType(argument.Type) {
					return fmt.Errorf("custom page %s has unsupported argument %s", operation.Name, argument.Name)
				}
			}
		}
		if !take || !after {
			return fmt.Errorf("custom page %s requires take and after arguments", operation.Name)
		}
		if err := ValidatePageArguments(operation.Arguments, contract.GraphQLTypes); err != nil {
			return fmt.Errorf("custom page %s: %w", operation.Name, err)
		}
		used[name] = true
	}
	for name := range pages {
		if !used[name] {
			return fmt.Errorf("compiler-owned page %s has no operation", name)
		}
	}
	for _, model := range contract.Models {
		for _, computed := range model.Computed {
			if pageName(computed.Result) != "" {
				return fmt.Errorf("computed fields cannot return page types")
			}
		}
	}
	return nil
}

// ValidatePageArguments rejects capability inputs and JSON recursively. The
// continuation canonicalizer has no authority to serialize either implicitly.
func ValidatePageArguments(arguments []GraphQLArgumentContractIR, definitions []GraphQLTypeDefinitionIR) error {
	byName := make(map[string]GraphQLTypeDefinitionIR, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	visiting := make(map[string]bool)
	var check func(GraphQLTypeIR) error
	check = func(value GraphQLTypeIR) error {
		if !IsPageArgumentType(value) {
			return fmt.Errorf("unsupported page argument type %s %s", value.Kind, value.Name)
		}
		for value.Kind == GraphQLTypeList {
			value = *value.Element
		}
		if value.Kind != GraphQLTypeInputObject {
			return nil
		}
		definition, ok := byName[value.Name]
		if !ok || definition.Kind != GraphQLTypeInputObject {
			return fmt.Errorf("unknown page input %s", value.Name)
		}
		if visiting[value.Name] {
			return fmt.Errorf("recursive page input %s", value.Name)
		}
		visiting[value.Name] = true
		defer delete(visiting, value.Name)
		for _, field := range definition.Fields {
			if err := check(field.Type); err != nil {
				return fmt.Errorf("input %s.%s: %w", value.Name, field.Name, err)
			}
		}
		return nil
	}
	for _, argument := range arguments {
		if err := check(argument.Type); err != nil {
			return fmt.Errorf("argument %s: %w", argument.Name, err)
		}
	}
	return nil
}
