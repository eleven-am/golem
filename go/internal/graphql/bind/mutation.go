package bind

import (
	"fmt"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/publicvalue"
)

func (b *Binder) MutationValue(modelID compilerir.ModelID, fieldID compilerir.FieldID, raw any) (any, error) {
	model, contract, err := b.model(modelID)
	if err != nil {
		return nil, err
	}
	var field *compilerir.FieldIR
	for index := range model.Fields {
		if model.Fields[index].ID == fieldID {
			field = &model.Fields[index]
			break
		}
	}
	if field == nil || field.Scalar == nil || field.Kind == compilerir.FieldRelation {
		return nil, fmt.Errorf("P5_BIND_MUTATION_VALUE: field is absent or relational")
	}
	visible := false
	for _, candidate := range contract.Fields {
		if candidate.FieldID == fieldID {
			visible = true
			break
		}
	}
	if !visible {
		return nil, fmt.Errorf("P5_BIND_MUTATION_VALUE: field is absent from the GraphQL contract")
	}
	typ, err := bindType(field.Scalar.Type, field.Scalar.Nullable)
	if err != nil {
		return nil, fmt.Errorf("P5_BIND_MUTATION_VALUE: %w", err)
	}
	state := inputState{binder: b}
	value, err := state.value(field.Scalar.Type, raw)
	if err != nil {
		return nil, fmt.Errorf("P5_BIND_MUTATION_VALUE: %w", err)
	}
	public, err := publicvalue.Typed(value, typ, publicvalue.EnumWire(b.freezeEnumLabels()))
	if err != nil {
		return nil, fmt.Errorf("P5_BIND_MUTATION_VALUE: %w", err)
	}
	return public, nil
}
