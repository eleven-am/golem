package operation

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func (c *Compiler) freezeRequest(request readir.Request) (golem.FrozenReadRequest, error) {
	if c == nil || c.binder == nil {
		return golem.FrozenReadRequest{}, fmt.Errorf("operation compiler is absent")
	}
	operation, err := frozenReadOperation(request.Operation())
	if err != nil {
		return golem.FrozenReadRequest{}, err
	}
	input := golem.RuntimeReadRequestInput{Operation: operation, Model: golem.ModelID(request.ModelID())}
	if where, ok := request.Where(); ok {
		frozen, freezeErr := c.binder.FreezePredicate(where)
		if freezeErr != nil {
			return golem.FrozenReadRequest{}, freezeErr
		}
		input.Where = &frozen
	}
	for _, order := range request.OrderBy() {
		direction := golem.SortAscending
		if order.Direction() == readir.Descending {
			direction = golem.SortDescending
		}
		input.OrderBy = append(input.OrderBy, golem.RuntimeReadOrderInput{Field: golem.FieldID(order.FieldID()), Direction: direction})
	}
	if value, ok := request.Take(); ok {
		input.Take = &value
	}
	if value, ok := request.Skip(); ok {
		input.Skip = &value
	}
	for _, field := range request.Distinct() {
		input.Distinct = append(input.Distinct, golem.FieldID(field))
	}
	for _, field := range request.Omitted() {
		input.Omit = append(input.Omit, golem.FieldID(field))
	}
	switch request.ProjectionMode() {
	case readir.ProjectionDefault:
		input.Projection = golem.ProjectionDefault
	case readir.ProjectionSelect:
		input.Projection = golem.ProjectionSelect
	case readir.ProjectionInclude:
		input.Projection = golem.ProjectionInclude
	default:
		return golem.FrozenReadRequest{}, fmt.Errorf("projection mode %d is invalid", request.ProjectionMode())
	}
	for index, selection := range request.Selection() {
		converted, convertErr := c.freezeSelection(selection)
		if convertErr != nil {
			return golem.FrozenReadRequest{}, fmt.Errorf("selection %d: %w", index, convertErr)
		}
		input.Selection = append(input.Selection, converted)
	}
	if selector, ok := request.Selector(); ok {
		input.Selector = runtimeSelector(golem.ModelID(request.ModelID()), selector)
	}
	if cursor, ok := request.Cursor(); ok {
		where, freezeErr := c.binder.FreezePredicate(cursor.Predicate())
		if freezeErr != nil {
			return golem.FrozenReadRequest{}, freezeErr
		}
		input.Cursor = &golem.RuntimeReadCursorInput{Selector: *runtimeSelector(golem.ModelID(request.ModelID()), cursor.Selector()), Where: where}
	}
	return golem.RuntimeFreezeReadRequest(input)
}

func (c *Compiler) freezeSelection(selection readir.Selection) (golem.RuntimeReadSelectionInput, error) {
	result := golem.RuntimeReadSelectionInput{
		Field:      golem.FieldID(selection.FieldID()),
		Relation:   golem.RelationID(selection.RelationID()),
		Target:     golem.ModelID(selection.TargetModelID()),
		Occurrence: golem.RuntimeOccurrenceID(selection.OccurrenceID()),
	}
	switch selection.Kind() {
	case readir.SelectScalar:
		result.Kind = golem.RuntimeReadScalar
	case readir.SelectRelation, readir.SelectRelationCount:
		if selection.Kind() == readir.SelectRelation {
			result.Kind = golem.RuntimeReadRelation
		} else {
			result.Kind = golem.RuntimeReadRelationCount
		}
		child, ok := selection.Request()
		if !ok {
			return golem.RuntimeReadSelectionInput{}, fmt.Errorf("relation child request is absent")
		}
		frozen, err := c.freezeRequest(child)
		if err != nil {
			return golem.RuntimeReadSelectionInput{}, err
		}
		result.Request = &frozen
	default:
		return golem.RuntimeReadSelectionInput{}, fmt.Errorf("selection kind %d is invalid", selection.Kind())
	}
	return result, nil
}

func runtimeSelector(model golem.ModelID, selector readir.Selector) *golem.RuntimeReadSelectorInput {
	fields := selector.Fields()
	public := make([]golem.FieldID, len(fields))
	for index, field := range fields {
		public[index] = golem.FieldID(field)
	}
	return &golem.RuntimeReadSelectorInput{Model: model, Key: selector.KeyID(), Fields: public}
}

func frozenReadOperation(operation readir.Operation) (golem.ReadOperation, error) {
	switch operation {
	case readir.FindUnique:
		return golem.ReadFindUnique, nil
	case readir.FindFirst:
		return golem.ReadFindFirst, nil
	case readir.FindMany:
		return golem.ReadFindMany, nil
	case readir.Count:
		return golem.ReadCount, nil
	default:
		return 0, fmt.Errorf("read operation %d is invalid", operation)
	}
}
