package golem

import "fmt"

// RuntimeMutationInputKind identifies the three public P4 input envelopes.
// It exists for generated compiler/runtime adapters which cannot name an
// application's generic model type. Ordinary application code should keep
// using GeneratedCreateInput, GeneratedUpdateInput, and
// GeneratedUpdateManyInput.
type RuntimeMutationInputKind uint8

const (
	RuntimeMutationCreateInput RuntimeMutationInputKind = iota + 1
	RuntimeMutationUpdateInput
	RuntimeMutationUpdateManyInput
)

// RuntimeNestedMutationBranchValue is the model-erased construction shape for
// one already-coerced nested branch. The P4 nested binder remains authoritative
// for schema ownership, exposure, cardinality, requiredness, target binding,
// predicate binding, and operation legality.
type RuntimeNestedMutationBranchValue struct {
	Branch    MutationRelationBranch
	Model     ModelID
	Action    MutationRelationAction
	Target    *FrozenMutationTarget
	Predicate *FrozenPredicate
	Input     *FrozenMutationInput
}

// RuntimeNestedMutationValue is the model-erased construction shape for one
// generated relation envelope. It contains stable identities only; no public
// GraphQL name or provider payload crosses this boundary.
type RuntimeNestedMutationValue struct {
	Parent   ModelID
	Field    FieldID
	Relation RelationID
	Target   ModelID
	Action   MutationRelationAction
	Branches []RuntimeNestedMutationBranchValue
}

// RuntimeMutationInputFromValues is the narrow generated-adapter bridge into
// P4's existing frozen mutation ABI. It validates structural invariants and
// makes detached copies. The ordinary P4 binder still performs every
// schema-dependent validation before planning or database work.
func RuntimeMutationInputFromValues(kind RuntimeMutationInputKind, model ModelID, fields []RuntimeMutationFieldValue, relations []RuntimeNestedMutationValue) (FrozenMutationInput, error) {
	if model == (ModelID{}) || kind < RuntimeMutationCreateInput || kind > RuntimeMutationUpdateManyInput {
		return FrozenMutationInput{}, invalidMutation("runtime input", model, FieldID{}, "input kind or model identity is invalid")
	}
	if kind == RuntimeMutationUpdateManyInput && len(relations) != 0 {
		return FrozenMutationInput{}, invalidMutation("runtime updateMany", model, FieldID{}, "update-many input cannot contain relations")
	}
	result := FrozenMutationInput{model: model, fields: make([]FrozenMutationField, len(fields)), relations: make([]FrozenNestedMutation, len(relations))}
	seenFields := make(map[FieldID]struct{}, len(fields))
	for index, field := range fields {
		if field.Field == (FieldID{}) {
			return FrozenMutationInput{}, invalidMutation("runtime input", model, field.Field, fmt.Sprintf("field %d has no identity", index))
		}
		if _, duplicate := seenFields[field.Field]; duplicate {
			return FrozenMutationInput{}, invalidMutation("runtime input", model, field.Field, "field operation is duplicated")
		}
		if !runtimeInputOperationAllowed(kind, field.Operation) {
			return FrozenMutationInput{}, invalidMutation("runtime input", model, field.Field, "field operation is invalid for the input kind")
		}
		needsValue := field.Operation != MutationFieldNull
		if field.HasValue != needsValue {
			return FrozenMutationInput{}, invalidMutation("runtime input", model, field.Field, "field operand shape is invalid")
		}
		seenFields[field.Field] = struct{}{}
		result.fields[index] = FrozenMutationField{field: field.Field, operation: field.Operation, value: cloneMutationValue(field.Value), hasValue: field.HasValue}
	}
	for index, relation := range relations {
		frozen, err := runtimeFreezeNestedMutation(model, relation)
		if err != nil {
			return FrozenMutationInput{}, invalidMutationCause("runtime input", model, relation.Field, fmt.Sprintf("relation %d is invalid", index), err)
		}
		result.relations[index] = frozen
	}
	return result, nil
}

func runtimeInputOperationAllowed(kind RuntimeMutationInputKind, operation MutationFieldOperation) bool {
	switch kind {
	case RuntimeMutationCreateInput:
		return operation == MutationFieldCreate || operation == MutationFieldNull
	case RuntimeMutationUpdateInput, RuntimeMutationUpdateManyInput:
		return operation >= MutationFieldSet && operation <= MutationFieldDecrement
	default:
		return false
	}
}

func runtimeFreezeNestedMutation(parent ModelID, value RuntimeNestedMutationValue) (FrozenNestedMutation, error) {
	if value.Parent != parent || value.Field == (FieldID{}) || value.Relation == (RelationID{}) || value.Target == (ModelID{}) || value.Action < MutationRelationCreate || value.Action > MutationRelationDeleteMany {
		return FrozenNestedMutation{}, fmt.Errorf("nested relation identities or action are invalid")
	}
	if len(value.Branches) == 0 {
		return FrozenNestedMutation{}, fmt.Errorf("nested relation has no branches")
	}
	result := FrozenNestedMutation{parent: parent, field: value.Field, relation: value.Relation, target: value.Target, action: value.Action, branches: make([]FrozenNestedMutationBranch, len(value.Branches))}
	for index, branch := range value.Branches {
		if branch.Model != value.Target || branch.Action < MutationRelationCreate || branch.Action > MutationRelationDeleteMany || branch.Branch < MutationRelationMainBranch || branch.Branch > MutationRelationConnectOrCreateConnectBranch {
			return FrozenNestedMutation{}, fmt.Errorf("nested branch %d has invalid identity, action, or branch kind", index)
		}
		result.branches[index] = FrozenNestedMutationBranch{
			branch: branch.Branch, model: branch.Model, action: branch.Action,
			target: branch.Target, predicate: branch.Predicate, input: branch.Input,
		}.clone()
	}
	return result, nil
}
