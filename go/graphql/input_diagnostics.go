package graphql

import (
	compileerr "github.com/eleven-am/golem/go/internal/bindingerr"
	"github.com/vektah/gqlparser/v2/ast"
)

// enumInputDiagnostic enriches an already refused request using only schema
// metadata. Validator messages include caller values and must not be rendered.
// This is diagnostic traversal, not a second coercion or authorization path.
func enumInputDiagnostic(schema *ast.Schema, document *ast.QueryDocument, operationName string, variables map[string]any) *Error {
	operation := document.Operations.ForName(operationName)
	if operation == nil {
		return nil
	}
	owner := schema.Query
	switch operation.Operation {
	case ast.Mutation:
		owner = schema.Mutation
	case ast.Subscription:
		owner = schema.Subscription
	}
	walker := enumDiagnosticWalker{schema: schema, document: document, variables: variables, fragments: map[string]bool{}, remaining: 32768}
	return walker.selection(operation.SelectionSet, owner, nil, 0)
}

type enumDiagnosticWalker struct {
	schema    *ast.Schema
	document  *ast.QueryDocument
	variables map[string]any
	fragments map[string]bool
	remaining int
}

func (walker *enumDiagnosticWalker) step(depth int) bool {
	if walker.remaining <= 0 || depth > 64 {
		return false
	}
	walker.remaining--
	return true
}

func (walker *enumDiagnosticWalker) selection(selections ast.SelectionSet, owner *ast.Definition, path []any, depth int) *Error {
	if owner == nil || !walker.step(depth) {
		return nil
	}
	for _, selection := range selections {
		if !walker.step(depth) {
			return nil
		}
		switch selected := selection.(type) {
		case *ast.Field:
			field := owner.Fields.ForName(selected.Name)
			if field == nil {
				continue
			}
			response := selected.Alias
			if response == "" {
				response = selected.Name
			}
			fieldPath := append(append([]any(nil), path...), response)
			for _, argument := range selected.Arguments {
				if !walker.step(depth) {
					return nil
				}
				definition := field.Arguments.ForName(argument.Name)
				if definition == nil {
					continue
				}
				if failure := walker.literal(argument.Value, definition.Type, argument.Name, depth+1); failure != nil {
					presented := presentCompileError(failure, fieldPath)
					if argument.Value.Position != nil {
						presented.Locations = []Location{{Line: argument.Value.Position.Line, Column: argument.Value.Position.Column}}
					}
					return &presented
				}
			}
			if failure := walker.selection(selected.SelectionSet, walker.schema.Types[field.Type.Name()], fieldPath, depth+1); failure != nil {
				return failure
			}
		case *ast.InlineFragment:
			target := owner
			if selected.TypeCondition != "" {
				target = walker.schema.Types[selected.TypeCondition]
			}
			if !walker.overlap(owner, target, depth) {
				continue
			}
			if failure := walker.selection(selected.SelectionSet, target, path, depth+1); failure != nil {
				return failure
			}
		case *ast.FragmentSpread:
			// A global visited set also bounds repeated fragment expansion.
			if walker.fragments[selected.Name] {
				continue
			}
			fragment := walker.document.Fragments.ForName(selected.Name)
			if fragment != nil && walker.overlap(owner, walker.schema.Types[fragment.TypeCondition], depth) {
				walker.fragments[selected.Name] = true
				if failure := walker.selection(fragment.SelectionSet, walker.schema.Types[fragment.TypeCondition], path, depth+1); failure != nil {
					return failure
				}
			}
		}
	}
	return nil
}

func (walker *enumDiagnosticWalker) literal(value *ast.Value, typ *ast.Type, path string, depth int) *compileerr.Error {
	if !walker.step(depth) || value == nil || typ == nil || value.Kind == ast.NullValue {
		return nil
	}
	if value.Kind == ast.Variable {
		return walker.variable(walker.variables[value.Raw], typ, path, depth+1)
	}
	if typ.Elem != nil {
		if value.Kind != ast.ListValue {
			return walker.literal(value, typ.Elem, path, depth+1)
		}
		for index, child := range value.Children {
			if !walker.step(depth) {
				return nil
			}
			if failure := walker.literal(child.Value, typ.Elem, compileerr.Index(path, index), depth+1); failure != nil {
				return failure
			}
		}
		return nil
	}
	definition := walker.schema.Types[typ.NamedType]
	if definition == nil {
		return nil
	}
	if definition.Kind == ast.Enum {
		if value.Kind != ast.EnumValue || definition.EnumValues.ForName(value.Raw) == nil {
			return enumDiagnostic(definition, typ, path)
		}
	}
	if definition.Kind == ast.InputObject && value.Kind == ast.ObjectValue {
		for _, child := range value.Children {
			if !walker.step(depth) {
				return nil
			}
			if field := definition.Fields.ForName(child.Name); field != nil {
				if failure := walker.literal(child.Value, field.Type, compileerr.Join(path, field.Name), depth+1); failure != nil {
					return failure
				}
			}
		}
	}
	return nil
}

func (walker *enumDiagnosticWalker) variable(value any, typ *ast.Type, path string, depth int) *compileerr.Error {
	if !walker.step(depth) || value == nil || typ == nil {
		return nil
	}
	if typ.Elem != nil {
		if items, ok := value.([]any); ok {
			for index, item := range items {
				if !walker.step(depth) {
					return nil
				}
				if failure := walker.variable(item, typ.Elem, compileerr.Index(path, index), depth+1); failure != nil {
					return failure
				}
			}
			return nil
		}
		return walker.variable(value, typ.Elem, path, depth+1)
	}
	definition := walker.schema.Types[typ.NamedType]
	if definition == nil {
		return nil
	}
	if definition.Kind == ast.Enum {
		name, valid := value.(string)
		if !valid || definition.EnumValues.ForName(name) == nil {
			return enumDiagnostic(definition, typ, path)
		}
	}
	if definition.Kind == ast.InputObject {
		if object, ok := value.(map[string]any); ok {
			// Schema order is deterministic and cannot expose unknown input keys.
			for _, field := range definition.Fields {
				if !walker.step(depth) {
					return nil
				}
				if failure := walker.variable(object[field.Name], field.Type, compileerr.Join(path, field.Name), depth+1); failure != nil {
					return failure
				}
			}
		}
	}
	return nil
}

func enumDiagnostic(definition *ast.Definition, typ *ast.Type, path string) *compileerr.Error {
	allowed := make([]string, 0, len(definition.EnumValues))
	for _, value := range definition.EnumValues {
		allowed = append(allowed, value.Name)
	}
	return compileerr.New(compileerr.KindInvalidValue, compileerr.ValueUnknownEnum,
		compileerr.Path(path), compileerr.ExpectedType(typ.String()), compileerr.AllowedValues(allowed))
}

// Invalid fragments must not turn diagnostic traversal into schema discovery.
func (walker *enumDiagnosticWalker) overlap(left, right *ast.Definition, depth int) bool {
	if left == nil || right == nil || !left.IsCompositeType() || !right.IsCompositeType() {
		return false
	}
	if left.Name == right.Name {
		return true
	}
	possible := func(definition *ast.Definition) []*ast.Definition {
		if definition.Kind == ast.Object {
			return []*ast.Definition{definition}
		}
		return walker.schema.GetPossibleTypes(definition)
	}
	for _, a := range possible(left) {
		for _, b := range possible(right) {
			if !walker.step(depth) {
				return false
			}
			if a.Name == b.Name {
				return true
			}
		}
	}
	return false
}
