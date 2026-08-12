// Package mutation lowers already-coerced, stable-identity GraphQL mutation
// inputs into P4's frozen public boundary. It performs no authorization, SQL,
// hook, transaction, invalidation, or fact work.
package mutation

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type InputKind uint8

const (
	CreateInput InputKind = iota + 1
	UpdateInput
	UpdateManyInput
)

type RootOperation uint8

const (
	Create RootOperation = iota + 1
	Update
	Upsert
	Delete
	UpdateMany
	DeleteMany
)

type Limits struct {
	MaxInputDepth int
	MaxInputNodes int
	MaxListItems  int
}

type Scalar struct {
	Field     golem.FieldID
	Operation golem.MutationFieldOperation
	Value     any
}

// RelationEntry is one ordered list member in a generated nested relation
// operation. Only the members used by that operation may be populated.
type RelationEntry struct {
	Target    *golem.FrozenMutationTarget
	Predicate *golem.FrozenPredicate
	Create    *Input
	Update    *Input
}

// Relation represents one member of a generated relation input object.
// Entries preserve GraphQL list order. A Set with zero entries is clear-all;
// targetless to-one operations use one empty entry.
type Relation struct {
	Field       golem.FieldID
	Relation    golem.RelationID
	TargetModel golem.ModelID
	Action      golem.MutationRelationAction
	Entries     []RelationEntry
}

// Input represents a presence-aware generated GraphQL input. Omitted fields
// and relations are absent from their slices. Explicit create null is Scalar
// with MutationFieldNull; it is therefore distinct from omission.
type Input struct {
	Kind      InputKind
	Model     golem.ModelID
	Scalars   []Scalar
	Relations []Relation
}

// RootInput is the six-root GraphQL mutation vocabulary after public names,
// exact scalars, selectors, predicates, and result selections are bound.
type RootInput struct {
	Operation              RootOperation
	Model                  golem.ModelID
	Target                 *golem.FrozenMutationTarget
	Where                  *golem.FrozenPredicate
	Data                   *Input
	Create                 *Input
	Update                 *Input
	Selection              []readir.Selection
	ExistingVersion        *golem.ExistingVersion
	ConcurrencyExpectation *golem.ConcurrencyExpectation
}

// Request is the immutable handoff from generated GraphQL adapters to the P4
// execution adapter. Inputs remain frozen P4 values, so P4 rebinds and
// revalidates them against the active schema before any work.
type Request struct {
	operation   mutationir.Operation
	model       golem.ModelID
	target      *golem.FrozenMutationTarget
	where       *golem.FrozenPredicate
	input       *golem.FrozenMutationInput
	create      *golem.FrozenMutationInput
	update      *golem.FrozenMutationInput
	selection   []readir.Selection
	existing    *golem.ExistingVersion
	expectation *golem.ConcurrencyExpectation
}

func (request Request) Operation() mutationir.Operation { return request.operation }
func (request Request) ModelID() golem.ModelID          { return request.model }
func (request Request) Target() (golem.FrozenMutationTarget, bool) {
	if request.target == nil {
		return golem.FrozenMutationTarget{}, false
	}
	return *request.target, true
}
func (request Request) Where() (golem.FrozenPredicate, bool) {
	if request.where == nil {
		return golem.FrozenPredicate{}, false
	}
	return *request.where, true
}
func (request Request) Input() (golem.FrozenMutationInput, bool) {
	if request.input == nil {
		return golem.FrozenMutationInput{}, false
	}
	return *request.input, true
}
func (request Request) CreateInput() (golem.FrozenMutationInput, bool) {
	if request.create == nil {
		return golem.FrozenMutationInput{}, false
	}
	return *request.create, true
}
func (request Request) UpdateInput() (golem.FrozenMutationInput, bool) {
	if request.update == nil {
		return golem.FrozenMutationInput{}, false
	}
	return *request.update, true
}
func (request Request) Selection() []readir.Selection {
	return append([]readir.Selection(nil), request.selection...)
}

func (request Request) ExistingVersion() (golem.ExistingVersion, bool) {
	if request.existing == nil {
		return golem.ExistingVersion{}, false
	}
	return *request.existing, true
}

func (request Request) ConcurrencyExpectation() (golem.ConcurrencyExpectation, bool) {
	if request.expectation == nil {
		return golem.ConcurrencyExpectation{}, false
	}
	return *request.expectation, true
}

// FreezeExecution joins the already-frozen P4 input with the P3 result
// projection compiled from the same GraphQL root. Batch roots have no row
// projection and return BatchPayload instead.
func (request Request) FreezeExecution(projection *golem.FrozenReadRequest) (golem.RuntimeMutationRequest, error) {
	input := golem.RuntimeMutationRequestInput{Operation: runtimeOperation(request.operation), Model: request.model, Projection: projection}
	if request.target != nil {
		value := *request.target
		input.Target = &value
	}
	if request.where != nil {
		value := *request.where
		input.Where = &value
	}
	if request.input != nil {
		value := *request.input
		input.Input = &value
	}
	if request.create != nil {
		value := *request.create
		input.Create = &value
	}
	if request.update != nil {
		value := *request.update
		input.Update = &value
	}
	if request.existing != nil {
		value := *request.existing
		return golem.RuntimeFreezeVersionedMutationRequest(golem.RuntimeVersionedMutationRequestInput{Request: input, ExistingVersion: &value})
	}
	if request.expectation != nil {
		value := *request.expectation
		return golem.RuntimeFreezeVersionedMutationRequest(golem.RuntimeVersionedMutationRequestInput{Request: input, ConcurrencyExpectation: &value})
	}
	return golem.RuntimeFreezeMutationRequest(input)
}

func runtimeOperation(operation mutationir.Operation) golem.RuntimeMutationOperation {
	return map[mutationir.Operation]golem.RuntimeMutationOperation{
		mutationir.Create: golem.RuntimeMutationCreate, mutationir.Update: golem.RuntimeMutationUpdate,
		mutationir.Upsert: golem.RuntimeMutationUpsert, mutationir.Delete: golem.RuntimeMutationDelete,
		mutationir.UpdateMany: golem.RuntimeMutationUpdateMany, mutationir.DeleteMany: golem.RuntimeMutationDeleteMany,
	}[operation]
}

type lowerer struct {
	limits Limits
	nodes  int
}

func Lower(root RootInput, limits Limits) (Request, error) {
	limits = normalizeLimits(limits)
	if root.Model == (golem.ModelID{}) || root.Operation < Create || root.Operation > DeleteMany {
		return Request{}, fmt.Errorf("P5_MUTATION_ROOT: model and supported operation are required")
	}
	lower := lowerer{limits: limits}
	request := Request{operation: rootIR(root.Operation), model: root.Model, selection: append([]readir.Selection(nil), root.Selection...)}
	if root.ExistingVersion != nil {
		value := *root.ExistingVersion
		request.existing = &value
	}
	if root.ConcurrencyExpectation != nil {
		value := *root.ConcurrencyExpectation
		request.expectation = &value
	}
	if root.Target != nil {
		value := *root.Target
		request.target = &value
	}
	if root.Where != nil {
		value := *root.Where
		request.where = &value
	}
	if err := validateRootShape(root); err != nil {
		return Request{}, err
	}
	if root.Target != nil && root.Target.Selector().ModelID() != root.Model {
		return Request{}, fmt.Errorf("P5_MUTATION_TARGET: root selector belongs to another model")
	}
	if root.Where != nil && root.Where.View().RootModelID() != root.Model {
		return Request{}, fmt.Errorf("P5_MUTATION_WHERE: root predicate belongs to another model")
	}
	var err error
	switch root.Operation {
	case Create:
		request.input, err = lower.input(root.Data, CreateInput, root.Model, 1)
	case Update:
		request.input, err = lower.input(root.Data, UpdateInput, root.Model, 1)
	case Upsert:
		request.create, err = lower.input(root.Create, CreateInput, root.Model, 1)
		if err == nil {
			request.update, err = lower.input(root.Update, UpdateInput, root.Model, 1)
		}
	case Delete:
	case UpdateMany:
		request.input, err = lower.input(root.Data, UpdateManyInput, root.Model, 1)
	case DeleteMany:
	}
	if err != nil {
		return Request{}, err
	}
	return request, nil
}

func normalizeLimits(limits Limits) Limits {
	if limits.MaxInputDepth <= 0 {
		limits.MaxInputDepth = 16
	}
	if limits.MaxInputNodes <= 0 {
		limits.MaxInputNodes = 8192
	}
	if limits.MaxListItems <= 0 {
		limits.MaxListItems = 1000
	}
	return limits
}

func validateRootShape(root RootInput) error {
	hasTarget, hasWhere := root.Target != nil, root.Where != nil
	hasData, hasCreate, hasUpdate := root.Data != nil, root.Create != nil, root.Update != nil
	hasSelection := len(root.Selection) != 0
	hasExisting, hasExpectation := root.ExistingVersion != nil, root.ConcurrencyExpectation != nil
	valid := false
	switch root.Operation {
	case Create:
		valid = !hasTarget && !hasWhere && hasData && !hasCreate && !hasUpdate && !hasExisting && !hasExpectation
	case Update:
		valid = hasTarget && !hasWhere && hasData && !hasCreate && !hasUpdate && !hasExpectation
	case Upsert:
		valid = hasTarget && !hasWhere && !hasData && hasCreate && hasUpdate && !hasExisting
	case Delete:
		valid = hasTarget && !hasWhere && !hasData && !hasCreate && !hasUpdate && !hasExpectation
	case UpdateMany:
		valid = !hasTarget && hasWhere && hasData && !hasCreate && !hasUpdate && !hasSelection && !hasExisting && !hasExpectation
	case DeleteMany:
		valid = !hasTarget && hasWhere && !hasData && !hasCreate && !hasUpdate && !hasSelection && !hasExisting && !hasExpectation
	}
	if !valid {
		return fmt.Errorf("P5_MUTATION_ROOT: operation arguments do not match the generated root shape")
	}
	return nil
}

func rootIR(operation RootOperation) mutationir.Operation {
	return map[RootOperation]mutationir.Operation{
		Create: mutationir.Create, Update: mutationir.Update, Upsert: mutationir.Upsert,
		Delete: mutationir.Delete, UpdateMany: mutationir.UpdateMany, DeleteMany: mutationir.DeleteMany,
	}[operation]
}

func (lower *lowerer) input(input *Input, expected InputKind, expectedModel golem.ModelID, depth int) (*golem.FrozenMutationInput, error) {
	if input == nil || input.Model == (golem.ModelID{}) || input.Model != expectedModel || input.Kind != expected {
		return nil, fmt.Errorf("P5_MUTATION_INPUT: required %d input is absent or has the wrong kind", expected)
	}
	if depth > lower.limits.MaxInputDepth {
		return nil, fmt.Errorf("P5_MUTATION_LIMIT: input depth exceeds %d", lower.limits.MaxInputDepth)
	}
	if err := lower.consume(1 + len(input.Scalars) + len(input.Relations)); err != nil {
		return nil, err
	}
	fields := make([]golem.RuntimeMutationFieldValue, len(input.Scalars))
	seenFields := make(map[golem.FieldID]struct{}, len(input.Scalars))
	for index, scalar := range input.Scalars {
		if scalar.Field == (golem.FieldID{}) {
			return nil, fmt.Errorf("P5_MUTATION_FIELD: scalar %d has no stable identity", index)
		}
		if _, duplicate := seenFields[scalar.Field]; duplicate {
			return nil, fmt.Errorf("P5_MUTATION_FIELD: field operation is duplicated")
		}
		if !scalarAllowed(expected, scalar.Operation) {
			return nil, fmt.Errorf("P5_MUTATION_FIELD: operation is invalid for input kind")
		}
		hasValue := scalar.Operation != golem.MutationFieldNull
		fields[index] = golem.RuntimeMutationFieldValue{Field: scalar.Field, Operation: scalar.Operation, Value: scalar.Value, HasValue: hasValue}
		seenFields[scalar.Field] = struct{}{}
	}
	if expected == UpdateManyInput && len(input.Relations) != 0 {
		return nil, fmt.Errorf("P5_MUTATION_INPUT: update-many cannot contain relations")
	}
	var relations []golem.RuntimeNestedMutationValue
	for index, relation := range input.Relations {
		values, err := lower.relation(input.Model, expected, relation, depth)
		if err != nil {
			return nil, fmt.Errorf("P5_MUTATION_RELATION: relation %d: %w", index, err)
		}
		relations = append(relations, values...)
	}
	kind := map[InputKind]golem.RuntimeMutationInputKind{CreateInput: golem.RuntimeMutationCreateInput, UpdateInput: golem.RuntimeMutationUpdateInput, UpdateManyInput: golem.RuntimeMutationUpdateManyInput}[expected]
	frozen, err := golem.RuntimeMutationInputFromValues(kind, input.Model, fields, relations)
	if err != nil {
		return nil, fmt.Errorf("P5_MUTATION_BOUNDARY: %w", err)
	}
	return &frozen, nil
}

func scalarAllowed(kind InputKind, operation golem.MutationFieldOperation) bool {
	if kind == CreateInput {
		return operation == golem.MutationFieldCreate || operation == golem.MutationFieldNull
	}
	return operation >= golem.MutationFieldSet && operation <= golem.MutationFieldDecrement
}

func (lower *lowerer) relation(parent golem.ModelID, kind InputKind, relation Relation, depth int) ([]golem.RuntimeNestedMutationValue, error) {
	if relation.Field == (golem.FieldID{}) || relation.Relation == (golem.RelationID{}) || relation.TargetModel == (golem.ModelID{}) {
		return nil, fmt.Errorf("stable relation identities are incomplete")
	}
	if relation.Action < golem.MutationRelationCreate || relation.Action > golem.MutationRelationDeleteMany {
		return nil, fmt.Errorf("nested action is invalid")
	}
	if kind == CreateInput && relation.Action > golem.MutationRelationConnectOrCreate {
		return nil, fmt.Errorf("update-only nested action appears in create input")
	}
	if len(relation.Entries) > lower.limits.MaxListItems {
		return nil, fmt.Errorf("list items exceed %d", lower.limits.MaxListItems)
	}
	if err := lower.consume(len(relation.Entries)); err != nil {
		return nil, err
	}
	base := golem.RuntimeNestedMutationValue{Parent: parent, Field: relation.Field, Relation: relation.Relation, Target: relation.TargetModel, Action: relation.Action}
	if relation.Action == golem.MutationRelationSet && len(relation.Entries) == 0 {
		base.Branches = []golem.RuntimeNestedMutationBranchValue{{Branch: golem.MutationRelationMainBranch, Model: relation.TargetModel, Action: golem.MutationRelationSet}}
		return []golem.RuntimeNestedMutationValue{base}, nil
	}
	if len(relation.Entries) == 0 {
		return nil, fmt.Errorf("nested action has no explicit entries")
	}
	perEntry := relation.Action == golem.MutationRelationConnectOrCreate || relation.Action == golem.MutationRelationUpdate ||
		relation.Action == golem.MutationRelationUpdateMany || relation.Action == golem.MutationRelationUpsert ||
		relation.Action == golem.MutationRelationDelete || relation.Action == golem.MutationRelationDeleteMany
	var result []golem.RuntimeNestedMutationValue
	for index, entry := range relation.Entries {
		branches, err := lower.entry(relation, entry, depth+1)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", index, err)
		}
		if perEntry {
			value := base
			value.Branches = branches
			result = append(result, value)
		} else {
			base.Branches = append(base.Branches, branches...)
		}
	}
	if !perEntry {
		result = append(result, base)
	}
	return result, nil
}

func (lower *lowerer) entry(relation Relation, entry RelationEntry, depth int) ([]golem.RuntimeNestedMutationBranchValue, error) {
	branch := func(action golem.MutationRelationAction) golem.RuntimeNestedMutationBranchValue {
		return golem.RuntimeNestedMutationBranchValue{Branch: golem.MutationRelationMainBranch, Model: relation.TargetModel, Action: action, Target: entry.Target, Predicate: entry.Predicate}
	}
	lowerInput := func(input *Input, kind InputKind) (*golem.FrozenMutationInput, error) {
		return lower.input(input, kind, relation.TargetModel, depth)
	}
	switch relation.Action {
	case golem.MutationRelationCreate, golem.MutationRelationCreateMany:
		if entry.Target != nil || entry.Predicate != nil || entry.Update != nil {
			return nil, fmt.Errorf("create entry contains a target, predicate, or update")
		}
		input, err := lowerInput(entry.Create, CreateInput)
		if err != nil {
			return nil, err
		}
		value := branch(golem.MutationRelationCreate)
		value.Input = input
		return []golem.RuntimeNestedMutationBranchValue{value}, nil
	case golem.MutationRelationConnect, golem.MutationRelationDisconnect, golem.MutationRelationSet:
		if entry.Predicate != nil || entry.Create != nil || entry.Update != nil {
			return nil, fmt.Errorf("membership entry contains an input or predicate")
		}
		if relation.Action != golem.MutationRelationDisconnect && entry.Target == nil {
			return nil, fmt.Errorf("membership entry requires a target")
		}
		if entry.Target != nil && entry.Target.Selector().ModelID() != relation.TargetModel {
			return nil, fmt.Errorf("membership target belongs to another model")
		}
		return []golem.RuntimeNestedMutationBranchValue{branch(relation.Action)}, nil
	case golem.MutationRelationConnectOrCreate:
		if entry.Target == nil || entry.Predicate != nil || entry.Update != nil {
			return nil, fmt.Errorf("connect-or-create requires target and create only")
		}
		if entry.Target.Selector().ModelID() != relation.TargetModel {
			return nil, fmt.Errorf("connect-or-create target belongs to another model")
		}
		input, err := lowerInput(entry.Create, CreateInput)
		if err != nil {
			return nil, err
		}
		return []golem.RuntimeNestedMutationBranchValue{
			{Branch: golem.MutationRelationConnectOrCreateConnectBranch, Model: relation.TargetModel, Action: golem.MutationRelationConnect, Target: entry.Target},
			{Branch: golem.MutationRelationConnectOrCreateCreateBranch, Model: relation.TargetModel, Action: golem.MutationRelationCreate, Input: input},
		}, nil
	case golem.MutationRelationUpdate:
		if entry.Predicate != nil || entry.Create != nil {
			return nil, fmt.Errorf("update entry contains create or predicate")
		}
		if entry.Target != nil && entry.Target.Selector().ModelID() != relation.TargetModel {
			return nil, fmt.Errorf("update target belongs to another model")
		}
		input, err := lowerInput(entry.Update, UpdateInput)
		if err != nil {
			return nil, err
		}
		value := branch(golem.MutationRelationUpdate)
		value.Input = input
		return []golem.RuntimeNestedMutationBranchValue{value}, nil
	case golem.MutationRelationUpdateMany:
		if entry.Target != nil || entry.Predicate == nil || entry.Create != nil {
			return nil, fmt.Errorf("update-many requires predicate and update only")
		}
		if entry.Predicate.View().RootModelID() != relation.TargetModel {
			return nil, fmt.Errorf("update-many predicate belongs to another model")
		}
		input, err := lowerInput(entry.Update, UpdateManyInput)
		if err != nil {
			return nil, err
		}
		value := branch(golem.MutationRelationUpdateMany)
		value.Input = input
		return []golem.RuntimeNestedMutationBranchValue{value}, nil
	case golem.MutationRelationUpsert:
		if entry.Predicate != nil {
			return nil, fmt.Errorf("upsert entry cannot contain a predicate")
		}
		if entry.Target != nil && entry.Target.Selector().ModelID() != relation.TargetModel {
			return nil, fmt.Errorf("upsert target belongs to another model")
		}
		create, err := lowerInput(entry.Create, CreateInput)
		if err != nil {
			return nil, err
		}
		update, err := lowerInput(entry.Update, UpdateInput)
		if err != nil {
			return nil, err
		}
		return []golem.RuntimeNestedMutationBranchValue{
			{Branch: golem.MutationRelationUpsertCreateBranch, Model: relation.TargetModel, Action: golem.MutationRelationCreate, Input: create},
			{Branch: golem.MutationRelationUpsertUpdateBranch, Model: relation.TargetModel, Action: golem.MutationRelationUpdate, Target: entry.Target, Input: update},
		}, nil
	case golem.MutationRelationDelete:
		if entry.Predicate != nil || entry.Create != nil || entry.Update != nil {
			return nil, fmt.Errorf("delete entry contains an input or predicate")
		}
		if entry.Target != nil && entry.Target.Selector().ModelID() != relation.TargetModel {
			return nil, fmt.Errorf("delete target belongs to another model")
		}
		return []golem.RuntimeNestedMutationBranchValue{branch(golem.MutationRelationDelete)}, nil
	case golem.MutationRelationDeleteMany:
		if entry.Target != nil || entry.Predicate == nil || entry.Create != nil || entry.Update != nil {
			return nil, fmt.Errorf("delete-many requires predicate only")
		}
		if entry.Predicate.View().RootModelID() != relation.TargetModel {
			return nil, fmt.Errorf("delete-many predicate belongs to another model")
		}
		return []golem.RuntimeNestedMutationBranchValue{branch(golem.MutationRelationDeleteMany)}, nil
	default:
		return nil, fmt.Errorf("unsupported nested action")
	}
}

func (lower *lowerer) consume(nodes int) error {
	lower.nodes += nodes
	if lower.nodes > lower.limits.MaxInputNodes {
		return fmt.Errorf("P5_MUTATION_LIMIT: input nodes exceed %d", lower.limits.MaxInputNodes)
	}
	return nil
}
