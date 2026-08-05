// Package ir owns the closed, immutable provider-neutral P3 read request.
package ir

import (
	"fmt"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type Operation uint8

const (
	FindUnique Operation = iota + 1
	FindFirst
	FindMany
	Count
)

type SortDirection uint8

const (
	Ascending SortDirection = iota + 1
	Descending
)

type Order struct {
	field     policyir.FieldID
	direction SortDirection
}

func NewOrder(field policyir.FieldID, direction SortDirection) (Order, error) {
	if field == (policyir.FieldID{}) || (direction != Ascending && direction != Descending) {
		return Order{}, fmt.Errorf("P3_READ_IR_ORDER: field and direction are required")
	}
	return Order{field: field, direction: direction}, nil
}

func (order Order) FieldID() policyir.FieldID { return order.field }
func (order Order) Direction() SortDirection  { return order.direction }

type SelectionKind uint8

const (
	SelectScalar SelectionKind = iota + 1
	SelectRelation
)

type Selection struct {
	kind     SelectionKind
	field    policyir.FieldID
	relation policyir.RelationID
	target   policyir.ModelID
	request  *Request
}

func NewScalarSelection(field policyir.FieldID) (Selection, error) {
	if field == (policyir.FieldID{}) {
		return Selection{}, fmt.Errorf("P3_READ_IR_SELECTION: scalar field is required")
	}
	return Selection{kind: SelectScalar, field: field}, nil
}

func NewRelationSelection(field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID, request Request) (Selection, error) {
	if field == (policyir.FieldID{}) || relation == (policyir.RelationID{}) || target == (policyir.ModelID{}) || request.model != target {
		return Selection{}, fmt.Errorf("P3_READ_IR_SELECTION: relation identities and matching child request are required")
	}
	child := request.clone()
	return Selection{kind: SelectRelation, field: field, relation: relation, target: target, request: &child}, nil
}

func (selection Selection) Kind() SelectionKind             { return selection.kind }
func (selection Selection) FieldID() policyir.FieldID       { return selection.field }
func (selection Selection) RelationID() policyir.RelationID { return selection.relation }
func (selection Selection) TargetModelID() policyir.ModelID { return selection.target }
func (selection Selection) Request() (Request, bool) {
	if selection.request == nil {
		return Request{}, false
	}
	return selection.request.clone(), true
}
func (selection Selection) clone() Selection {
	if selection.request != nil {
		child := selection.request.clone()
		selection.request = &child
	}
	return selection
}

type RequestInput struct {
	Operation Operation
	Model     policyir.ModelID
	Where     *policyir.Condition
	OrderBy   []Order
	Take      *int
	Skip      *int
	Distinct  []policyir.FieldID
	Selection []Selection
}

type Request struct {
	operation Operation
	model     policyir.ModelID
	where     *policyir.Condition
	orders    []Order
	take      *int
	skip      *int
	distinct  []policyir.FieldID
	selection []Selection
}

func NewRequest(input RequestInput) (Request, error) {
	if input.Operation < FindUnique || input.Operation > Count || input.Model == (policyir.ModelID{}) {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: operation and model are required")
	}
	if input.Where != nil && input.Where.ModelID() != input.Model {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: where model does not match request model")
	}
	result := Request{operation: input.Operation, model: input.Model}
	if input.Where != nil {
		where := *input.Where
		result.where = &where
	}
	result.orders = append([]Order(nil), input.OrderBy...)
	result.distinct = append([]policyir.FieldID(nil), input.Distinct...)
	result.selection = make([]Selection, len(input.Selection))
	for index, selection := range input.Selection {
		if selection.field == (policyir.FieldID{}) || (selection.kind != SelectScalar && selection.kind != SelectRelation) {
			return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: selection %d is invalid", index)
		}
		result.selection[index] = selection.clone()
	}
	if input.Take != nil {
		value := *input.Take
		result.take = &value
	}
	if input.Skip != nil {
		value := *input.Skip
		result.skip = &value
	}
	return result, nil
}

func (request Request) Operation() Operation      { return request.operation }
func (request Request) ModelID() policyir.ModelID { return request.model }
func (request Request) Where() (policyir.Condition, bool) {
	if request.where == nil {
		return policyir.Condition{}, false
	}
	return *request.where, true
}
func (request Request) OrderBy() []Order { return append([]Order(nil), request.orders...) }
func (request Request) Take() (int, bool) {
	if request.take == nil {
		return 0, false
	}
	return *request.take, true
}
func (request Request) Skip() (int, bool) {
	if request.skip == nil {
		return 0, false
	}
	return *request.skip, true
}
func (request Request) Distinct() []policyir.FieldID {
	return append([]policyir.FieldID(nil), request.distinct...)
}
func (request Request) Selection() []Selection {
	result := make([]Selection, len(request.selection))
	for index, selection := range request.selection {
		result[index] = selection.clone()
	}
	return result
}
func (request Request) clone() Request {
	input := RequestInput{Operation: request.operation, Model: request.model, Where: request.where, OrderBy: request.orders, Take: request.take, Skip: request.skip, Distinct: request.distinct, Selection: request.selection}
	result, _ := NewRequest(input)
	return result
}
