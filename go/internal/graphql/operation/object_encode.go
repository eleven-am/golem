package operation

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
)

func missingDTOModel() error {
	return golem.RuntimeOperationError(golem.CodeNotFound, "customResult", golem.ModelID{}, golem.FieldID{}, "record not found", nil)
}

func customResultObjectName(typ compilerir.GraphQLTypeIR) string {
	for typ.Kind == compilerir.GraphQLTypeList && typ.Element != nil {
		typ = *typ.Element
	}
	if typ.Kind == compilerir.GraphQLTypeObject {
		return typ.Name
	}
	return ""
}

// HoldsObject reports a DTO result, possibly inside nested lists.
func HoldsObject(typ compilerir.GraphQLTypeIR) bool { return customResultObjectName(typ) != "" }

func (c *Compiler) encodeCustomObject(ctx context.Context, typ compilerir.GraphQLTypeIR, value any, slots []selectset.Slot, resolve ComputedResolver, path []any) (any, []ComputedFieldFailure, error) {
	object, ok := value.(map[string]any)
	projected, prepared := value.(hydratedObject)
	if !ok && !prepared {
		return nil, nil, fmt.Errorf("custom object %s has value %T", typ.Name, value)
	}
	var definition *compilerir.GraphQLTypeDefinitionIR
	for index := range c.compilation.Contract.GraphQLTypes {
		candidate := &c.compilation.Contract.GraphQLTypes[index]
		if candidate.Name == typ.Name && candidate.Kind == typ.Kind {
			definition = candidate
			break
		}
	}
	if definition == nil {
		return nil, nil, fmt.Errorf("custom object %s is not registered", typ.Name)
	}
	fields := make(map[string]compilerir.GraphQLTypeIR, len(definition.Fields))
	for _, field := range definition.Fields {
		fields[field.Name] = field.Type
	}
	result := make(map[string]any, len(slots))
	var failures []ComputedFieldFailure
	for _, slot := range slots {
		if slot.Kind == selectset.SlotObjectGate {
			continue
		}
		if slot.Kind == selectset.SlotTypename {
			result[slot.ResponseName] = typ.Name
			continue
		}
		field, exists := fields[slot.ObjectField]
		if slot.Kind != selectset.SlotObjectField || !exists {
			return nil, nil, fmt.Errorf("custom object %s has an invalid selected field", typ.Name)
		}
		item, present := object[slot.ObjectField]
		if prepared {
			item, present = projected.fields[slot.ResponseName]
		}
		if slot.Model != nil && !prepared {
			return nil, nil, fmt.Errorf("custom DTO model field has not been caller-hydrated")
		}
		if !present {
			return nil, nil, fmt.Errorf("custom object %s.%s is absent", typ.Name, slot.ObjectField)
		}
		encoded, childFailures, err := c.encodeCustomPreparedValuePartial(ctx, field, item, slot.Children, resolve, appendPath(path, slot.ResponseName))
		if err != nil {
			result[slot.ResponseName] = nil
			failures = append(failures, ComputedFieldFailure{Path: appendPath(path, slot.ResponseName), Err: err, PropagatesToRoot: !field.Nullable})
			continue
		}
		result[slot.ResponseName] = encoded
		failures = append(failures, propagateThroughBoundary(childFailures, field.Nullable)...)
	}
	return result, propagateThroughBoundary(failures, typ.Nullable), nil
}
