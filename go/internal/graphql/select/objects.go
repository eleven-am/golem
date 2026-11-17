package selectset

import (
	compileerr "github.com/eleven-am/golem/go/internal/bindingerr"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2/ast"
)

func computedResultObjectName(value compilerir.GraphQLTypeIR) string {
	for value.Kind == compilerir.GraphQLTypeList && value.Element != nil {
		value = *value.Element
	}
	if value.Kind == compilerir.GraphQLTypeObject {
		return value.Name
	}
	return ""
}

// CompileObject uses the same occurrence expansion and query limits as model
// projections, while retaining DTO fields as transport fields without row IDs.
func CompileObject(request Request, name string) (Result, error) {
	if name == "" || len(request.Selections) == 0 {
		return Result{}, compileerr.Invariantf("object selection compile requires a named type and a selection set")
	}
	entry := newCompiler(request)
	slots, err := entry.compileObject(name, request.Selections, 1, map[string]bool{})
	if err != nil {
		return Result{}, err
	}
	return Result{Slots: slots, Depth: entry.maxDepth, Fields: entry.fields, Aliases: entry.aliases}, nil
}

func (in *compiler) compileObject(name string, set ast.SelectionSet, depth int, stack map[string]bool) ([]Slot, error) {
	if depth > in.request.MaxDepth {
		return nil, selectionLimit("MaxDepth", in.request.MaxDepth)
	}
	if depth > in.maxDepth {
		in.maxDepth = depth
	}
	var definition *compilerir.GraphQLTypeDefinitionIR
	for index := range in.request.Compilation.Contract.GraphQLTypes {
		candidate := &in.request.Compilation.Contract.GraphQLTypes[index]
		if candidate.Name == name && candidate.Kind == compilerir.GraphQLTypeObject {
			definition = candidate
			break
		}
	}
	if definition == nil {
		return nil, compileerr.Invariantf("output definition %s is absent", name)
	}
	fields := make(map[string]compilerir.GraphQLObjectFieldIR, len(definition.Fields))
	for _, field := range definition.Fields {
		fields[field.Name] = field
	}
	occurrences, err := in.expand(set, stack)
	if err != nil {
		return nil, err
	}
	slots := make([]Slot, 0, len(occurrences))
	for _, occurrence := range occurrences {
		if err := in.countField(occurrence); err != nil {
			return nil, err
		}
		if len(occurrence.field.Arguments) != 0 {
			return nil, compileerr.Invariantf("DTO field %s.%s has arguments", name, occurrence.field.Name)
		}
		if occurrence.field.Name == "__typename" {
			if len(occurrence.selections) != 0 {
				return nil, shapeError(compileerr.ShapeForbidsSelection, occurrence.field.Name, name)
			}
			slots = append(slots, Slot{Kind: SlotTypename, ResponseName: occurrence.responseName})
			continue
		}
		field, exists := fields[occurrence.field.Name]
		if !exists {
			return nil, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableField, compileerr.Path(occurrence.field.Name), compileerr.Model(name))
		}
		leaf := field.Type
		for leaf.Kind == compilerir.GraphQLTypeList {
			if leaf.Element == nil {
				return nil, compileerr.Invariantf("DTO list %s.%s has no element type", name, field.Name)
			}
			leaf = *leaf.Element
		}
		slot := Slot{Kind: SlotObjectField, ResponseName: occurrence.responseName, ObjectField: field.Name}
		switch leaf.Kind {
		case compilerir.GraphQLTypeModel:
			if len(occurrence.selections) == 0 {
				return nil, shapeError(compileerr.ShapeNeedsSelection, field.Name, name)
			}
			model, exists := in.contractNames[leaf.Name]
			if !exists {
				return nil, compileerr.Invariantf("DTO model %s is absent", leaf.Name)
			}
			projection, children, err := in.compileModel(model, occurrence.selections, depth+1, stack)
			if err != nil {
				return nil, err
			}
			slot.Model, slot.Children = &ModelSelection{Model: model, Projection: projection}, children
		case compilerir.GraphQLTypeObject:
			if len(occurrence.selections) == 0 {
				return nil, shapeError(compileerr.ShapeNeedsSelection, field.Name, name)
			}
			slot.Children, err = in.compileObject(leaf.Name, occurrence.selections, depth+1, stack)
			if err != nil {
				return nil, err
			}
		case compilerir.GraphQLTypeScalar, compilerir.GraphQLTypeEnum:
			if len(occurrence.selections) != 0 {
				return nil, shapeError(compileerr.ShapeForbidsSelection, field.Name, name)
			}
		default:
			return nil, compileerr.Invariantf("DTO field %s.%s requires unsupported result kind %s", name, field.Name, leaf.Kind)
		}
		slots = append(slots, slot)
	}
	if definition.RowGate != "" {
		selected := false
		for _, slot := range slots {
			if slot.ObjectField == definition.RowGate && slot.Model != nil {
				selected = true
				break
			}
		}
		if !selected {
			field, exists := fields[definition.RowGate]
			model, known := in.contractNames[field.Type.Name]
			if !exists || !known || field.Type.Kind != compilerir.GraphQLTypeModel {
				return nil, compileerr.Invariantf("DTO row gate %s.%s is invalid", name, definition.RowGate)
			}
			slots = append(slots, Slot{Kind: SlotObjectGate, ObjectField: definition.RowGate, Model: &ModelSelection{Model: model}})
		}
	}
	return slots, nil
}
