package bind

import (
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
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
		return nil, compileerr.Invariantf("mutation field is absent or relational")
	}
	visible := false
	for _, candidate := range contract.Fields {
		if candidate.FieldID == fieldID {
			visible = true
			break
		}
	}
	if !visible {
		return nil, compileerr.Invariantf("mutation field is absent from the GraphQL contract")
	}
	typ, err := bindType(field.Scalar.Type, field.Scalar.Nullable)
	if err != nil {
		return nil, err
	}
	state := inputState{binder: b}
	value, err := state.value(field.Scalar.Type, raw)
	if err != nil {
		return nil, compileerr.On(err, contract.GraphQLName)
	}
	public, err := publicvalue.Typed(value, typ, publicvalue.EnumWire(b.freezeEnumLabels()))
	if err != nil {
		return nil, compileerr.Invariant(err)
	}
	return public, nil
}
