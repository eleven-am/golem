package golem

import "fmt"

type RuntimeReadSelectionKind uint8

const (
	RuntimeReadScalar RuntimeReadSelectionKind = iota + 1
	RuntimeReadRelation
	RuntimeReadRelationCount
)

type RuntimeReadOrderInput struct {
	Field     FieldID
	Direction SortDirection
}

type RuntimeReadSelectorInput struct {
	Model  ModelID
	Key    KeyID
	Fields []FieldID
}

type RuntimeReadCursorInput struct {
	Selector RuntimeReadSelectorInput
	Where    FrozenPredicate
}

type RuntimeReadSelectionInput struct {
	Kind       RuntimeReadSelectionKind
	Field      FieldID
	Relation   RelationID
	Target     ModelID
	Occurrence RuntimeOccurrenceID
	Request    *FrozenReadRequest
}

// RuntimeReadRequestInput is the generated stable-identity boundary between
// the GraphQL operation compiler and the ordinary public P3 binder. It carries
// no public names and performs no authorization or SQL work.
type RuntimeReadRequestInput struct {
	Operation  ReadOperation
	Model      ModelID
	Where      *FrozenPredicate
	OrderBy    []RuntimeReadOrderInput
	Take       *int
	Skip       *int
	Distinct   []FieldID
	Selection  []RuntimeReadSelectionInput
	Projection ReadProjectionMode
	Omit       []FieldID
	Selector   *RuntimeReadSelectorInput
	Cursor     *RuntimeReadCursorInput
}

func RuntimeFreezeReadRequest(input RuntimeReadRequestInput) (FrozenReadRequest, error) {
	if input.Operation < ReadFindUnique || input.Operation > ReadCount || input.Model == (ModelID{}) {
		return FrozenReadRequest{}, fmt.Errorf("runtime read request: operation and model are required")
	}
	nodes := make([]readOptionNode, 0, 8)
	if input.Where != nil {
		where := cloneFrozenPredicate(*input.Where)
		nodes = append(nodes, readOptionNode{kind: readOptionWhere, freezePredicate: func(model ModelID) (FrozenPredicate, error) {
			if where.rootModel != model {
				return FrozenPredicate{}, fmt.Errorf("runtime read request: where model does not match request model")
			}
			return cloneFrozenPredicate(where), nil
		}})
	}
	if len(input.OrderBy) != 0 {
		orders := make([]readOrderNode, len(input.OrderBy))
		for index, order := range input.OrderBy {
			orders[index] = readOrderNode{field: order.Field, direction: order.Direction}
		}
		nodes = append(nodes, readOptionNode{kind: readOptionOrderBy, orders: orders})
	}
	if input.Take != nil {
		nodes = append(nodes, readOptionNode{kind: readOptionTake, integer: *input.Take})
	}
	if input.Skip != nil {
		nodes = append(nodes, readOptionNode{kind: readOptionSkip, integer: *input.Skip})
	}
	if len(input.Distinct) != 0 {
		nodes = append(nodes, readOptionNode{kind: readOptionDistinct, fields: append([]FieldID(nil), input.Distinct...)})
	}
	if len(input.Selection) != 0 {
		selection := make([]readSelectionNode, len(input.Selection))
		for index, value := range input.Selection {
			var err error
			selection[index], err = runtimeReadSelectionNode(value)
			if err != nil {
				return FrozenReadRequest{}, fmt.Errorf("runtime read request: selection %d: %w", index, err)
			}
		}
		kind := readOptionSelect
		if input.Projection == ProjectionInclude {
			kind = readOptionInclude
		} else if input.Projection != 0 && input.Projection != ProjectionSelect {
			return FrozenReadRequest{}, fmt.Errorf("runtime read request: selection requires select or include projection mode")
		}
		nodes = append(nodes, readOptionNode{kind: kind, selection: selection})
	} else if input.Projection == ProjectionSelect || input.Projection == ProjectionInclude {
		return FrozenReadRequest{}, fmt.Errorf("runtime read request: explicit projection is empty")
	} else if input.Projection != 0 && input.Projection != ProjectionDefault {
		return FrozenReadRequest{}, fmt.Errorf("runtime read request: projection mode is invalid")
	}
	if len(input.Omit) != 0 {
		nodes = append(nodes, readOptionNode{kind: readOptionOmit, fields: append([]FieldID(nil), input.Omit...)})
	}
	if input.Cursor != nil {
		components, err := selectorComponentsFromPredicate(input.Cursor.Where, input.Cursor.Selector.Fields)
		if err != nil {
			return FrozenReadRequest{}, fmt.Errorf("runtime read request: cursor: %w", err)
		}
		nodes = append(nodes, readOptionNode{kind: readOptionCursor, selectorModel: input.Cursor.Selector.Model, selectorKey: input.Cursor.Selector.Key, selectorValues: components})
	}
	request, err := freezeReadNodes(input.Operation, input.Model, nodes, 0)
	if err != nil {
		return FrozenReadRequest{}, err
	}
	if input.Selector != nil {
		if input.Operation != ReadFindUnique || input.Selector.Model != input.Model || input.Selector.Key == (KeyID{}) || len(input.Selector.Fields) == 0 {
			return FrozenReadRequest{}, fmt.Errorf("runtime read request: selector is incomplete or illegal for this operation")
		}
		request.selector = &FrozenUniqueSelector{model: input.Selector.Model, key: input.Selector.Key, fields: append([]FieldID(nil), input.Selector.Fields...)}
	} else if input.Operation == ReadFindUnique {
		return FrozenReadRequest{}, fmt.Errorf("runtime read request: findUnique requires selector metadata")
	}
	return request, nil
}

func runtimeReadSelectionNode(input RuntimeReadSelectionInput) (readSelectionNode, error) {
	result := readSelectionNode{field: input.Field, relation: input.Relation, target: input.Target, occurrence: input.Occurrence}
	switch input.Kind {
	case RuntimeReadScalar:
		result.kind = readSelectionScalar
		if input.Relation != (RelationID{}) || input.Target != (ModelID{}) || input.Occurrence != 0 || input.Request != nil {
			return readSelectionNode{}, fmt.Errorf("scalar selection carries relation data")
		}
	case RuntimeReadRelation, RuntimeReadRelationCount:
		if input.Relation == (RelationID{}) || input.Target == (ModelID{}) || input.Request == nil || input.Request.ModelID() != input.Target {
			return readSelectionNode{}, fmt.Errorf("relation selection identity or child request is incomplete")
		}
		if input.Kind == RuntimeReadRelation {
			result.kind = readSelectionRelation
		} else {
			result.kind = readSelectionRelationCount
		}
		options, err := thawReadOptions[struct{}](*input.Request, false)
		if err != nil {
			return readSelectionNode{}, err
		}
		result.options = make([]readOptionNode, len(options))
		var witness struct{}
		for index, option := range options {
			result.options[index] = option.readOption(witness)
		}
	default:
		return readSelectionNode{}, fmt.Errorf("selection kind is invalid")
	}
	return result, nil
}
