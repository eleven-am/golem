package golem

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/concurrencyclaim"
)

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
	operation   RuntimeMutationOperation
	model       ModelID
	target      *FrozenMutationTarget
	where       *FrozenPredicate
	input       *FrozenMutationInput
	create      *FrozenMutationInput
	update      *FrozenMutationInput
	projection  *FrozenReadRequest
	existing    *ExistingVersion
	expectation *ConcurrencyExpectation
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

// RuntimeVersionedMutationRequestInput is the additive GraphQL-to-runtime
// boundary for one exact optimistic-concurrency claim. Keeping it separate
// preserves RuntimeMutationRequestInput's released unkeyed-literal shape.
type RuntimeVersionedMutationRequestInput struct {
	Request                RuntimeMutationRequestInput
	ExistingVersion        *ExistingVersion
	ConcurrencyExpectation *ConcurrencyExpectation
}

func RuntimeFreezeMutationRequest(input RuntimeMutationRequestInput) (RuntimeMutationRequest, error) {
	return runtimeFreezeMutationRequest(input, nil, nil)
}

// RuntimeFreezeVersionedMutationRequest freezes a versioned update, delete,
// or upsert request. The claim is copied and retained only in its closed public
// value type; callers cannot supply a raw integer authority.
func RuntimeFreezeVersionedMutationRequest(input RuntimeVersionedMutationRequestInput) (RuntimeMutationRequest, error) {
	hasExisting, hasExpectation := input.ExistingVersion != nil, input.ConcurrencyExpectation != nil
	switch input.Request.Operation {
	case RuntimeMutationUpdate, RuntimeMutationDelete:
		if !hasExisting || hasExpectation {
			return RuntimeMutationRequest{}, fmt.Errorf("runtime versioned mutation request: update and delete require exactly one existing-version claim")
		}
	case RuntimeMutationUpsert:
		if hasExisting || !hasExpectation {
			return RuntimeMutationRequest{}, fmt.Errorf("runtime versioned mutation request: upsert requires exactly one concurrency expectation")
		}
	default:
		return RuntimeMutationRequest{}, fmt.Errorf("runtime versioned mutation request: operation does not support a concurrency claim")
	}
	return runtimeFreezeMutationRequest(input.Request, input.ExistingVersion, input.ConcurrencyExpectation)
}

func runtimeFreezeMutationRequest(input RuntimeMutationRequestInput, existing *ExistingVersion, expectation *ConcurrencyExpectation) (RuntimeMutationRequest, error) {
	if input.Operation < RuntimeMutationCreate || input.Operation > RuntimeMutationDeleteMany || input.Model == (ModelID{}) {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: operation and model are required")
	}
	hasTarget, hasWhere := input.Target != nil, input.Where != nil
	hasInput, hasCreate, hasUpdate := input.Input != nil, input.Create != nil, input.Update != nil
	hasProjection := input.Projection != nil
	hasExisting, hasExpectation := existing != nil, expectation != nil
	valid := false
	switch input.Operation {
	case RuntimeMutationCreate:
		valid = !hasTarget && !hasWhere && hasInput && !hasCreate && !hasUpdate && hasProjection && !hasExisting && !hasExpectation
	case RuntimeMutationUpdate:
		valid = hasTarget && !hasWhere && hasInput && !hasCreate && !hasUpdate && hasProjection && !hasExpectation
	case RuntimeMutationUpsert:
		valid = hasTarget && !hasWhere && !hasInput && hasCreate && hasUpdate && hasProjection && !hasExisting
	case RuntimeMutationDelete:
		valid = hasTarget && !hasWhere && !hasInput && !hasCreate && !hasUpdate && hasProjection && !hasExpectation
	case RuntimeMutationUpdateMany:
		valid = !hasTarget && hasWhere && hasInput && !hasCreate && !hasUpdate && !hasProjection && !hasExisting && !hasExpectation
	case RuntimeMutationDeleteMany:
		valid = !hasTarget && hasWhere && !hasInput && !hasCreate && !hasUpdate && !hasProjection && !hasExisting && !hasExpectation
	}
	if !valid {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: argument shape does not match operation")
	}
	if hasExisting && !concurrencyclaim.ValidExistingVersion(concurrencyclaim.ExistingVersion(*existing)) {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: existing-version expectation is invalid")
	}
	if hasExpectation && !concurrencyclaim.ValidExpectation(concurrencyclaim.ConcurrencyExpectation(*expectation)) {
		return RuntimeMutationRequest{}, fmt.Errorf("runtime mutation request: concurrency expectation is invalid")
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
	if existing != nil {
		value := *existing
		result.existing = &value
	}
	if expectation != nil {
		value := *expectation
		result.expectation = &value
	}
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
func (request RuntimeMutationRequest) ExistingVersion() (ExistingVersion, bool) {
	if request.existing == nil {
		return ExistingVersion{}, false
	}
	return *request.existing, true
}
func (request RuntimeMutationRequest) ConcurrencyExpectation() (ConcurrencyExpectation, bool) {
	if request.expectation == nil {
		return ConcurrencyExpectation{}, false
	}
	return *request.expectation, true
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
