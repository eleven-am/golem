package golem

import "fmt"

// RuntimeIdentityHydrationLimit bounds the number of key components in one
// generated hydration request before building its private correlation tree.
const RuntimeIdentityHydrationLimit = 16 * 1024

type frozenIdentityHydration struct {
	predicate FrozenPredicate
	fields    []FieldID
}

// restoreReadHydration carries runtime-owned correlation through a typed hook's
// public option roundtrip. Public filters and window options remain the hook's;
// the ordinary binder must still validate them, including the exact lookup
// window. Correlation never becomes an app-authored Where predicate.
func restoreReadHydration(original, transformed FrozenReadRequest) (FrozenReadRequest, error) {
	if original.model != transformed.model || original.operation != transformed.operation {
		return FrozenReadRequest{}, fmt.Errorf("identity hydration: hook changed request identity")
	}
	if original.hydration != nil {
		transformed.hydration = &frozenIdentityHydration{
			predicate: original.hydration.predicate,
			fields:    append([]FieldID(nil), original.hydration.fields...),
		}
	}
	// Match the full relation occurrence, rather than the field alone: aliased
	// selections of one relation can carry different private identity sets.
	type selectionKey struct {
		kind       readSelectionKind
		field      FieldID
		relation   RelationID
		target     ModelID
		occurrence RuntimeOccurrenceID
	}
	key := func(selection FrozenReadSelection) selectionKey {
		return selectionKey{selection.kind, selection.field, selection.relation, selection.target, selection.occurrence}
	}
	children := make(map[selectionKey]*FrozenReadRequest)
	for _, selection := range original.selection {
		if selection.request != nil {
			children[key(selection)] = selection.request
		}
	}
	if len(children) == 0 {
		return transformed, nil
	}
	transformed.selection = append([]FrozenReadSelection(nil), transformed.selection...)
	for index, selection := range transformed.selection {
		prior := children[key(selection)]
		if prior == nil || selection.request == nil {
			continue
		}
		child, err := restoreReadHydration(*prior, *selection.request)
		if err != nil {
			return FrozenReadRequest{}, err
		}
		transformed.selection[index].request = &child
	}
	return transformed, nil
}

func RuntimeIdentityHydrationRequest(base FrozenReadRequest, identities []RuntimeRowIdentity, filters ...FrozenPredicate) (FrozenReadRequest, error) {
	if base.operation != ReadFindMany || base.model == (ModelID{}) || len(identities) == 0 || len(identities) > RuntimeIdentityHydrationLimit || len(filters) > 1 {
		return FrozenReadRequest{}, fmt.Errorf("identity hydration: findMany request and identities are required")
	}
	if base.hydration != nil || len(base.orders) != 0 || base.skip != nil || len(base.distinct) != 0 || base.cursor != nil || base.selector != nil || base.take == nil || *base.take != len(identities) {
		return FrozenReadRequest{}, fmt.Errorf("identity hydration: an uncorrelated bounded lookup without ordering, skip, distinct, or cursor is required")
	}
	fields := identities[0].Fields()
	if len(fields) == 0 || len(fields) > RuntimeIdentityHydrationLimit/len(identities) {
		return FrozenReadRequest{}, fmt.Errorf("identity hydration: key component limit exceeded")
	}
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
	canonical, err := encodeFrozenPredicate(base.model, selector)
	if err != nil {
		return FrozenReadRequest{}, fmt.Errorf("identity hydration: predicate is invalid: %w", err)
	}
	result := base.clone()
	result.hydration = &frozenIdentityHydration{predicate: FrozenPredicate{rootModel: base.model, root: selector, canonical: canonical}, fields: append([]FieldID(nil), fields...)}
	// App-authored filters remain ordinary caller predicates. Identity
	// correlation must never lend its authority to their field references.
	if len(filters) == 1 {
		if filters[0].rootModel != base.model || filters[0].root == nil {
			return FrozenReadRequest{}, fmt.Errorf("identity hydration: filter does not match the request")
		}
		filter := cloneFrozenCondition(filters[0].root)
		if base.where != nil {
			filter = &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorAnd, operand: frozenOperand{kind: FrozenOperandNone}, children: []*frozenCondition{cloneFrozenCondition(base.where.root), filter}}
		}
		canonical, err := encodeFrozenPredicate(base.model, filter)
		if err != nil {
			return FrozenReadRequest{}, fmt.Errorf("identity hydration: filter is invalid: %w", err)
		}
		where := FrozenPredicate{rootModel: base.model, root: filter, canonical: canonical}
		result.where = &where
	}
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
