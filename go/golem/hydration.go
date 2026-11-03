package golem

import "fmt"

func RuntimeIdentityHydrationRequest(base FrozenReadRequest, identities []RuntimeRowIdentity, filters ...FrozenPredicate) (FrozenReadRequest, error) {
	if base.operation != ReadFindMany || base.model == (ModelID{}) || len(identities) == 0 || len(filters) > 1 {
		return FrozenReadRequest{}, fmt.Errorf("identity hydration: findMany request and identities are required")
	}
	fields := identities[0].Fields()
	children := make([]*frozenCondition, 0, len(identities))
	for index, identity := range identities {
		if !identity.Present() || !equalFieldIDs(fields, identity.Fields()) {
			return FrozenReadRequest{}, fmt.Errorf("identity hydration: identity %d does not match the request", index)
		}
		condition, err := identityCondition(identity)
		if err != nil {
			return FrozenReadRequest{}, fmt.Errorf("identity hydration: identity %d: %w", index, err)
		}
		children = append(children, condition)
	}
	selector := children[0]
	if len(children) > 1 {
		selector = &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorOr, operand: frozenOperand{kind: FrozenOperandNone}, children: children}
	}
	if base.where != nil {
		selector = &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorAnd, operand: frozenOperand{kind: FrozenOperandNone}, children: []*frozenCondition{cloneFrozenCondition(base.where.root), selector}}
	}
	if len(filters) == 1 {
		if filters[0].rootModel != base.model || filters[0].root == nil {
			return FrozenReadRequest{}, fmt.Errorf("identity hydration: filter does not match the request")
		}
		selector = &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorAnd, operand: frozenOperand{kind: FrozenOperandNone}, children: []*frozenCondition{cloneFrozenCondition(filters[0].root), selector}}
	}
	canonical, err := encodeFrozenPredicate(base.model, selector)
	if err != nil {
		return FrozenReadRequest{}, fmt.Errorf("identity hydration: predicate is invalid: %w", err)
	}
	result := base.clone()
	where := FrozenPredicate{rootModel: base.model, root: selector, canonical: canonical}
	result.where = &where
	return result, nil
}

func identityCondition(identity RuntimeRowIdentity) (*frozenCondition, error) {
	values := identity.Values()
	conditions := make([]*frozenCondition, len(values))
	for index, field := range identity.Fields() {
		value := scalarValueAny(values[index])
		if value.invalid != "" {
			return nil, fmt.Errorf("key field %x: %s", field, value.invalid)
		}
		conditions[index] = &frozenCondition{kind: FrozenConditionScalar, operator: FrozenOperatorEq, mode: FrozenComparisonSensitive, field: field, operand: frozenOperand{kind: FrozenOperandOne, one: value}}
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorAnd, operand: frozenOperand{kind: FrozenOperandNone}, children: conditions}, nil
}

func equalFieldIDs(left, right []FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
