package graphql

import (
	"reflect"
	"sort"

	"github.com/vektah/gqlparser/v2/ast"
)

// resolvedInputLimit measures argument materialization, separately from the
// existing raw-variable envelope check. Reusing a variable consumes its shape
// at each occurrence, and schema/variable defaults are not free inputs.
func resolvedInputLimit(schema *ast.Schema, operation *ast.OperationDefinition, fragments ast.FragmentDefinitionList, variables map[string]any, limits Limits) *limitBound {
	budget := argumentInputBudget{schema: schema, limits: limits, nodes: 1} // One virtual argument object, like the variable envelope.
	if bound := budget.directives(operation.Directives, variables); bound != nil {
		return bound
	}
	return budget.selection(operation.SelectionSet, fragments, variables, map[string]bool{})
}

type argumentInputBudget struct {
	schema *ast.Schema
	limits Limits
	nodes  int
}

func (budget *argumentInputBudget) step(depth int) *limitBound {
	if budget.nodes >= budget.limits.MaxInputNodes {
		return limitInputNodes
	}
	budget.nodes++
	if depth > budget.limits.MaxInputDepth {
		return limitInputDepth
	}
	return nil
}

func (budget *argumentInputBudget) raw(value any, typ *ast.Type, depth int) *limitBound {
	// Variable coercion may produce typed slices and leaves nested singleton
	// promotions to the executor. Account for the fully materialized shape.
	if value != nil && typ != nil && typ.Elem != nil && reflect.TypeOf(value).Kind() != reflect.Slice {
		if bound := budget.step(depth); bound != nil {
			return bound
		}
		return budget.raw(value, typ.Elem, depth+1)
	}
	if bound := budget.step(depth); bound != nil {
		return bound
	}
	if value == nil {
		return nil
	}
	if list := reflect.ValueOf(value); list.Kind() == reflect.Slice {
		if list.Len() > budget.limits.MaxListItems {
			return limitListItems
		}
		var element *ast.Type
		if typ != nil {
			element = typ.Elem
		}
		for i := 0; i < list.Len(); i++ {
			if bound := budget.raw(list.Index(i).Interface(), element, depth+1); bound != nil {
				return bound
			}
		}
	} else if object, ok := value.(map[string]any); ok {
		// Raw variables have already passed the envelope node budget. Sorting
		// their bounded keys makes the first reported limit deterministic.
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		definition := budget.definition(typ)
		for _, key := range keys {
			var fieldType *ast.Type
			if definition != nil {
				if field := definition.Fields.ForName(key); field != nil {
					fieldType = field.Type
				}
			}
			if bound := budget.raw(object[key], fieldType, depth+1); bound != nil {
				return bound
			}
		}
		if definition != nil && definition.Kind == ast.InputObject {
			for _, field := range definition.Fields {
				if _, present := object[field.Name]; !present && field.DefaultValue != nil {
					if bound := budget.literal(field.DefaultValue, nil, field.Type, depth+1, false); bound != nil {
						return bound
					}
				}
			}
		}
	}
	return nil
}

func (budget *argumentInputBudget) definition(typ *ast.Type) *ast.Definition {
	if typ == nil {
		return nil
	}
	return budget.schema.Types[typ.Name()]
}

func (budget *argumentInputBudget) literal(value *ast.Value, variables map[string]any, typ *ast.Type, depth int, listPosition bool) *limitBound {
	if value == nil {
		return budget.step(depth)
	}
	if value.Kind == ast.Variable {
		if raw, present := variables[value.Raw]; present {
			return budget.raw(raw, typ, depth)
		}
		if value.VariableDefinition != nil && value.VariableDefinition.DefaultValue != nil {
			return budget.literal(value.VariableDefinition.DefaultValue, variables, typ, depth, listPosition)
		}
		if listPosition {
			return budget.step(depth)
		} // Undefined list entries become null, not omitted entries.
		return nil
	}
	for value.Kind != ast.NullValue && value.Kind != ast.ListValue && typ != nil && typ.Elem != nil {
		if bound := budget.step(depth); bound != nil {
			return bound
		}
		depth++
		typ = typ.Elem
	}
	if bound := budget.step(depth); bound != nil {
		return bound
	}
	switch value.Kind {
	case ast.ListValue:
		if len(value.Children) > budget.limits.MaxListItems {
			return limitListItems
		}
		for _, child := range value.Children {
			var element *ast.Type
			if typ != nil {
				element = typ.Elem
			}
			if bound := budget.literal(child.Value, variables, element, depth+1, true); bound != nil {
				return bound
			}
		}
	case ast.ObjectValue:
		definition := budget.definition(typ)
		for _, child := range value.Children {
			var fieldType *ast.Type
			if definition != nil {
				if field := definition.Fields.ForName(child.Name); field != nil {
					fieldType = field.Type
				}
			}
			if bound := budget.literal(child.Value, variables, fieldType, depth+1, false); bound != nil {
				return bound
			}
		}
		if definition != nil && definition.Kind == ast.InputObject {
			for _, field := range definition.Fields {
				if field.DefaultValue == nil {
					continue
				}
				provided := value.Children.ForName(field.Name)
				if !inputValuePresent(provided, variables) {
					if bound := budget.literal(field.DefaultValue, variables, field.Type, depth+1, false); bound != nil {
						return bound
					}
				}
			}
		}
	}
	return nil
}

func inputValuePresent(value *ast.Value, variables map[string]any) bool {
	if value == nil {
		return false
	}
	if value.Kind != ast.Variable {
		return true
	}
	if _, present := variables[value.Raw]; present {
		return true
	}
	return value.VariableDefinition != nil && value.VariableDefinition.DefaultValue != nil
}

func (budget *argumentInputBudget) fieldArguments(field *ast.Field, variables map[string]any) *limitBound {
	if bound := budget.arguments(field.Arguments, variables); bound != nil {
		return bound
	}
	if field.Definition == nil {
		return nil
	}
	for _, argument := range field.Definition.Arguments {
		if argument.DefaultValue == nil {
			continue
		}
		provided := field.Arguments.ForName(argument.Name)
		if provided == nil || !inputValuePresent(provided.Value, variables) {
			if bound := budget.literal(argument.DefaultValue, variables, argument.Type, 2, false); bound != nil {
				return bound
			}
		}
	}
	return nil
}

func (budget *argumentInputBudget) arguments(arguments ast.ArgumentList, variables map[string]any) *limitBound {
	for _, argument := range arguments {
		if bound := budget.literal(argument.Value, variables, argument.Value.ExpectedType, 2, false); bound != nil {
			return bound
		}
	}
	return nil
}

func (budget *argumentInputBudget) directives(directives ast.DirectiveList, variables map[string]any) *limitBound {
	for _, directive := range directives {
		if bound := budget.arguments(directive.Arguments, variables); bound != nil {
			return bound
		}
	}
	return nil
}

func (budget *argumentInputBudget) selection(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]any, stack map[string]bool) *limitBound {
	for _, selection := range set {
		switch selection := selection.(type) {
		case *ast.Field:
			if bound := budget.fieldArguments(selection, variables); bound != nil {
				return bound
			}
			if bound := budget.directives(selection.Directives, variables); bound != nil {
				return bound
			}
			if bound := budget.selection(selection.SelectionSet, fragments, variables, stack); bound != nil {
				return bound
			}
		case *ast.InlineFragment:
			if bound := budget.directives(selection.Directives, variables); bound != nil {
				return bound
			}
			if bound := budget.selection(selection.SelectionSet, fragments, variables, stack); bound != nil {
				return bound
			}
		case *ast.FragmentSpread:
			if bound := budget.directives(selection.Directives, variables); bound != nil {
				return bound
			}
			fragment := fragments.ForName(selection.Name)
			if fragment == nil || stack[selection.Name] {
				continue
			} // The preceding document/shape validation rejects these.
			stack[selection.Name] = true
			bound := budget.selection(fragment.SelectionSet, fragments, variables, stack)
			delete(stack, selection.Name)
			if bound != nil {
				return bound
			}
		}
	}
	return nil
}
