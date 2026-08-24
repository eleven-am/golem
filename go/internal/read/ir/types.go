// Package ir owns the closed, immutable provider-neutral P3 read request.
package ir

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type Selector struct {
	key    golem.KeyID
	fields []policyir.FieldID
}

func NewSelector(key golem.KeyID, fields []policyir.FieldID) (Selector, error) {
	if key == (golem.KeyID{}) || len(fields) == 0 {
		return Selector{}, fmt.Errorf("P3_READ_IR_SELECTOR: key and fields are required")
	}
	seen := map[policyir.FieldID]bool{}
	result := Selector{key: key, fields: append([]policyir.FieldID(nil), fields...)}
	for _, field := range result.fields {
		if field == (policyir.FieldID{}) || seen[field] {
			return Selector{}, fmt.Errorf("P3_READ_IR_SELECTOR: fields are zero or duplicate")
		}
		seen[field] = true
	}
	return result, nil
}
func (selector Selector) KeyID() golem.KeyID { return selector.key }
func (selector Selector) Fields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), selector.fields...)
}

type Cursor struct {
	selector  Selector
	predicate policyir.Condition
}

func NewCursor(selector Selector, predicate policyir.Condition) (Cursor, error) {
	if selector.key == (golem.KeyID{}) || len(selector.fields) == 0 || predicate.ModelID() == (policyir.ModelID{}) {
		return Cursor{}, fmt.Errorf("P3_READ_IR_CURSOR: selector and predicate are required")
	}
	fields, err := cursorPredicateFields(predicate)
	if err != nil || !sameFieldSet(fields, selector.fields) {
		return Cursor{}, fmt.Errorf("P3_READ_IR_CURSOR: predicate must be equality on exactly the selector fields")
	}
	return Cursor{selector: Selector{key: selector.key, fields: selector.Fields()}, predicate: predicate}, nil
}

func cursorPredicateFields(condition policyir.Condition) ([]policyir.FieldID, error) {
	switch condition.Kind() {
	case policyir.ConditionScalar:
		operator, _ := condition.Operator()
		operand, _ := condition.Operand()
		field, _ := condition.Field()
		if operator != policyir.OperatorEqual || operand.Kind() != policyir.OperandOne || field == (policyir.FieldID{}) {
			return nil, fmt.Errorf("cursor leaf is not scalar equality")
		}
		return []policyir.FieldID{field}, nil
	case policyir.ConditionLogical:
		operator, children, _ := condition.Logical()
		if operator != policyir.LogicalAnd || len(children) == 0 {
			return nil, fmt.Errorf("cursor logical predicate is not conjunction")
		}
		var fields []policyir.FieldID
		for _, child := range children {
			values, err := cursorPredicateFields(child)
			if err != nil {
				return nil, err
			}
			fields = append(fields, values...)
		}
		return fields, nil
	default:
		return nil, fmt.Errorf("cursor predicate has unsupported condition kind")
	}
}

func sameFieldSet(left, right []policyir.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[policyir.FieldID]bool, len(left))
	for _, field := range left {
		if field == (policyir.FieldID{}) || seen[field] {
			return false
		}
		seen[field] = true
	}
	for _, field := range right {
		if !seen[field] {
			return false
		}
	}
	return true
}

func (cursor Cursor) Selector() Selector {
	return Selector{key: cursor.selector.key, fields: cursor.selector.Fields()}
}
func (cursor Cursor) Predicate() policyir.Condition { return cursor.predicate }

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

// OccurrenceID addresses one response-path occurrence without changing the
// stable field/relation identity used for authorization and SQL planning. Zero
// is reserved for the ordinary typed Go projection API.
type OccurrenceID uint32

const (
	SelectScalar SelectionKind = iota + 1
	SelectRelation
	SelectRelationCount
)

type ProjectionMode uint8

const (
	ProjectionDefault ProjectionMode = iota + 1
	ProjectionSelect
	ProjectionInclude
)

type Selection struct {
	kind       SelectionKind
	field      policyir.FieldID
	relation   policyir.RelationID
	target     policyir.ModelID
	request    *Request
	occurrence OccurrenceID
}

func NewScalarSelection(field policyir.FieldID) (Selection, error) {
	if field == (policyir.FieldID{}) {
		return Selection{}, fmt.Errorf("P3_READ_IR_SELECTION: scalar field is required")
	}
	return Selection{kind: SelectScalar, field: field}, nil
}

func NewRelationSelection(field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID, request Request) (Selection, error) {
	return NewRelationSelectionOccurrence(0, field, relation, target, request)
}

func NewRelationSelectionOccurrence(occurrence OccurrenceID, field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID, request Request) (Selection, error) {
	if field == (policyir.FieldID{}) || relation == (policyir.RelationID{}) || target == (policyir.ModelID{}) || request.model != target {
		return Selection{}, fmt.Errorf("P3_READ_IR_SELECTION: relation identities and matching child request are required")
	}
	child := request.clone()
	return Selection{kind: SelectRelation, field: field, relation: relation, target: target, request: &child, occurrence: occurrence}, nil
}

func NewRelationCountSelection(field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID, request Request) (Selection, error) {
	return NewRelationCountSelectionOccurrence(0, field, relation, target, request)
}

func NewRelationCountSelectionOccurrence(occurrence OccurrenceID, field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID, request Request) (Selection, error) {
	if field == (policyir.FieldID{}) || relation == (policyir.RelationID{}) || target == (policyir.ModelID{}) || request.model != target || request.operation != Count {
		return Selection{}, fmt.Errorf("P3_READ_IR_SELECTION: relation-count identities and matching count request are required")
	}
	child := request.clone()
	return Selection{kind: SelectRelationCount, field: field, relation: relation, target: target, request: &child, occurrence: occurrence}, nil
}

func (selection Selection) Kind() SelectionKind             { return selection.kind }
func (selection Selection) FieldID() policyir.FieldID       { return selection.field }
func (selection Selection) RelationID() policyir.RelationID { return selection.relation }
func (selection Selection) TargetModelID() policyir.ModelID { return selection.target }
func (selection Selection) OccurrenceID() OccurrenceID      { return selection.occurrence }
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
	Operation  Operation
	Model      policyir.ModelID
	Where      *policyir.Condition
	OrderBy    []Order
	Take       *int
	Skip       *int
	Distinct   []policyir.FieldID
	Selection  []Selection
	Projection ProjectionMode
	Omit       []policyir.FieldID
	Selector   *Selector
	Cursor     *Cursor
}

type Request struct {
	operation  Operation
	model      policyir.ModelID
	where      *policyir.Condition
	orders     []Order
	take       *int
	skip       *int
	distinct   []policyir.FieldID
	selection  []Selection
	projection ProjectionMode
	omit       []policyir.FieldID
	selector   *Selector
	cursor     *Cursor
}

func NewRequest(input RequestInput) (Request, error) {
	if input.Operation < FindUnique || input.Operation > Count || input.Model == (policyir.ModelID{}) {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: operation and model are required")
	}
	if input.Where != nil && input.Where.ModelID() != input.Model {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: where model does not match request model")
	}
	projection := input.Projection
	if projection == 0 {
		projection = ProjectionDefault
		// Preserve the pre-mode constructor contract for trusted internal
		// callers while all bound public requests carry an explicit mode.
		if len(input.Selection) != 0 {
			projection = ProjectionSelect
		}
	}
	if projection < ProjectionDefault || projection > ProjectionInclude {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: projection mode is invalid")
	}
	result := Request{operation: input.Operation, model: input.Model, projection: projection}
	if input.Where != nil {
		where := *input.Where
		result.where = &where
	}
	if input.Operation == FindUnique && input.Selector == nil {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: findUnique requires a selector")
	}
	if input.Operation != FindUnique && input.Selector != nil {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: selector is only valid for findUnique")
	}
	if input.Selector != nil {
		value := Selector{key: input.Selector.key, fields: input.Selector.Fields()}
		result.selector = &value
	}
	if input.Cursor != nil {
		if input.Operation != FindFirst && input.Operation != FindMany {
			return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: cursor is only valid for findFirst/findMany")
		}
		if input.Cursor.predicate.ModelID() != input.Model {
			return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: cursor model does not match request model")
		}
		value := Cursor{selector: input.Cursor.Selector(), predicate: input.Cursor.Predicate()}
		result.cursor = &value
	}
	result.orders = append([]Order(nil), input.OrderBy...)
	result.distinct = append([]policyir.FieldID(nil), input.Distinct...)
	result.omit = append([]policyir.FieldID(nil), input.Omit...)
	result.selection = make([]Selection, len(input.Selection))
	for index, selection := range input.Selection {
		if selection.field == (policyir.FieldID{}) || (selection.kind != SelectScalar && selection.kind != SelectRelation && selection.kind != SelectRelationCount) {
			return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: selection %d is invalid", index)
		}
		result.selection[index] = selection.clone()
	}
	if result.projection == ProjectionDefault && len(result.selection) != 0 {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: default projection cannot carry selections")
	}
	if result.projection == ProjectionSelect && (len(result.selection) == 0 || len(result.omit) != 0) {
		return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: select requires selections and cannot combine with omit")
	}
	if result.projection == ProjectionInclude {
		if len(result.selection) == 0 {
			return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: include requires relation selections")
		}
		for _, selection := range result.selection {
			if selection.kind != SelectRelation && selection.kind != SelectRelationCount {
				return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: include accepts relation and relation-count selections only")
			}
		}
	}
	type selectionIdentity struct {
		field policyir.FieldID
		kind  SelectionKind
	}
	seenSelection := make(map[selectionIdentity]Selection, len(result.selection))
	seenOccurrence := make(map[OccurrenceID]bool, len(result.selection))
	for _, selection := range result.selection {
		identity := selectionIdentity{field: selection.field, kind: selection.kind}
		if previous, duplicate := seenSelection[identity]; duplicate {
			if previous.occurrence == 0 || selection.occurrence == 0 || previous.occurrence == selection.occurrence {
				return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: selections are duplicate")
			}
		}
		if selection.occurrence != 0 && seenOccurrence[selection.occurrence] {
			return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: occurrence identities are duplicate")
		}
		seenSelection[identity] = selection
		if selection.occurrence != 0 {
			seenOccurrence[selection.occurrence] = true
		}
	}
	seenOmit := make(map[policyir.FieldID]bool, len(result.omit))
	for _, field := range result.omit {
		if field == (policyir.FieldID{}) || seenOmit[field] {
			return Request{}, fmt.Errorf("P3_READ_IR_REQUEST: omit fields are zero or duplicate")
		}
		seenOmit[field] = true
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
func (request Request) ProjectionMode() ProjectionMode { return request.projection }
func (request Request) Omitted() []policyir.FieldID {
	return append([]policyir.FieldID(nil), request.omit...)
}
func (request Request) Selector() (Selector, bool) {
	if request.selector == nil {
		return Selector{}, false
	}
	return Selector{key: request.selector.key, fields: request.selector.Fields()}, true
}
func (request Request) Cursor() (Cursor, bool) {
	if request.cursor == nil {
		return Cursor{}, false
	}
	return Cursor{selector: request.cursor.Selector(), predicate: request.cursor.Predicate()}, true
}
func (request Request) clone() Request {
	input := RequestInput{Operation: request.operation, Model: request.model, Where: request.where, OrderBy: request.orders, Take: request.take, Skip: request.skip, Distinct: request.distinct, Selection: request.selection, Projection: request.projection, Omit: request.omit, Selector: request.selector, Cursor: request.cursor}
	result, _ := NewRequest(input)
	return result
}

func NarrowCap(current, candidate int) int {
	if candidate > 0 && (current == 0 || candidate < current) {
		return candidate
	}
	return current
}
