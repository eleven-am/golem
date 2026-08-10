package golem

import "fmt"

// The RuntimeTyped* helpers are the narrow generated-glue bridge for custom
// GraphQL argument structs. They preserve the ordinary typed Caller API while
// accepting only values already frozen and identity-checked by P3/P4 binders.

type RuntimeCustomMutationInput struct {
	kind  RuntimeMutationInputKind
	value FrozenMutationInput
}

func RuntimeCustomMutationInputValue(kind RuntimeMutationInputKind, value FrozenMutationInput) (RuntimeCustomMutationInput, error) {
	if value.ModelID() == (ModelID{}) || kind < RuntimeMutationCreateInput || kind > RuntimeMutationUpdateManyInput {
		return RuntimeCustomMutationInput{}, fmt.Errorf("runtime custom mutation input kind or model is invalid")
	}
	if kind == RuntimeMutationUpdateManyInput && len(value.Relations()) != 0 {
		return RuntimeCustomMutationInput{}, fmt.Errorf("runtime custom update-many input contains relations")
	}
	for _, field := range value.Fields() {
		if !runtimeInputOperationAllowed(kind, field.Operation()) {
			return RuntimeCustomMutationInput{}, fmt.Errorf("runtime custom mutation input operation does not match its kind")
		}
	}
	return RuntimeCustomMutationInput{kind: kind, value: cloneFrozenMutationInput(value)}, nil
}

func (value RuntimeCustomMutationInput) Kind() RuntimeMutationInputKind { return value.kind }
func (value RuntimeCustomMutationInput) Frozen() FrozenMutationInput {
	return cloneFrozenMutationInput(value.value)
}

func RuntimeTypedPredicate[M any](model ModelID, value FrozenPredicate) (Predicate[M], error) {
	if model == (ModelID{}) || value.View().RootModelID() != model {
		return Predicate[M]{}, fmt.Errorf("runtime custom predicate belongs to another model")
	}
	return thawPredicate[M](cloneFrozenPredicate(value)), nil
}

func RuntimeTypedUniqueSelector[M any](model ModelID, value FrozenMutationTarget) (UniqueSelectorValue[M], error) {
	selector := value.Selector()
	_, guarded := value.Guard()
	if model == (ModelID{}) || selector.ModelID() != model || guarded {
		return UniqueSelectorValue[M]{}, fmt.Errorf("runtime custom selector belongs to another model or carries a guard")
	}
	components, err := selectorComponentsFromPredicate(value.SelectorPredicate(), selector.Fields())
	if err != nil {
		return UniqueSelectorValue[M]{}, err
	}
	return GeneratedUniqueSelectorValue[M](model, selector.KeyID(), components...), nil
}

func RuntimeTypedMutationTarget[M any](model ModelID, value FrozenMutationTarget) (MutationTarget[M], error) {
	if model == (ModelID{}) || value.Selector().ModelID() != model {
		return nil, fmt.Errorf("runtime custom mutation target belongs to another model")
	}
	return runtimeFrozenMutationTarget[M]{target: cloneFrozenMutationTarget(value)}, nil
}

func RuntimeTypedCreateInput[M any](model ModelID, value RuntimeCustomMutationInput) (CreateInput[M], error) {
	frozen := value.Frozen()
	if model == (ModelID{}) || value.Kind() != RuntimeMutationCreateInput || frozen.ModelID() != model {
		return CreateInput[M]{}, fmt.Errorf("runtime custom create input belongs to another model")
	}
	return thawCreateInput[M](frozen), nil
}

func RuntimeTypedUpdateInput[M any](model ModelID, value RuntimeCustomMutationInput) (UpdateInput[M], error) {
	frozen := value.Frozen()
	if model == (ModelID{}) || value.Kind() != RuntimeMutationUpdateInput || frozen.ModelID() != model {
		return UpdateInput[M]{}, fmt.Errorf("runtime custom update input belongs to another model")
	}
	return thawUpdateInput[M](frozen), nil
}

func RuntimeTypedUpdateManyInput[M any](model ModelID, value RuntimeCustomMutationInput) (UpdateManyInput[M], error) {
	frozen := value.Frozen()
	if model == (ModelID{}) || value.Kind() != RuntimeMutationUpdateManyInput || frozen.ModelID() != model {
		return UpdateManyInput[M]{}, fmt.Errorf("runtime custom update-many input belongs to another model")
	}
	return thawUpdateManyInput[M](frozen), nil
}
