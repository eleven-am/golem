package golem

import (
	"context"
	"fmt"
)

// RuntimeReadHookRequest is the immutable model-erased handoff used by P3
// GraphQL execution. Generated hook bindings recover their concrete model type
// without reflection and return a freshly frozen transformed request.
type RuntimeReadHookRequest struct {
	model     ModelID
	operation HookOperation
	request   FrozenReadRequest
}

func RuntimeReadHookRequestFromFrozen(request FrozenReadRequest) (RuntimeReadHookRequest, error) {
	operation, ok := readHookOperation(request.Operation())
	if !ok || request.ModelID() == (ModelID{}) {
		return RuntimeReadHookRequest{}, fmt.Errorf("generated read hook: unsupported or incomplete request")
	}
	return RuntimeReadHookRequest{model: request.ModelID(), operation: operation, request: request.clone()}, nil
}

func (request RuntimeReadHookRequest) ModelID() ModelID         { return request.model }
func (request RuntimeReadHookRequest) Operation() HookOperation { return request.operation }
func (request RuntimeReadHookRequest) Request() FrozenReadRequest {
	return request.request.clone()
}

// RuntimeReadHookResult carries detached masked rows only. The generated
// binding restores Row[M] before application hook code runs.
type RuntimeReadHookResult struct {
	model     ModelID
	operation HookOperation
	rows      []RuntimeModelRow
	found     bool
}

func RuntimeReadHookRows(request RuntimeReadHookRequest, rows []RuntimeModelRow, found bool) RuntimeReadHookResult {
	return RuntimeReadHookResult{model: request.model, operation: request.operation, rows: cloneRuntimeModelRows(rows), found: found}
}

func (result RuntimeReadHookResult) ModelID() ModelID         { return result.model }
func (result RuntimeReadHookResult) Operation() HookOperation { return result.operation }
func (result RuntimeReadHookResult) Rows() []RuntimeModelRow {
	return cloneRuntimeModelRows(result.rows)
}
func (result RuntimeReadHookResult) Found() bool { return result.found }

type readBeforeBridge func(context.Context, RuntimeReadHookRequest) (RuntimeReadHookRequest, error)
type readResultBridge func(context.Context, RuntimeReadHookResult) error

func generatedReadBeforeBridge[M, Request any](operation HookOperation, invoke func(context.Context, *Request) error) readBeforeBridge {
	if operation != HookFindOne && operation != HookFindFirst && operation != HookFindMany {
		return nil
	}
	return func(ctx context.Context, erased RuntimeReadHookRequest) (RuntimeReadHookRequest, error) {
		if erased.model == (ModelID{}) || erased.operation != operation || invoke == nil {
			return RuntimeReadHookRequest{}, fmt.Errorf("generated read hook: invalid before-hook envelope")
		}
		options, err := thawReadOptions[M](erased.request, operation == HookFindOne)
		if err != nil {
			return RuntimeReadHookRequest{}, err
		}
		descriptor := GeneratedModelDescriptor[M](erased.model, GeneratedDescriptorShape(nil, nil, nil, nil))
		var frozen FrozenReadRequest
		switch operation {
		case HookFindOne:
			selector, err := thawReadSelector[M](erased.request)
			if err != nil {
				return RuntimeReadHookRequest{}, err
			}
			typed := RuntimeFindOneHookRequest(selector, options)
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeReadHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeReadHookRequest{}, err
			}
			frozen, err = FreezeFindUnique(descriptor, typed.Selector(), typed.Options()...)
			if err != nil {
				return RuntimeReadHookRequest{}, err
			}
		case HookFindFirst:
			typed := RuntimeFindFirstHookRequest(options)
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeReadHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeReadHookRequest{}, err
			}
			frozen, err = FreezeFindFirst(descriptor, typed.Options()...)
			if err != nil {
				return RuntimeReadHookRequest{}, err
			}
		case HookFindMany:
			typed := RuntimeFindManyHookRequest(options)
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeReadHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeReadHookRequest{}, err
			}
			frozen, err = FreezeFindMany(descriptor, typed.Options()...)
			if err != nil {
				return RuntimeReadHookRequest{}, err
			}
		}
		return RuntimeReadHookRequestFromFrozen(frozen)
	}
}

func generatedReadResultBridge[M, Result any](operation HookOperation, invoke func(context.Context, Result) error) readResultBridge {
	if operation != HookFindOne && operation != HookFindFirst && operation != HookFindMany {
		return nil
	}
	return func(ctx context.Context, erased RuntimeReadHookResult) error {
		if erased.model == (ModelID{}) || erased.operation != operation || invoke == nil {
			return fmt.Errorf("generated read hook: invalid result envelope")
		}
		rows := make([]Row[M], len(erased.rows))
		for index, row := range erased.rows {
			var err error
			rows[index], err = runtimeTypedHookRow[M](erased.model, row)
			if err != nil {
				return err
			}
		}
		var payload any
		switch operation {
		case HookFindOne:
			if len(rows) != 1 || !erased.found {
				return errGeneratedBindingType
			}
			payload = RuntimeFindOneHookResult(rows[0])
		case HookFindFirst:
			if len(rows) > 1 || erased.found != (len(rows) == 1) {
				return errGeneratedBindingType
			}
			var row Row[M]
			if erased.found {
				row = rows[0]
			}
			payload = RuntimeFindFirstHookResult(row, erased.found)
		case HookFindMany:
			payload = RuntimeFindManyHookResult(rows)
		}
		result, ok := payload.(Result)
		if !ok {
			return errGeneratedBindingType
		}
		return invoke(ctx, result)
	}
}

func readHookOperation(operation ReadOperation) (HookOperation, bool) {
	switch operation {
	case ReadFindUnique:
		return HookFindOne, true
	case ReadFindFirst:
		return HookFindFirst, true
	case ReadFindMany:
		return HookFindMany, true
	default:
		return "", false
	}
}

func thawReadSelector[M any](request FrozenReadRequest) (UniqueSelectorValue[M], error) {
	selector, ok := request.Selector()
	if !ok {
		return UniqueSelectorValue[M]{}, fmt.Errorf("generated read hook: find-one selector is absent")
	}
	predicate, ok := request.Where()
	if !ok {
		return UniqueSelectorValue[M]{}, fmt.Errorf("generated read hook: find-one selector predicate is absent")
	}
	components, err := selectorComponentsFromPredicate(predicate, selector.Fields())
	if err != nil {
		return UniqueSelectorValue[M]{}, err
	}
	return GeneratedUniqueSelectorValue[M](selector.ModelID(), selector.KeyID(), components...), nil
}

func selectorComponentsFromPredicate(predicate FrozenPredicate, fields []FieldID) ([]selectorComponent, error) {
	byField := map[FieldID]frozenOperand{}
	var visit func(*frozenCondition) error
	visit = func(node *frozenCondition) error {
		if node == nil {
			return fmt.Errorf("generated read hook: selector predicate is empty")
		}
		if node.kind == FrozenConditionLogical && node.operator == FrozenOperatorAnd {
			for _, child := range node.children {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}
		if node.kind != FrozenConditionScalar || node.operator != FrozenOperatorEq || node.operand.kind != FrozenOperandOne || node.field == (FieldID{}) {
			return fmt.Errorf("generated read hook: selector predicate is not an equality conjunction")
		}
		if _, duplicate := byField[node.field]; duplicate {
			return fmt.Errorf("generated read hook: selector predicate repeats a field")
		}
		byField[node.field] = cloneFrozenOperand(node.operand)
		return nil
	}
	if err := visit(predicate.root); err != nil {
		return nil, err
	}
	if len(byField) != len(fields) {
		return nil, fmt.Errorf("generated read hook: selector predicate field set differs from selector metadata")
	}
	result := make([]selectorComponent, len(fields))
	for index, field := range fields {
		operand, ok := byField[field]
		if !ok {
			return nil, fmt.Errorf("generated read hook: selector predicate is missing a component")
		}
		result[index] = selectorComponent{field: field, operand: operand}
	}
	return result, nil
}

func thawReadOptions[M any](request FrozenReadRequest, omitSelectorWhere bool) ([]ReadOption[M], error) {
	options := make([]ReadOption[M], 0, 8)
	if predicate, ok := request.Where(); ok && !omitSelectorWhere {
		frozen := cloneFrozenPredicate(predicate)
		options = append(options, readOptionValue[M]{node: readOptionNode{kind: readOptionWhere, freezePredicate: func(ModelID) (FrozenPredicate, error) { return cloneFrozenPredicate(frozen), nil }}})
	}
	if orders := request.OrderBy(); len(orders) != 0 {
		nodes := make([]readOrderNode, len(orders))
		for index, order := range orders {
			nodes[index] = readOrderNode{field: order.FieldID(), direction: order.Direction()}
		}
		options = append(options, readOptionValue[M]{node: readOptionNode{kind: readOptionOrderBy, orders: nodes}})
	}
	if value, ok := request.Take(); ok {
		options = append(options, Take[M](value))
	}
	if value, ok := request.Skip(); ok {
		options = append(options, Skip[M](value))
	}
	if fields := request.Distinct(); len(fields) != 0 {
		options = append(options, readOptionValue[M]{node: readOptionNode{kind: readOptionDistinct, fields: fields}})
	}
	if cursor, ok := request.Cursor(); ok {
		components, err := selectorComponentsFromPredicate(cursor.Predicate(), cursor.Selector().Fields())
		if err != nil {
			return nil, err
		}
		options = append(options, readOptionValue[M]{node: readOptionNode{kind: readOptionCursor, selectorModel: cursor.Selector().ModelID(), selectorKey: cursor.Selector().KeyID(), selectorValues: components}})
	}
	if selections := request.Selection(); len(selections) != 0 {
		nodes := make([]readSelectionNode, len(selections))
		for index, selection := range selections {
			var err error
			nodes[index], err = thawReadSelection(selection)
			if err != nil {
				return nil, err
			}
		}
		kind := readOptionSelect
		if request.ProjectionMode() == ProjectionInclude {
			kind = readOptionInclude
		}
		options = append(options, readOptionValue[M]{node: readOptionNode{kind: kind, selection: nodes}})
	}
	if fields := request.Omitted(); len(fields) != 0 {
		options = append(options, readOptionValue[M]{node: readOptionNode{kind: readOptionOmit, fields: fields}})
	}
	return options, nil
}

func thawReadSelection(selection FrozenReadSelection) (readSelectionNode, error) {
	kind := readSelectionScalar
	if selection.IsRelation() {
		kind = readSelectionRelation
	} else if selection.IsRelationCount() {
		kind = readSelectionRelationCount
	}
	result := readSelectionNode{kind: kind, field: selection.FieldID(), relation: selection.RelationID(), target: selection.TargetModelID(), occurrence: selection.OccurrenceID()}
	if child, ok := selection.Request(); ok {
		options, err := thawReadOptions[struct{}](child, false)
		if err != nil {
			return readSelectionNode{}, err
		}
		result.options = make([]readOptionNode, len(options))
		var witness struct{}
		for index, option := range options {
			result.options[index] = option.readOption(witness)
		}
	}
	return result, nil
}
