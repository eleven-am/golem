// Package bind validates frozen public P3 reads against the active schema and
// converts them into the closed internal read IR.
package bind

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type ErrorCode string

const (
	CodeInput    ErrorCode = "P3_READ_BIND_INPUT"
	CodeModel    ErrorCode = "P3_READ_BIND_MODEL"
	CodeField    ErrorCode = "P3_READ_BIND_FIELD"
	CodeRelation ErrorCode = "P3_READ_BIND_RELATION"
	CodePolicy   ErrorCode = "P3_READ_BIND_POLICY"
)

type Error struct {
	Code   ErrorCode
	Model  golem.ModelID
	Field  golem.FieldID
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	return fmt.Sprintf("%s: model=%x field=%x: %s", failure.Code, failure.Model, failure.Field, failure.Detail)
}
func (failure *Error) Unwrap() error { return failure.Cause }

func Request(frozen golem.FrozenReadRequest, registry *schema.Registry, providers policyir.ProviderSet) (readir.Request, error) {
	if registry == nil || !providers.Valid() {
		return readir.Request{}, fail(CodeInput, frozen.ModelID(), golem.FieldID{}, "registry and provider set are required", nil)
	}
	return request(frozen, registry, providers, 0)
}

func request(frozen golem.FrozenReadRequest, registry *schema.Registry, providers policyir.ProviderSet, depth int) (readir.Request, error) {
	if depth > 64 {
		return readir.Request{}, fail(CodeInput, frozen.ModelID(), golem.FieldID{}, "read exceeds structural depth limit", nil)
	}
	model := frozen.ModelID()
	if _, ok := registry.Model(model); !ok {
		return readir.Request{}, fail(CodeModel, model, golem.FieldID{}, "model is absent from the active schema", nil)
	}
	operation, ok := operation(frozen.Operation())
	if !ok {
		return readir.Request{}, fail(CodeInput, model, golem.FieldID{}, "unknown read operation", nil)
	}
	input := readir.RequestInput{Operation: operation, Model: policyir.ModelID(model)}
	if publicWhere, present := frozen.Where(); present {
		where, err := policybind.Predicate(publicWhere, registry, providers)
		if err != nil {
			return readir.Request{}, fail(CodePolicy, model, golem.FieldID{}, "where predicate did not bind", err)
		}
		input.Where = &where
	}
	for _, publicOrder := range frozen.OrderBy() {
		field, err := scalarField(registry, model, publicOrder.FieldID())
		if err != nil {
			return readir.Request{}, err
		}
		direction := readir.Ascending
		if publicOrder.Direction() == golem.SortDescending {
			direction = readir.Descending
		} else if publicOrder.Direction() != golem.SortAscending {
			return readir.Request{}, fail(CodeInput, model, publicOrder.FieldID(), "unknown order direction", nil)
		}
		order, err := readir.NewOrder(policyir.FieldID(field.ID()), direction)
		if err != nil {
			return readir.Request{}, fail(CodeInput, model, publicOrder.FieldID(), "order is invalid", err)
		}
		input.OrderBy = append(input.OrderBy, order)
	}
	if value, present := frozen.Take(); present {
		input.Take = &value
	}
	if value, present := frozen.Skip(); present {
		input.Skip = &value
	}
	for _, fieldID := range frozen.Distinct() {
		field, err := scalarField(registry, model, fieldID)
		if err != nil {
			return readir.Request{}, err
		}
		input.Distinct = append(input.Distinct, policyir.FieldID(field.ID()))
	}
	for _, publicSelection := range frozen.Selection() {
		field, ok := registry.Field(model, publicSelection.FieldID())
		if !ok {
			return readir.Request{}, fail(CodeField, model, publicSelection.FieldID(), "selected field is absent or belongs to another model", nil)
		}
		if !publicSelection.IsRelation() {
			if field.Kind() == compilerir.FieldRelation {
				return readir.Request{}, fail(CodeField, model, publicSelection.FieldID(), "relation field was presented as a scalar selection", nil)
			}
			selection, err := readir.NewScalarSelection(policyir.FieldID(field.ID()))
			if err != nil {
				return readir.Request{}, fail(CodeField, model, publicSelection.FieldID(), "scalar selection is invalid", err)
			}
			input.Selection = append(input.Selection, selection)
			continue
		}
		if field.Kind() != compilerir.FieldRelation {
			return readir.Request{}, fail(CodeRelation, model, publicSelection.FieldID(), "scalar field was presented as a relation selection", nil)
		}
		endpoint, ok := registry.RelationEndpoint(model, publicSelection.FieldID(), publicSelection.RelationID())
		if !ok || endpoint.TargetModelID() != publicSelection.TargetModelID() {
			return readir.Request{}, fail(CodeRelation, model, publicSelection.FieldID(), "relation identity or target does not match the active schema", nil)
		}
		publicChild, ok := publicSelection.Request()
		if !ok {
			return readir.Request{}, fail(CodeRelation, model, publicSelection.FieldID(), "relation selection has no child request", nil)
		}
		child, err := request(publicChild, registry, providers, depth+1)
		if err != nil {
			return readir.Request{}, err
		}
		selection, err := readir.NewRelationSelection(policyir.FieldID(field.ID()), policyir.RelationID(endpoint.RelationID()), policyir.ModelID(endpoint.TargetModelID()), child)
		if err != nil {
			return readir.Request{}, fail(CodeRelation, model, publicSelection.FieldID(), "relation selection is invalid", err)
		}
		input.Selection = append(input.Selection, selection)
	}
	bound, err := readir.NewRequest(input)
	if err != nil {
		return readir.Request{}, fail(CodeInput, model, golem.FieldID{}, "internal read request is invalid", err)
	}
	return bound, nil
}

func scalarField(registry *schema.Registry, model golem.ModelID, id golem.FieldID) (schema.Field, error) {
	field, ok := registry.Field(model, id)
	if !ok || field.Kind() == compilerir.FieldRelation {
		return schema.Field{}, fail(CodeField, model, id, "field is absent, belongs to another model, or is not scalar", nil)
	}
	return field, nil
}

func operation(value golem.ReadOperation) (readir.Operation, bool) {
	switch value {
	case golem.ReadFindUnique:
		return readir.FindUnique, true
	case golem.ReadFindFirst:
		return readir.FindFirst, true
	case golem.ReadFindMany:
		return readir.FindMany, true
	case golem.ReadCount:
		return readir.Count, true
	default:
		return 0, false
	}
}

func fail(code ErrorCode, model golem.ModelID, field golem.FieldID, detail string, cause error) error {
	return &Error{Code: code, Model: model, Field: field, Detail: detail, Cause: cause}
}
