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
	switch frozen.ProjectionMode() {
	case golem.ProjectionDefault:
		input.Projection = readir.ProjectionDefault
	case golem.ProjectionSelect:
		input.Projection = readir.ProjectionSelect
	case golem.ProjectionInclude:
		input.Projection = readir.ProjectionInclude
	default:
		return readir.Request{}, fail(CodeInput, model, golem.FieldID{}, "unknown projection mode", nil)
	}
	if publicSelector, present := frozen.Selector(); present {
		selector, selectorErr := bindSelector(publicSelector, model, registry)
		if selectorErr != nil {
			return readir.Request{}, selectorErr
		}
		input.Selector = &selector
	} else if operation == readir.FindUnique {
		return readir.Request{}, fail(CodeInput, model, golem.FieldID{}, "findUnique requires a unique selector", nil)
	}
	if publicCursor, present := frozen.Cursor(); present {
		selector, err := bindSelector(publicCursor.Selector(), model, registry)
		if err != nil {
			return readir.Request{}, err
		}
		predicate, err := policybind.Predicate(publicCursor.Predicate(), registry, providers)
		if err != nil {
			return readir.Request{}, fail(CodePolicy, model, golem.FieldID{}, "cursor predicate did not bind", err)
		}
		if err := validatePublicCondition(predicate, registry); err != nil {
			return readir.Request{}, err
		}
		cursor, err := readir.NewCursor(selector, predicate)
		if err != nil {
			return readir.Request{}, fail(CodeInput, model, golem.FieldID{}, "cursor is invalid", err)
		}
		input.Cursor = &cursor
	}
	if publicWhere, present := frozen.Where(); present {
		where, err := policybind.Predicate(publicWhere, registry, providers)
		if err != nil {
			return readir.Request{}, fail(CodePolicy, model, golem.FieldID{}, "where predicate did not bind", err)
		}
		if err := validatePublicCondition(where, registry); err != nil {
			return readir.Request{}, err
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
	for _, fieldID := range frozen.Omitted() {
		field, err := scalarField(registry, model, fieldID)
		if err != nil {
			return readir.Request{}, err
		}
		input.Omit = append(input.Omit, policyir.FieldID(field.ID()))
	}
	for _, publicSelection := range frozen.Selection() {
		field, ok := registry.Field(model, publicSelection.FieldID())
		if !ok {
			return readir.Request{}, fail(CodeField, model, publicSelection.FieldID(), "selected field is absent or belongs to another model", nil)
		}
		if !field.Visible() {
			return readir.Request{}, fail(CodeField, model, publicSelection.FieldID(), "hidden or write-only field cannot be selected by a public read", nil)
		}
		if !publicSelection.IsRelation() && !publicSelection.IsRelationCount() {
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
		if publicSelection.IsRelationCount() {
			if endpoint.Cardinality() != compilerir.RelationMany {
				return readir.Request{}, fail(CodeRelation, model, publicSelection.FieldID(), "relation count requires a to-many relation", nil)
			}
			selection, selectionErr := readir.NewRelationCountSelectionOccurrence(readir.OccurrenceID(publicSelection.OccurrenceID()), policyir.FieldID(field.ID()), policyir.RelationID(endpoint.RelationID()), policyir.ModelID(endpoint.TargetModelID()), child)
			if selectionErr != nil {
				return readir.Request{}, fail(CodeRelation, model, publicSelection.FieldID(), "relation-count selection is invalid", selectionErr)
			}
			input.Selection = append(input.Selection, selection)
			continue
		}
		selection, err := readir.NewRelationSelectionOccurrence(readir.OccurrenceID(publicSelection.OccurrenceID()), policyir.FieldID(field.ID()), policyir.RelationID(endpoint.RelationID()), policyir.ModelID(endpoint.TargetModelID()), child)
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

func bindSelector(public golem.FrozenUniqueSelector, model golem.ModelID, registry *schema.Registry) (readir.Selector, error) {
	modelFact, _ := registry.Model(model)
	identity, ok := modelFact.Identity(public.KeyID())
	if !ok || public.ModelID() != model || !sameFields(identity.Fields(), public.Fields()) {
		return readir.Selector{}, fail(CodeInput, model, golem.FieldID{}, "unique selector does not match the active schema", nil)
	}
	fields := make([]policyir.FieldID, len(public.Fields()))
	for index, field := range public.Fields() {
		if _, err := scalarField(registry, model, field); err != nil {
			return readir.Selector{}, err
		}
		fields[index] = policyir.FieldID(field)
	}
	selector, err := readir.NewSelector(public.KeyID(), fields)
	if err != nil {
		return readir.Selector{}, fail(CodeInput, model, golem.FieldID{}, "unique selector is invalid", err)
	}
	return selector, nil
}

func scalarField(registry *schema.Registry, model golem.ModelID, id golem.FieldID) (schema.Field, error) {
	field, ok := registry.Field(model, id)
	if !ok || field.Kind() == compilerir.FieldRelation {
		return schema.Field{}, fail(CodeField, model, id, "field is absent, belongs to another model, or is not scalar", nil)
	}
	if !field.Visible() {
		return schema.Field{}, fail(CodeField, model, id, "hidden or write-only field cannot be referenced by a public read", nil)
	}
	return field, nil
}

func validatePublicCondition(condition policyir.Condition, registry *schema.Registry) error {
	model := golem.ModelID(condition.ModelID())
	switch condition.Kind() {
	case policyir.ConditionConstant:
		return nil
	case policyir.ConditionLogical:
		_, children, ok := condition.Logical()
		if !ok {
			return fail(CodePolicy, model, golem.FieldID{}, "logical condition is malformed", nil)
		}
		for _, child := range children {
			if err := validatePublicCondition(child, registry); err != nil {
				return err
			}
		}
		return nil
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		field, ok := condition.Field()
		if !ok {
			return fail(CodePolicy, model, golem.FieldID{}, "field condition is malformed", nil)
		}
		_, err := scalarField(registry, model, golem.FieldID(field))
		return err
	case policyir.ConditionRelation:
		fieldID, relationID, targetID, _, child, ok := condition.Relation()
		if !ok {
			return fail(CodePolicy, model, golem.FieldID{}, "relation condition is malformed", nil)
		}
		field := golem.FieldID(fieldID)
		fact, present := registry.Field(model, field)
		if !present || fact.Kind() != compilerir.FieldRelation {
			return fail(CodeRelation, model, field, "relation condition does not identify an active relation field", nil)
		}
		if !fact.Visible() {
			return fail(CodeField, model, field, "hidden or write-only relation cannot be filtered by a public read", nil)
		}
		endpoint, present := registry.RelationEndpoint(model, field, golem.RelationID(relationID))
		if !present || endpoint.TargetModelID() != golem.ModelID(targetID) {
			return fail(CodeRelation, model, field, "relation condition does not match the active schema", nil)
		}
		if child != nil {
			return validatePublicCondition(*child, registry)
		}
		return nil
	default:
		return fail(CodePolicy, model, golem.FieldID{}, "condition kind is unknown", nil)
	}
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

func sameFields(left, right []golem.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
