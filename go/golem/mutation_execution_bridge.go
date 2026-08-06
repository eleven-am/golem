package golem

import "fmt"

// RuntimeMutationOperation is the model-erased form of the six P4 root
// mutation operations. Generated GraphQL adapters use it after every public
// name and exact value has already been bound.
type RuntimeMutationOperation uint8

const (
	RuntimeMutationCreate RuntimeMutationOperation = iota + 1
	RuntimeMutationUpdate
	RuntimeMutationUpsert
	RuntimeMutationDelete
	RuntimeMutationUpdateMany
	RuntimeMutationDeleteMany
)

// RuntimeMutationRequest is the immutable P5-to-P4 execution boundary. It
// contains only frozen P4 inputs and a frozen P3 result projection. Runtime
// execution rebinds all of them against the active schema before database work.
type RuntimeMutationRequest struct {
	operation  RuntimeMutationOperation
	model      ModelID
	target     *FrozenMutationTarget
	where      *FrozenPredicate
	input      *FrozenMutationInput
	create     *FrozenMutationInput
	update     *FrozenMutationInput
	projection *FrozenReadRequest
}

type RuntimeMutationRequestInput struct {
	Operation  RuntimeMutationOperation
	Model      ModelID
	Target     *FrozenMutationTarget
	Where      *FrozenPredicate
	Input      *FrozenMutationInput
	Create     *FrozenMutationInput
	Update     *FrozenMutationInput
	Projection *FrozenReadRequest
}

func RuntimeFreezeMutationRequest(input RuntimeMutationRequestInput) (RuntimeMutationRequest, error) {
	if input.Operation < RuntimeMutationCreate || input.Operation > RuntimeMutationDeleteMany || input.Model == (ModelID{}) {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: operation and model are required")
	}
	hasTarget, hasWhere := input.Target != nil, input.Where != nil
	hasInput, hasCreate, hasUpdate := input.Input != nil, input.Create != nil, input.Update != nil
	hasProjection := input.Projection != nil
	valid := false
	switch input.Operation {
	case RuntimeMutationCreate:
		valid = !hasTarget && !hasWhere && hasInput && !hasCreate && !hasUpdate && hasProjection
	case RuntimeMutationUpdate:
		valid = hasTarget && !hasWhere && hasInput && !hasCreate && !hasUpdate && hasProjection
	case RuntimeMutationUpsert:
		valid = hasTarget && !hasWhere && !hasInput && hasCreate && hasUpdate && hasProjection
	case RuntimeMutationDelete:
		valid = hasTarget && !hasWhere && !hasInput && !hasCreate && !hasUpdate && hasProjection
	case RuntimeMutationUpdateMany:
		valid = !hasTarget && hasWhere && hasInput && !hasCreate && !hasUpdate && !hasProjection
	case RuntimeMutationDeleteMany:
		valid = !hasTarget && hasWhere && !hasInput && !hasCreate && !hasUpdate && !hasProjection
	}
	if !valid {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: argument shape does not match operation")
	}
	if input.Target != nil && input.Target.Selector().ModelID() != input.Model {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: target belongs to another model")
	}
	if input.Where != nil && input.Where.View().RootModelID() != input.Model {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: predicate belongs to another model")
	}
	for _, mutationInput := range []*FrozenMutationInput{input.Input, input.Create, input.Update} {
		if mutationInput != nil && mutationInput.ModelID() != input.Model {
			return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: mutation input belongs to another model")
		}
	}
	if input.Projection != nil {
		if input.Projection.ModelID() != input.Model || input.Projection.Operation() != ReadFindMany || input.Projection.ProjectionMode() != ProjectionSelect || len(input.Projection.Selection()) == 0 {
			return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: result projection must be a non-empty find-many select for the mutation model")
		}
	}
	result := RuntimeMutationRequest{operation: input.Operation, model: input.Model}
	if input.Target != nil {
		value := cloneFrozenMutationTarget(*input.Target)
		result.target = &value
	}
	if input.Where != nil {
		value := cloneFrozenPredicate(*input.Where)
		result.where = &value
	}
	if input.Input != nil {
		value := cloneFrozenMutationInput(*input.Input)
		result.input = &value
	}
	if input.Create != nil {
		value := cloneFrozenMutationInput(*input.Create)
		result.create = &value
	}
	if input.Update != nil {
		value := cloneFrozenMutationInput(*input.Update)
		result.update = &value
	}
	if input.Projection != nil {
		value := input.Projection.clone()
		result.projection = &value
	}
	return result, nil
}

func (request RuntimeMutationRequest) Operation() RuntimeMutationOperation { return request.operation }
func (request RuntimeMutationRequest) ModelID() ModelID                    { return request.model }
func (request RuntimeMutationRequest) Target() (FrozenMutationTarget, bool) {
	if request.target == nil {
		return FrozenMutationTarget{}, false
	}
	return cloneFrozenMutationTarget(*request.target), true
}
func (request RuntimeMutationRequest) Where() (FrozenPredicate, bool) {
	if request.where == nil {
		return FrozenPredicate{}, false
	}
	return cloneFrozenPredicate(*request.where), true
}
func (request RuntimeMutationRequest) Input() (FrozenMutationInput, bool) {
	if request.input == nil {
		return FrozenMutationInput{}, false
	}
	return cloneFrozenMutationInput(*request.input), true
}
func (request RuntimeMutationRequest) CreateInput() (FrozenMutationInput, bool) {
	if request.create == nil {
		return FrozenMutationInput{}, false
	}
	return cloneFrozenMutationInput(*request.create), true
}
func (request RuntimeMutationRequest) UpdateInput() (FrozenMutationInput, bool) {
	if request.update == nil {
		return FrozenMutationInput{}, false
	}
	return cloneFrozenMutationInput(*request.update), true
}
func (request RuntimeMutationRequest) Projection() (FrozenReadRequest, bool) {
	if request.projection == nil {
		return FrozenReadRequest{}, false
	}
	return request.projection.clone(), true
}

type RuntimeMutationResult struct {
	row          *RuntimeModelRow
	count        int64
	countPresent bool
}

func RuntimeMutationRowResult(row RuntimeModelRow) RuntimeMutationResult {
	copy := cloneRuntimeModelRow(row)
	return RuntimeMutationResult{row: &copy}
}

func RuntimeMutationCountResult(count int64) (RuntimeMutationResult, error) {
	if count < 0 {
		return RuntimeMutationResult{}, fmt.Errorf("runtime mutation result: count cannot be negative")
	}
	return RuntimeMutationResult{count: count, countPresent: true}, nil
}

func (result RuntimeMutationResult) Row() (RuntimeModelRow, bool) {
	if result.row == nil {
		return RuntimeModelRow{}, false
	}
	return cloneRuntimeModelRow(*result.row), true
}

func (result RuntimeMutationResult) Count() (int64, bool) {
	return result.count, result.countPresent && result.row == nil
}
