package golem

import "fmt"

// ReadState distinguishes an omitted field from a selected null and a selected
// value. ReadNull intentionally does not disclose whether null came from storage
// or authorization masking.
type ReadState uint8

const (
	ReadUnselected ReadState = iota
	ReadNull
	ReadPresent
)

// ReadValue is an immutable typed result cell.
type ReadValue[V any] struct {
	state ReadState
	value V
	clone func(V) V
}

func (value ReadValue[V]) State() ReadState { return value.state }
func (value ReadValue[V]) IsSelected() bool { return value.state != ReadUnselected }
func (value ReadValue[V]) IsNull() bool     { return value.state == ReadNull }
func (value ReadValue[V]) IsPresent() bool  { return value.state == ReadPresent }
func (value ReadValue[V]) Get() (V, bool) {
	if value.state != ReadPresent {
		var zero V
		return zero, false
	}
	if value.clone != nil {
		return value.clone(value.value), true
	}
	return value.value, true
}

type readCell struct {
	state ReadState
	value any
	clone func(any) any
}

// RuntimeReadCell is the representation-opaque handoff from the P3 decoder to
// the public Row. Application code cannot inspect or mutate its payload.
type RuntimeReadCell struct {
	field FieldID
	cell  readCell
}

func RuntimeNullReadCell(field FieldID) RuntimeReadCell {
	return RuntimeReadCell{field: field, cell: readCell{state: ReadNull}}
}

// RuntimePresentReadCell records one already decoded value. Mutable values must
// provide a copier; immutable scalar values may pass nil.
func RuntimePresentReadCell[V any](field FieldID, value V, clone func(V) V) RuntimeReadCell {
	cell := readCell{state: ReadPresent, value: value}
	if clone != nil {
		cell.clone = func(raw any) any { return clone(raw.(V)) }
	}
	return RuntimeReadCell{field: field, cell: cell}
}

// Row is one immutable, model-typed projected result.
type Row[M any] struct {
	model ModelID
	cells map[FieldID]readCell
	_     func() M
}

// RuntimeReadRow validates and owns decoded cells at the public runtime
// boundary. Duplicate and zero field identities are always rejected.
func RuntimeReadRow[M any](descriptor ModelDescriptor[M], cells ...RuntimeReadCell) (Row[M], error) {
	model := descriptor.Metadata().ModelID()
	if model == (ModelID{}) {
		return Row[M]{}, fmt.Errorf("read row: model descriptor has a zero identity")
	}
	result := Row[M]{model: model, cells: make(map[FieldID]readCell, len(cells))}
	for index, value := range cells {
		if value.field == (FieldID{}) {
			return Row[M]{}, fmt.Errorf("read row: cell %d has a zero field identity", index)
		}
		if value.cell.state != ReadNull && value.cell.state != ReadPresent {
			return Row[M]{}, fmt.Errorf("read row: cell %d has invalid state %d", index, value.cell.state)
		}
		if _, duplicate := result.cells[value.field]; duplicate {
			return Row[M]{}, fmt.Errorf("read row: duplicate field identity %x", value.field)
		}
		result.cells[value.field] = cloneReadCell(value.cell)
	}
	return result, nil
}

func cloneReadCell(cell readCell) readCell {
	if cell.state == ReadPresent && cell.clone != nil {
		cell.value = cell.clone(cell.value)
	}
	return cell
}

func cloneRow[M any](row Row[M]) Row[M] {
	result := Row[M]{model: row.model, cells: make(map[FieldID]readCell, len(row.cells))}
	for field, cell := range row.cells {
		result.cells[field] = cloneReadCell(cell)
	}
	return result
}

// Value reads one generated scalar column without exposing a string-keyed row.
func Value[M, V any](row Row[M], field ScalarColumn[M, V]) ReadValue[V] {
	if field == nil {
		return ReadValue[V]{}
	}
	cell, ok := row.cells[field.fieldIdentity()]
	if !ok {
		return ReadValue[V]{}
	}
	if cell.state == ReadNull {
		return ReadValue[V]{state: ReadNull}
	}
	typed, ok := cell.value.(V)
	if !ok {
		return ReadValue[V]{}
	}
	result := ReadValue[V]{state: ReadPresent, value: typed}
	if cell.clone != nil {
		result.clone = func(value V) V { return cell.clone(value).(V) }
	}
	return result
}

func One[M, R any](row Row[M], field ToOne[M, R]) ReadValue[Row[R]] {
	cell, ok := row.cells[field.fieldIdentity()]
	if !ok {
		return ReadValue[Row[R]]{}
	}
	if cell.state == ReadNull {
		return ReadValue[Row[R]]{state: ReadNull}
	}
	value, ok := cell.value.(Row[R])
	if !ok {
		return ReadValue[Row[R]]{}
	}
	return ReadValue[Row[R]]{state: ReadPresent, value: cloneRow(value), clone: cloneRow[R]}
}

func Many[M, R any](row Row[M], field ToMany[M, R]) ReadValue[[]Row[R]] {
	cell, ok := row.cells[field.fieldIdentity()]
	if !ok {
		return ReadValue[[]Row[R]]{}
	}
	if cell.state == ReadNull {
		return ReadValue[[]Row[R]]{state: ReadNull}
	}
	value, ok := cell.value.([]Row[R])
	if !ok {
		return ReadValue[[]Row[R]]{}
	}
	clone := func(rows []Row[R]) []Row[R] {
		result := make([]Row[R], len(rows))
		for index, item := range rows {
			result[index] = cloneRow(item)
		}
		return result
	}
	return ReadValue[[]Row[R]]{state: ReadPresent, value: clone(value), clone: clone}
}

type readSelectionKind uint8

const (
	readSelectionScalar readSelectionKind = iota + 1
	readSelectionRelation
)

type readSelectionNode struct {
	kind     readSelectionKind
	field    FieldID
	relation RelationID
	target   ModelID
	options  []readOptionNode
}

// Selection is sealed to generated scalar handles and validated generated
// relation selections.
type Selection[M any] interface {
	readSelection(M) readSelectionNode
}

type RelationSelection[M, R any] struct {
	node readSelectionNode
	_    func(M) R
}

func (selection RelationSelection[M, R]) readSelection(M) readSelectionNode {
	return cloneReadSelection(selection.node)
}

func (field ToOne[M, R]) Select(fields ...Selection[R]) RelationSelection[M, R] {
	return RelationSelection[M, R]{node: readSelectionNode{
		kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel,
		options: []readOptionNode{projectionOption(fields)},
	}}
}

func (field ToMany[M, R]) Select(fields ...Selection[R]) RelationSelection[M, R] {
	return field.Args(Select(fields...))
}

func (field ToMany[M, R]) Args(options ...ReadOption[R]) RelationSelection[M, R] {
	nodes := make([]readOptionNode, len(options))
	var witness R
	for index, option := range options {
		if option != nil {
			nodes[index] = option.readOption(witness)
		}
	}
	return RelationSelection[M, R]{node: readSelectionNode{
		kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel, options: nodes,
	}}
}

func cloneReadSelection(value readSelectionNode) readSelectionNode {
	value.options = cloneReadOptions(value.options)
	return value
}

type readOptionKind uint8

const (
	readOptionWhere readOptionKind = iota + 1
	readOptionOrderBy
	readOptionTake
	readOptionSkip
	readOptionDistinct
	readOptionSelect
)

type readOptionNode struct {
	kind            readOptionKind
	freezePredicate func(ModelID) (FrozenPredicate, error)
	orders          []readOrderNode
	integer         int
	fields          []FieldID
	selection       []readSelectionNode
}

type ReadOption[M any] interface {
	readOption(M) readOptionNode
}

type readOptionValue[M any] struct {
	node readOptionNode
	_    func() M
}

func (option readOptionValue[M]) readOption(M) readOptionNode { return cloneReadOption(option.node) }

func Where[M any](predicate Predicate[M]) ReadOption[M] {
	return readOptionValue[M]{node: readOptionNode{kind: readOptionWhere, freezePredicate: predicate.freezeForModel}}
}

type SortDirection uint8

const (
	SortAscending SortDirection = iota + 1
	SortDescending
)

type readOrderNode struct {
	field     FieldID
	direction SortDirection
}

type OrderTerm[M any] struct {
	node readOrderNode
	_    func() M
}

func orderTerm[M any](field FieldID, direction SortDirection) OrderTerm[M] {
	return OrderTerm[M]{node: readOrderNode{field: field, direction: direction}}
}

func OrderBy[M any](terms ...OrderTerm[M]) ReadOption[M] {
	orders := make([]readOrderNode, len(terms))
	for index, term := range terms {
		orders[index] = term.node
	}
	return readOptionValue[M]{node: readOptionNode{kind: readOptionOrderBy, orders: orders}}
}

func Take[M any](value int) ReadOption[M] {
	return readOptionValue[M]{node: readOptionNode{kind: readOptionTake, integer: value}}
}

func Skip[M any](value int) ReadOption[M] {
	return readOptionValue[M]{node: readOptionNode{kind: readOptionSkip, integer: value}}
}

func Distinct[M any](fields ...Column[M]) ReadOption[M] {
	identities := make([]FieldID, len(fields))
	for index, field := range fields {
		if field != nil {
			identities[index] = field.fieldIdentity()
		}
	}
	return readOptionValue[M]{node: readOptionNode{kind: readOptionDistinct, fields: identities}}
}

func Select[M any](fields ...Selection[M]) ReadOption[M] {
	return readOptionValue[M]{node: projectionOption(fields)}
}

func projectionOption[M any](fields []Selection[M]) readOptionNode {
	selection := make([]readSelectionNode, len(fields))
	var witness M
	for index, field := range fields {
		if field != nil {
			selection[index] = field.readSelection(witness)
		}
	}
	return readOptionNode{kind: readOptionSelect, selection: selection}
}

func cloneReadOption(value readOptionNode) readOptionNode {
	value.orders = append([]readOrderNode(nil), value.orders...)
	value.fields = append([]FieldID(nil), value.fields...)
	selection := value.selection
	value.selection = make([]readSelectionNode, len(selection))
	for index, selection := range selection {
		value.selection[index] = cloneReadSelection(selection)
	}
	return value
}

func cloneReadOptions(values []readOptionNode) []readOptionNode {
	result := make([]readOptionNode, len(values))
	for index, value := range values {
		result[index] = cloneReadOption(value)
	}
	return result
}

type ErrorCode string

const (
	CodeBadUserInput    ErrorCode = "BAD_USER_INPUT"
	CodeNotFound        ErrorCode = "NOT_FOUND"
	CodeUnauthenticated ErrorCode = "UNAUTHENTICATED"
	CodeForbidden       ErrorCode = "FORBIDDEN"
)

// Error is the transport-neutral public operation error. Cause is retained for
// trusted logs through Unwrap but is never part of Error's public text.
type Error struct {
	Code      ErrorCode
	Operation string
	Model     ModelID
	Field     FieldID
	Message   string
	cause     error
}

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	return string(failure.Code) + ": " + failure.Message
}

func (failure *Error) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

type ReadOperation uint8

const (
	ReadFindUnique ReadOperation = iota + 1
	ReadFindFirst
	ReadFindMany
	ReadCount
)

type FrozenReadOrder struct {
	field     FieldID
	direction SortDirection
}

func (order FrozenReadOrder) FieldID() FieldID         { return order.field }
func (order FrozenReadOrder) Direction() SortDirection { return order.direction }

type FrozenReadSelection struct {
	kind     readSelectionKind
	field    FieldID
	relation RelationID
	target   ModelID
	request  *FrozenReadRequest
}

func (selection FrozenReadSelection) FieldID() FieldID       { return selection.field }
func (selection FrozenReadSelection) RelationID() RelationID { return selection.relation }
func (selection FrozenReadSelection) TargetModelID() ModelID { return selection.target }
func (selection FrozenReadSelection) IsRelation() bool {
	return selection.kind == readSelectionRelation
}
func (selection FrozenReadSelection) Request() (FrozenReadRequest, bool) {
	if selection.request == nil {
		return FrozenReadRequest{}, false
	}
	return selection.request.clone(), true
}

// FrozenReadRequest is a schema-agnostic immutable read request. The P3 binder
// validates every identity against the active fingerprinted schema.
type FrozenReadRequest struct {
	operation ReadOperation
	model     ModelID
	where     *FrozenPredicate
	orders    []FrozenReadOrder
	take      *int
	skip      *int
	distinct  []FieldID
	selection []FrozenReadSelection
}

func (request FrozenReadRequest) Operation() ReadOperation { return request.operation }
func (request FrozenReadRequest) ModelID() ModelID         { return request.model }
func (request FrozenReadRequest) Where() (FrozenPredicate, bool) {
	if request.where == nil {
		return FrozenPredicate{}, false
	}
	return *request.where, true
}
func (request FrozenReadRequest) OrderBy() []FrozenReadOrder {
	return append([]FrozenReadOrder(nil), request.orders...)
}
func (request FrozenReadRequest) Take() (int, bool) {
	if request.take == nil {
		return 0, false
	}
	return *request.take, true
}
func (request FrozenReadRequest) Skip() (int, bool) {
	if request.skip == nil {
		return 0, false
	}
	return *request.skip, true
}
func (request FrozenReadRequest) Distinct() []FieldID {
	return append([]FieldID(nil), request.distinct...)
}
func (request FrozenReadRequest) Selection() []FrozenReadSelection {
	result := make([]FrozenReadSelection, len(request.selection))
	for index, value := range request.selection {
		result[index] = value.clone()
	}
	return result
}

func (request FrozenReadRequest) clone() FrozenReadRequest {
	result := request
	result.orders = request.OrderBy()
	result.distinct = request.Distinct()
	result.selection = request.Selection()
	if request.take != nil {
		value := *request.take
		result.take = &value
	}
	if request.skip != nil {
		value := *request.skip
		result.skip = &value
	}
	if request.where != nil {
		value := *request.where
		result.where = &value
	}
	return result
}

func (selection FrozenReadSelection) clone() FrozenReadSelection {
	if selection.request != nil {
		request := selection.request.clone()
		selection.request = &request
	}
	return selection
}

func FreezeFindMany[M any](descriptor ModelDescriptor[M], options ...ReadOption[M]) (FrozenReadRequest, error) {
	return freezeReadRequest(ReadFindMany, descriptor, options)
}

func FreezeFindFirst[M any](descriptor ModelDescriptor[M], options ...ReadOption[M]) (FrozenReadRequest, error) {
	return freezeReadRequest(ReadFindFirst, descriptor, options)
}

func FreezeCount[M any](descriptor ModelDescriptor[M], options ...ReadOption[M]) (FrozenReadRequest, error) {
	return freezeReadRequest(ReadCount, descriptor, options)
}

func freezeReadRequest[M any](operation ReadOperation, descriptor ModelDescriptor[M], options []ReadOption[M]) (FrozenReadRequest, error) {
	model := descriptor.Metadata().ModelID()
	if model == (ModelID{}) {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "model descriptor has a zero identity")
	}
	nodes := make([]readOptionNode, len(options))
	var witness M
	for index, option := range options {
		if option == nil {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, fmt.Sprintf("option %d is nil", index))
		}
		nodes[index] = option.readOption(witness)
	}
	return freezeReadNodes(operation, model, nodes, 0)
}

func freezeReadNodes(operation ReadOperation, model ModelID, nodes []readOptionNode, depth int) (FrozenReadRequest, error) {
	if depth > 64 {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "relation selection exceeds the structural depth limit")
	}
	result := FrozenReadRequest{operation: operation, model: model}
	seen := make(map[readOptionKind]bool)
	for index, node := range nodes {
		if node.kind < readOptionWhere || node.kind > readOptionSelect {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, fmt.Sprintf("option %d has an invalid kind", index))
		}
		if seen[node.kind] {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, fmt.Sprintf("option kind %d appears more than once", node.kind))
		}
		seen[node.kind] = true
		switch node.kind {
		case readOptionWhere:
			if node.freezePredicate == nil {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "where has no predicate")
			}
			predicate, err := node.freezePredicate(model)
			if err != nil {
				return FrozenReadRequest{}, invalidReadCause(operation, model, FieldID{}, "where predicate is invalid", err)
			}
			result.where = &predicate
		case readOptionOrderBy:
			if len(node.orders) == 0 {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "orderBy is empty")
			}
			ordered := make(map[FieldID]bool, len(node.orders))
			for _, order := range node.orders {
				if order.field == (FieldID{}) || (order.direction != SortAscending && order.direction != SortDescending) || ordered[order.field] {
					return FrozenReadRequest{}, invalidRead(operation, model, order.field, "orderBy contains a zero, duplicate, or invalid term")
				}
				ordered[order.field] = true
				result.orders = append(result.orders, FrozenReadOrder{field: order.field, direction: order.direction})
			}
		case readOptionTake:
			value := node.integer
			result.take = &value
		case readOptionSkip:
			if node.integer < 0 {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "skip must be non-negative")
			}
			value := node.integer
			result.skip = &value
		case readOptionDistinct:
			if len(node.fields) == 0 {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "distinct is empty")
			}
			fields := make(map[FieldID]bool, len(node.fields))
			for _, field := range node.fields {
				if field == (FieldID{}) || fields[field] {
					return FrozenReadRequest{}, invalidRead(operation, model, field, "distinct contains a zero or duplicate field")
				}
				fields[field] = true
				result.distinct = append(result.distinct, field)
			}
		case readOptionSelect:
			selection, err := freezeReadSelection(operation, model, node.selection, depth)
			if err != nil {
				return FrozenReadRequest{}, err
			}
			result.selection = selection
		}
	}
	if result.take != nil && *result.take < 0 && len(result.orders) == 0 {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "negative take requires orderBy")
	}
	if operation == ReadCount {
		if len(result.orders) != 0 || result.take != nil || result.skip != nil || len(result.distinct) != 0 || len(result.selection) != 0 {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "count accepts only where")
		}
	}
	return result, nil
}

func freezeReadSelection(operation ReadOperation, model ModelID, nodes []readSelectionNode, depth int) ([]FrozenReadSelection, error) {
	if len(nodes) == 0 {
		return nil, invalidRead(operation, model, FieldID{}, "select is empty")
	}
	seen := make(map[FieldID]bool, len(nodes))
	result := make([]FrozenReadSelection, 0, len(nodes))
	for _, node := range nodes {
		if node.field == (FieldID{}) || seen[node.field] {
			return nil, invalidRead(operation, model, node.field, "select contains a zero or duplicate field")
		}
		seen[node.field] = true
		selection := FrozenReadSelection{kind: node.kind, field: node.field}
		switch node.kind {
		case readSelectionScalar:
			if node.relation != (RelationID{}) || node.target != (ModelID{}) || len(node.options) != 0 {
				return nil, invalidRead(operation, model, node.field, "scalar selection carries relation data")
			}
		case readSelectionRelation:
			if node.relation == (RelationID{}) || node.target == (ModelID{}) {
				return nil, invalidRead(operation, model, node.field, "relation selection has incomplete generated identity")
			}
			child, err := freezeReadNodes(ReadFindMany, node.target, node.options, depth+1)
			if err != nil {
				return nil, err
			}
			selection.relation, selection.target, selection.request = node.relation, node.target, &child
		default:
			return nil, invalidRead(operation, model, node.field, "selection has an invalid kind")
		}
		result = append(result, selection)
	}
	return result, nil
}

func invalidRead(operation ReadOperation, model ModelID, field FieldID, message string) error {
	return &Error{Code: CodeBadUserInput, Operation: readOperationName(operation), Model: model, Field: field, Message: message}
}

func invalidReadCause(operation ReadOperation, model ModelID, field FieldID, message string, cause error) error {
	failure := invalidRead(operation, model, field, message).(*Error)
	failure.cause = cause
	return failure
}

func readOperationName(operation ReadOperation) string {
	switch operation {
	case ReadFindUnique:
		return "findUnique"
	case ReadFindFirst:
		return "findFirst"
	case ReadFindMany:
		return "findMany"
	case ReadCount:
		return "count"
	default:
		return "read"
	}
}
