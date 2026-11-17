package selectset

import (
	compileerr "github.com/eleven-am/golem/go/internal/bindingerr"
	"github.com/vektah/gqlparser/v2/ast"
)

// ArgumentValues retains GraphQL's distinction between an undefined variable
// and explicit null, including inside input object literals. Undefined list
// members retain their position as null instead of changing the list length.
func ArgumentValues(arguments ast.ArgumentList, variables map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(arguments))
	seen := make(map[string]bool, len(arguments))
	for _, argument := range arguments {
		if seen[argument.Name] {
			return nil, compileerr.New(compileerr.KindDuplicate, compileerr.DuplicateArgument, compileerr.Path(argument.Name))
		}
		seen[argument.Name] = true
		value, present, err := inputLiteralValue(argument.Value, variables)
		if err != nil {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape, compileerr.Path(argument.Name))
		}
		if present {
			result[argument.Name] = value
		}
	}
	return result, nil
}

func inputLiteralValue(value *ast.Value, variables map[string]any) (any, bool, error) {
	if value == nil {
		return nil, true, nil
	}
	switch value.Kind {
	case ast.Variable:
		if supplied, present := variables[value.Raw]; present {
			return supplied, true, nil
		}
		if value.VariableDefinition != nil && value.VariableDefinition.DefaultValue != nil {
			return inputLiteralValue(value.VariableDefinition.DefaultValue, variables)
		}
		return nil, false, nil
	case ast.ObjectValue:
		result := make(map[string]any, len(value.Children))
		for _, child := range value.Children {
			item, present, err := inputLiteralValue(child.Value, variables)
			if err != nil {
				return nil, false, err
			}
			if present {
				result[child.Name] = item
			}
		}
		return result, true, nil
	case ast.ListValue:
		result := make([]any, len(value.Children))
		for i, child := range value.Children {
			item, _, err := inputLiteralValue(child.Value, variables)
			if err != nil {
				return nil, false, err
			}
			result[i] = item
		}
		return result, true, nil
	default:
		result, err := value.Value(variables)
		return result, true, err
	}
}
