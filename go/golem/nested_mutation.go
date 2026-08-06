package golem

import "fmt"

// MutationRelationAction is the closed nested relation vocabulary. Its order
// deliberately matches the provider-neutral mutation IR vocabulary, but this
// public request representation does not import an internal package.
type MutationRelationAction uint8

const (
	MutationRelationCreate MutationRelationAction = iota + 1
	MutationRelationCreateMany
	MutationRelationConnect
	MutationRelationConnectOrCreate
	MutationRelationDisconnect
	MutationRelationSet
	MutationRelationUpdate
	MutationRelationUpdateMany
	MutationRelationUpsert
	MutationRelationDelete
	MutationRelationDeleteMany
)

// MutationRelationBranch records the semantically distinct paths within
// connect-or-create and upsert. Keeping branches explicit prevents the binder
// from having to reinterpret an opaque payload.
type MutationRelationBranch uint8

const (
	MutationRelationMainBranch MutationRelationBranch = iota + 1
	MutationRelationUpsertCreateBranch
	MutationRelationUpsertUpdateBranch
	MutationRelationConnectOrCreateCreateBranch
	MutationRelationConnectOrCreateConnectBranch
)

// NestedValue is a generated relation value legal in both create and update
// inputs. Operations which are update-only return NestedUpdateValue instead.
type NestedValue[M any] interface {
	NestedCreateValue[M]
	NestedUpdateValue[M]
}

type nestedMutationBranchNode struct {
	branch    MutationRelationBranch
	model     ModelID
	action    MutationRelationAction
	target    *FrozenMutationTarget
	predicate *FrozenPredicate
	input     *FrozenMutationInput
}

func (branch nestedMutationBranchNode) clone() nestedMutationBranchNode {
	result := branch
	if branch.target != nil {
		value := cloneFrozenMutationTarget(*branch.target)
		result.target = &value
	}
	if branch.predicate != nil {
		value := cloneFrozenPredicate(*branch.predicate)
		result.predicate = &value
	}
	if branch.input != nil {
		value := cloneFrozenMutationInput(*branch.input)
		result.input = &value
	}
	return result
}

type nestedMutationNode struct {
	parent   ModelID
	field    FieldID
	relation RelationID
	target   ModelID
	action   MutationRelationAction
	branches []nestedMutationBranchNode
	err      error
}

func (node nestedMutationNode) clone() nestedMutationNode {
	result := node
	result.branches = make([]nestedMutationBranchNode, len(node.branches))
	for index, branch := range node.branches {
		result.branches[index] = branch.clone()
	}
	return result
}

type nestedValue[M any] struct {
	node nestedMutationNode
	_    func() M
}

func (value nestedValue[M]) mutationNode() mutationValueNode {
	node := value.node.clone()
	return mutationValueNode{model: node.parent, field: node.field, relation: &node}
}

func (value nestedValue[M]) createMutationValue(M) mutationValueNode {
	return value.mutationNode()
}
func (value nestedValue[M]) updateMutationValue(M) mutationValueNode {
	return value.mutationNode()
}
func (value nestedValue[M]) nestedCreateMutationValue(M) mutationValueNode {
	return value.mutationNode()
}
func (value nestedValue[M]) nestedUpdateMutationValue(M) mutationValueNode {
	return value.mutationNode()
}

type nestedUpdateValue[M any] struct {
	node nestedMutationNode
	_    func() M
}

func (value nestedUpdateValue[M]) mutationNode() mutationValueNode {
	node := value.node.clone()
	return mutationValueNode{model: node.parent, field: node.field, relation: &node}
}

func (value nestedUpdateValue[M]) updateMutationValue(M) mutationValueNode {
	return value.mutationNode()
}
func (value nestedUpdateValue[M]) nestedUpdateMutationValue(M) mutationValueNode {
	return value.mutationNode()
}

func nestedNode(parent ModelID, field FieldID, relation RelationID, target ModelID, action MutationRelationAction) nestedMutationNode {
	return nestedMutationNode{parent: parent, field: field, relation: relation, target: target, action: action}
}

func freezeNestedCreateInput[T any](input CreateInput[T]) (*FrozenMutationInput, error) {
	frozen, err := RuntimeFreezeCreateInput(input)
	if err != nil {
		return nil, err
	}
	return &frozen, nil
}

func freezeNestedUpdateInput[T any](input UpdateInput[T]) (*FrozenMutationInput, error) {
	frozen, err := RuntimeFreezeUpdateInput(input)
	if err != nil {
		return nil, err
	}
	return &frozen, nil
}

func freezeNestedUpdateManyInput[T any](input UpdateManyInput[T]) (*FrozenMutationInput, error) {
	frozen, err := RuntimeFreezeUpdateManyInput(input)
	if err != nil {
		return nil, err
	}
	return &frozen, nil
}

func mainInputBranch(model ModelID, action MutationRelationAction, input *FrozenMutationInput) nestedMutationBranchNode {
	return nestedMutationBranchNode{branch: MutationRelationMainBranch, model: model, action: action, input: input}
}

func freezeNestedTarget[T any](target MutationTarget[T]) (*FrozenMutationTarget, error) {
	frozen, err := RuntimeFreezeMutationTarget(target)
	if err != nil {
		return nil, err
	}
	return &frozen, nil
}

func freezeNestedPredicate[T any](model ModelID, predicate Predicate[T]) (*FrozenPredicate, error) {
	frozen, err := predicate.freezeForModel(model)
	if err != nil {
		return nil, err
	}
	return &frozen, nil
}

// GeneratedNestedCreate creates one explicit child create node. Generated
// to-many wrappers may call it once per argument; callers never supply model or
// relation identities directly.
func GeneratedNestedCreate[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID, first CreateInput[T], rest ...CreateInput[T]) NestedValue[P] {
	inputs := append([]CreateInput[T]{first}, rest...)
	node := nestedNode(parent, field, relation, target, MutationRelationCreate)
	node.branches = make([]nestedMutationBranchNode, len(inputs))
	for index, input := range inputs {
		frozen, err := freezeNestedCreateInput(input)
		if err != nil && node.err == nil {
			node.err = err
		}
		node.branches[index] = mainInputBranch(target, MutationRelationCreate, frozen)
	}
	return nestedValue[P]{node: node}
}

func GeneratedNestedCreateMany[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID, first CreateInput[T], rest ...CreateInput[T]) NestedValue[P] {
	inputs := append([]CreateInput[T]{first}, rest...)
	node := nestedNode(parent, field, relation, target, MutationRelationCreateMany)
	node.branches = make([]nestedMutationBranchNode, len(inputs))
	for index, input := range inputs {
		frozen, err := freezeNestedCreateInput(input)
		if err != nil && node.err == nil {
			node.err = err
		}
		node.branches[index] = mainInputBranch(target, MutationRelationCreate, frozen)
	}
	return nestedValue[P]{node: node}
}

func GeneratedNestedConnect[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID, first MutationTarget[T], rest ...MutationTarget[T]) NestedValue[P] {
	targets := append([]MutationTarget[T]{first}, rest...)
	node := nestedNode(parent, field, relation, target, MutationRelationConnect)
	node.branches = make([]nestedMutationBranchNode, len(targets))
	for index, selected := range targets {
		frozen, err := freezeNestedTarget(selected)
		if err != nil && node.err == nil {
			node.err = err
		}
		node.branches[index] = nestedMutationBranchNode{branch: MutationRelationMainBranch, model: target, action: MutationRelationConnect, target: frozen}
	}
	return nestedValue[P]{node: node}
}

func GeneratedNestedConnectOrCreate[P, T any](parent ModelID, field FieldID, relation RelationID, targetModel ModelID, target MutationTarget[T], create CreateInput[T]) NestedValue[P] {
	node := nestedNode(parent, field, relation, targetModel, MutationRelationConnectOrCreate)
	selected, targetErr := freezeNestedTarget(target)
	created, createErr := freezeNestedCreateInput(create)
	if targetErr != nil {
		node.err = targetErr
	} else {
		node.err = createErr
	}
	node.branches = []nestedMutationBranchNode{
		{branch: MutationRelationConnectOrCreateConnectBranch, model: targetModel, action: MutationRelationConnect, target: selected},
		{branch: MutationRelationConnectOrCreateCreateBranch, model: targetModel, action: MutationRelationCreate, input: created},
	}
	return nestedValue[P]{node: node}
}

func GeneratedNestedDisconnect[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID, first MutationTarget[T], rest ...MutationTarget[T]) NestedUpdateValue[P] {
	targets := append([]MutationTarget[T]{first}, rest...)
	node := nestedNode(parent, field, relation, target, MutationRelationDisconnect)
	for _, selected := range targets {
		frozen, err := freezeNestedTarget(selected)
		if err != nil && node.err == nil {
			node.err = err
		}
		node.branches = append(node.branches, nestedMutationBranchNode{branch: MutationRelationMainBranch, model: target, action: MutationRelationDisconnect, target: frozen})
	}
	return nestedUpdateValue[P]{node: node}
}

// GeneratedNestedDisconnectOne is the targetless optional to-one operation.
// To-many wrappers use GeneratedNestedDisconnect, whose required first target
// makes disconnect-all unrepresentable; empty Set remains the clear-all shape.
func GeneratedNestedDisconnectOne[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID) NestedUpdateValue[P] {
	node := nestedNode(parent, field, relation, target, MutationRelationDisconnect)
	node.branches = []nestedMutationBranchNode{{branch: MutationRelationMainBranch, model: target, action: MutationRelationDisconnect}}
	return nestedUpdateValue[P]{node: node}
}

func GeneratedNestedSet[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID, targets ...MutationTarget[T]) NestedUpdateValue[P] {
	node := nestedNode(parent, field, relation, target, MutationRelationSet)
	if len(targets) == 0 {
		node.branches = []nestedMutationBranchNode{{branch: MutationRelationMainBranch, model: target, action: MutationRelationSet}}
	}
	for _, selected := range targets {
		frozen, err := freezeNestedTarget(selected)
		if err != nil && node.err == nil {
			node.err = err
		}
		node.branches = append(node.branches, nestedMutationBranchNode{branch: MutationRelationMainBranch, model: target, action: MutationRelationSet, target: frozen})
	}
	return nestedUpdateValue[P]{node: node}
}

func GeneratedNestedUpdate[P, T any](parent ModelID, field FieldID, relation RelationID, targetModel ModelID, target MutationTarget[T], input UpdateInput[T]) NestedUpdateValue[P] {
	node := nestedNode(parent, field, relation, targetModel, MutationRelationUpdate)
	updated, inputErr := freezeNestedUpdateInput(input)
	node.err = inputErr
	var selected *FrozenMutationTarget
	if target != nil {
		var targetErr error
		selected, targetErr = freezeNestedTarget(target)
		if node.err == nil {
			node.err = targetErr
		}
	}
	node.branches = []nestedMutationBranchNode{{branch: MutationRelationMainBranch, model: targetModel, action: MutationRelationUpdate, target: selected, input: updated}}
	return nestedUpdateValue[P]{node: node}
}

func GeneratedNestedUpdateMany[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID, predicate Predicate[T], input UpdateManyInput[T]) NestedUpdateValue[P] {
	node := nestedNode(parent, field, relation, target, MutationRelationUpdateMany)
	where, predicateErr := freezeNestedPredicate(target, predicate)
	updated, inputErr := freezeNestedUpdateManyInput(input)
	if predicateErr != nil {
		node.err = predicateErr
	} else {
		node.err = inputErr
	}
	node.branches = []nestedMutationBranchNode{{branch: MutationRelationMainBranch, model: target, action: MutationRelationUpdateMany, predicate: where, input: updated}}
	return nestedUpdateValue[P]{node: node}
}

func GeneratedNestedUpsert[P, T any](parent ModelID, field FieldID, relation RelationID, targetModel ModelID, target MutationTarget[T], create CreateInput[T], update UpdateInput[T]) NestedUpdateValue[P] {
	node := nestedNode(parent, field, relation, targetModel, MutationRelationUpsert)
	created, createErr := freezeNestedCreateInput(create)
	updated, updateErr := freezeNestedUpdateInput(update)
	if createErr != nil {
		node.err = createErr
	} else {
		node.err = updateErr
	}
	var selected *FrozenMutationTarget
	if target != nil {
		var targetErr error
		selected, targetErr = freezeNestedTarget(target)
		if node.err == nil {
			node.err = targetErr
		}
	}
	node.branches = []nestedMutationBranchNode{
		{branch: MutationRelationUpsertCreateBranch, model: targetModel, action: MutationRelationCreate, input: created},
		{branch: MutationRelationUpsertUpdateBranch, model: targetModel, action: MutationRelationUpdate, target: selected, input: updated},
	}
	return nestedUpdateValue[P]{node: node}
}

func GeneratedNestedDelete[P, T any](parent ModelID, field FieldID, relation RelationID, targetModel ModelID, target MutationTarget[T]) NestedUpdateValue[P] {
	node := nestedNode(parent, field, relation, targetModel, MutationRelationDelete)
	var selected *FrozenMutationTarget
	if target != nil {
		var err error
		selected, err = freezeNestedTarget(target)
		node.err = err
	}
	node.branches = []nestedMutationBranchNode{{branch: MutationRelationMainBranch, model: targetModel, action: MutationRelationDelete, target: selected}}
	return nestedUpdateValue[P]{node: node}
}

func GeneratedNestedDeleteMany[P, T any](parent ModelID, field FieldID, relation RelationID, target ModelID, predicate Predicate[T]) NestedUpdateValue[P] {
	node := nestedNode(parent, field, relation, target, MutationRelationDeleteMany)
	where, err := freezeNestedPredicate(target, predicate)
	node.err = err
	node.branches = []nestedMutationBranchNode{{branch: MutationRelationMainBranch, model: target, action: MutationRelationDeleteMany, predicate: where}}
	return nestedUpdateValue[P]{node: node}
}

// FrozenNestedMutation is the detached schema-agnostic relation handoff to the
// P4 binder. Every branch is an explicit typed target, predicate, or child
// input; no map or provider payload survives this boundary.
type FrozenNestedMutation struct {
	parent   ModelID
	field    FieldID
	relation RelationID
	target   ModelID
	action   MutationRelationAction
	branches []FrozenNestedMutationBranch
}

func (mutation FrozenNestedMutation) ParentModelID() ModelID         { return mutation.parent }
func (mutation FrozenNestedMutation) FieldID() FieldID               { return mutation.field }
func (mutation FrozenNestedMutation) RelationID() RelationID         { return mutation.relation }
func (mutation FrozenNestedMutation) TargetModelID() ModelID         { return mutation.target }
func (mutation FrozenNestedMutation) Action() MutationRelationAction { return mutation.action }
func (mutation FrozenNestedMutation) Branches() []FrozenNestedMutationBranch {
	result := make([]FrozenNestedMutationBranch, len(mutation.branches))
	for index, branch := range mutation.branches {
		result[index] = branch.clone()
	}
	return result
}

type FrozenNestedMutationBranch struct {
	branch    MutationRelationBranch
	model     ModelID
	action    MutationRelationAction
	target    *FrozenMutationTarget
	predicate *FrozenPredicate
	input     *FrozenMutationInput
}

// RuntimeNestedMutationFromHookRequest rebuilds one selected nested branch
// after generated Before hooks have transformed its model-erased request. It
// is intentionally a runtime/compiler bridge: application code cannot use it
// to bypass generated relation capabilities, and the nested compiler still
// rebinds, reclassifies, authorizes, and validates the returned frozen value.
func RuntimeNestedMutationFromHookRequest(parent ModelID, field FieldID, relation RelationID, target ModelID, request RuntimeMutationHookRequest) (FrozenNestedMutation, error) {
	if parent == (ModelID{}) || field == (FieldID{}) || relation == (RelationID{}) || target == (ModelID{}) || request.ModelID() != target {
		return FrozenNestedMutation{}, fmt.Errorf("runtime nested hook replacement: relation identities are invalid")
	}
	branch := FrozenNestedMutationBranch{branch: MutationRelationMainBranch, model: target}
	var action MutationRelationAction
	switch request.Operation() {
	case HookCreate:
		input, ok := request.Input()
		if !ok {
			return FrozenNestedMutation{}, fmt.Errorf("runtime nested hook replacement: create input is absent")
		}
		action, branch.action, branch.input = MutationRelationCreate, MutationRelationCreate, &input
	case HookUpdate:
		input, inputOK := request.Input()
		selected, targetOK := request.Target()
		if !inputOK || !targetOK {
			return FrozenNestedMutation{}, fmt.Errorf("runtime nested hook replacement: update target or input is absent")
		}
		action, branch.action, branch.input, branch.target = MutationRelationUpdate, MutationRelationUpdate, &input, &selected
	case HookDelete:
		selected, ok := request.Target()
		if !ok {
			return FrozenNestedMutation{}, fmt.Errorf("runtime nested hook replacement: delete target is absent")
		}
		action, branch.action, branch.target = MutationRelationDelete, MutationRelationDelete, &selected
	case HookUpdateMany:
		input, inputOK := request.Input()
		predicate, predicateOK := request.Predicate()
		if !inputOK || !predicateOK {
			return FrozenNestedMutation{}, fmt.Errorf("runtime nested hook replacement: update-many predicate or input is absent")
		}
		action, branch.action, branch.input, branch.predicate = MutationRelationUpdateMany, MutationRelationUpdateMany, &input, &predicate
	case HookDeleteMany:
		predicate, ok := request.Predicate()
		if !ok {
			return FrozenNestedMutation{}, fmt.Errorf("runtime nested hook replacement: delete-many predicate is absent")
		}
		action, branch.action, branch.predicate = MutationRelationDeleteMany, MutationRelationDeleteMany, &predicate
	default:
		return FrozenNestedMutation{}, fmt.Errorf("runtime nested hook replacement: hook operation %q is unsupported", request.Operation())
	}
	return FrozenNestedMutation{parent: parent, field: field, relation: relation, target: target, action: action, branches: []FrozenNestedMutationBranch{branch}}, nil
}

func (branch FrozenNestedMutationBranch) clone() FrozenNestedMutationBranch {
	result := branch
	if branch.target != nil {
		value := cloneFrozenMutationTarget(*branch.target)
		result.target = &value
	}
	if branch.predicate != nil {
		value := cloneFrozenPredicate(*branch.predicate)
		result.predicate = &value
	}
	if branch.input != nil {
		value := cloneFrozenMutationInput(*branch.input)
		result.input = &value
	}
	return result
}

func (branch FrozenNestedMutationBranch) Branch() MutationRelationBranch { return branch.branch }
func (branch FrozenNestedMutationBranch) ModelID() ModelID               { return branch.model }
func (branch FrozenNestedMutationBranch) Action() MutationRelationAction { return branch.action }
func (branch FrozenNestedMutationBranch) Target() (FrozenMutationTarget, bool) {
	if branch.target == nil {
		return FrozenMutationTarget{}, false
	}
	return cloneFrozenMutationTarget(*branch.target), true
}
func (branch FrozenNestedMutationBranch) Predicate() (FrozenPredicate, bool) {
	if branch.predicate == nil {
		return FrozenPredicate{}, false
	}
	return cloneFrozenPredicate(*branch.predicate), true
}
func (branch FrozenNestedMutationBranch) Input() (FrozenMutationInput, bool) {
	if branch.input == nil {
		return FrozenMutationInput{}, false
	}
	return cloneFrozenMutationInput(*branch.input), true
}

func freezeNestedMutation(operation string, parent ModelID, field FieldID, node nestedMutationNode) (FrozenNestedMutation, error) {
	if node.err != nil {
		return FrozenNestedMutation{}, invalidMutationCause(operation, parent, field, "nested mutation contains an invalid target, predicate, or child input", node.err)
	}
	if node.parent != parent || node.field != field || node.relation == (RelationID{}) || node.target == (ModelID{}) || node.action < MutationRelationCreate || node.action > MutationRelationDeleteMany {
		return FrozenNestedMutation{}, invalidMutation(operation, parent, field, "nested mutation has incomplete or foreign generated identity")
	}
	if len(node.branches) == 0 {
		return FrozenNestedMutation{}, invalidMutation(operation, parent, field, "nested mutation has no explicit branches")
	}
	result := FrozenNestedMutation{parent: parent, field: field, relation: node.relation, target: node.target, action: node.action, branches: make([]FrozenNestedMutationBranch, len(node.branches))}
	for index, branch := range node.branches {
		if branch.model != node.target || branch.action < MutationRelationCreate || branch.action > MutationRelationDeleteMany || branch.branch < MutationRelationMainBranch || branch.branch > MutationRelationConnectOrCreateConnectBranch {
			return FrozenNestedMutation{}, invalidMutation(operation, parent, field, fmt.Sprintf("nested mutation branch %d has incomplete or foreign generated identity", index))
		}
		result.branches[index] = FrozenNestedMutationBranch{
			branch: branch.branch, model: branch.model, action: branch.action,
			target: branch.target, predicate: branch.predicate, input: branch.input,
		}.clone()
	}
	return result, nil
}

func cloneFrozenMutationTarget(target FrozenMutationTarget) FrozenMutationTarget {
	result := FrozenMutationTarget{selector: target.Selector(), selectorPredicate: target.SelectorPredicate()}
	if guard, ok := target.Guard(); ok {
		result.guard = &guard
	}
	return result
}

func cloneFrozenMutationInput(input FrozenMutationInput) FrozenMutationInput {
	result := FrozenMutationInput{model: input.model, fields: input.Fields(), relations: make([]FrozenNestedMutation, len(input.relations))}
	for index, relation := range input.relations {
		result.relations[index] = FrozenNestedMutation{
			parent: relation.parent, field: relation.field, relation: relation.relation,
			target: relation.target, action: relation.action, branches: relation.Branches(),
		}
	}
	return result
}

// Relations returns a deep detached copy of every explicit nested relation
// node in source order. The binder canonicalizes execution order after it has
// validated active relation ownership.
func (input FrozenMutationInput) Relations() []FrozenNestedMutation {
	return cloneFrozenMutationInput(input).relations
}
