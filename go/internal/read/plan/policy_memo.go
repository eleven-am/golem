package plan

import (
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
)

type policyMemo interface {
	Memoized(policyruntime.MemoKey, func(policyruntime.Source) (policyir.Condition, error)) (policyir.Condition, bool, error)
}

func Memoize(policies PolicySet, key policyruntime.MemoKey, compute func(PolicySet) (policyir.Condition, error), fallback func() (policyir.Condition, error)) (policyir.Condition, error) {
	if memo, ok := policies.(policyMemo); ok {
		condition, served, err := memo.Memoized(key, func(source policyruntime.Source) (policyir.Condition, error) {
			return compute(source)
		})
		if served {
			return condition, err
		}
	}
	return fallback()
}

func ReachDepth(maxDepth int) int {
	if maxDepth <= 0 {
		return DefaultLimits().MaxRelationDepth
	}
	return maxDepth
}

func RowConstraint(policies PolicySet, policy policyir.Policy, action policyir.Action, model policyir.ModelID) (policyir.Condition, error) {
	key := policyruntime.MemoKey{Kind: policyruntime.MemoRowConstraint, Model: model, Action: action}
	return Memoize(policies, key, func(source PolicySet) (policyir.Condition, error) {
		resolved, ok := source.Policy(model)
		if !ok {
			return policyir.Condition{}, fail(CodePolicy, model, policyir.FieldID{}, "model policy is absent", nil)
		}
		return resolve.RowConstraint(resolved, action, model)
	}, func() (policyir.Condition, error) {
		return resolve.RowConstraint(policy, action, model)
	})
}

func FieldCondition(policies PolicySet, policy policyir.Policy, action policyir.Action, model policyir.ModelID, field policyir.FieldID) (policyir.Condition, error) {
	key := policyruntime.MemoKey{Kind: policyruntime.MemoFieldCondition, Model: model, Action: action, Field: field}
	return Memoize(policies, key, func(source PolicySet) (policyir.Condition, error) {
		resolved, ok := source.Policy(model)
		if !ok {
			return policyir.Condition{}, fail(CodePolicy, model, field, "model policy is absent", nil)
		}
		return resolve.FieldCondition(resolved, action, model, field)
	}, func() (policyir.Condition, error) {
		return resolve.FieldCondition(policy, action, model, field)
	})
}

func (expander *policyExpander) entry(model policyir.ModelID, depth int) (policyir.Condition, error) {
	if expander == nil || len(expander.active) != 0 {
		return expander.model(model, depth)
	}
	key := policyruntime.MemoKey{Kind: policyruntime.MemoReadExpansion, Model: model, Start: depth, Depth: expander.maximum}
	return Memoize(expander.policies, key, func(source PolicySet) (policyir.Condition, error) {
		return newPolicyExpander(source, expander.maximum).model(model, depth)
	}, func() (policyir.Condition, error) {
		return expander.model(model, depth)
	})
}
