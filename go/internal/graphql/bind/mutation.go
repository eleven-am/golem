package bind

import (
	"fmt"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// MutationValue applies the same exact GraphQL scalar/enum/list/JSON coercion
// used by P5-C filters, but returns P2's exact immutable value for P4 input
// binding. No public name or float64 approximation crosses this boundary.
func (b *Binder) MutationValue(modelID compilerir.ModelID, fieldID compilerir.FieldID, raw any) (policyir.Value, error) {
	model, contract, err := b.model(modelID)
	if err != nil {
		return policyir.Value{}, err
	}
	var field *compilerir.FieldIR
	for index := range model.Fields {
		if model.Fields[index].ID == fieldID {
			field = &model.Fields[index]
			break
		}
	}
	if field == nil || field.Scalar == nil || field.Kind == compilerir.FieldRelation {
		return policyir.Value{}, fmt.Errorf("P5_BIND_MUTATION_VALUE: field is absent or relational")
	}
	visible := false
	for _, candidate := range contract.Fields {
		if candidate.FieldID == fieldID {
			visible = true
			break
		}
	}
	if !visible {
		return policyir.Value{}, fmt.Errorf("P5_BIND_MUTATION_VALUE: field is absent from the GraphQL contract")
	}
	state := inputState{binder: b}
	value, err := state.value(field.Scalar.Type, raw)
	if err != nil {
		return policyir.Value{}, fmt.Errorf("P5_BIND_MUTATION_VALUE: %w", err)
	}
	return value, nil
}
