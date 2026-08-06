package plan

import (
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

// policyExpander closes every relation hop over the target model's read
// constraint. An authored condition is never allowed to observe target rows
// which the same caller could not read directly.
//
// active is deliberately path-local. A relation-free policy can be re-entered
// (a common self-relation case), while a relation-bearing re-entry is rejected
// rather than being partially expanded and accidentally widening a cycle.
type policyExpander struct {
	policies PolicySet
	maximum  int
	active   map[policyir.ModelID]bool
}

func newPolicyExpander(policies PolicySet, maximum int) *policyExpander {
	return &policyExpander{policies: policies, maximum: maximum, active: make(map[policyir.ModelID]bool)}
}

func (expander *policyExpander) model(model policyir.ModelID, depth int) (policyir.Condition, error) {
	if expander == nil || expander.policies == nil || model == (policyir.ModelID{}) {
		return policyir.Condition{}, fail(CodePolicy, model, policyir.FieldID{}, "relation target policy is unavailable", nil)
	}
	policy, ok := expander.policies.Policy(model)
	if !ok {
		return policyir.Condition{}, fail(CodePolicy, model, policyir.FieldID{}, "relation target policy is absent", nil)
	}
	row, err := resolve.RowConstraint(policy, policyir.ActionRead, model)
	if err != nil {
		return policyir.Condition{}, fail(CodePolicy, model, policyir.FieldID{}, "relation target policy could not be resolved", err)
	}
	if expander.active[model] {
		if conditionHasRelation(row) {
			return policyir.Condition{}, fail(CodePolicy, model, policyir.FieldID{}, "cyclic relation policy cannot be expanded safely", nil)
		}
		result, normalizeErr := normalize.Condition(row)
		if normalizeErr != nil {
			return policyir.Condition{}, fail(CodePolicy, model, policyir.FieldID{}, "relation-free cyclic policy could not be normalized", normalizeErr)
		}
		return result, nil
	}
	expander.active[model] = true
	defer delete(expander.active, model)
	return expander.condition(row, depth)
}

func (expander *policyExpander) condition(condition policyir.Condition, depth int) (policyir.Condition, error) {
	switch condition.Kind() {
	case policyir.ConditionConstant, policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		return condition, nil
	case policyir.ConditionLogical:
		op, children, ok := condition.Logical()
		if !ok {
			return policyir.Condition{}, fail(CodePolicy, condition.ModelID(), policyir.FieldID{}, "logical policy predicate is malformed", nil)
		}
		rewritten := make([]policyir.Condition, len(children))
		for index, child := range children {
			value, err := expander.condition(child, depth)
			if err != nil {
				return policyir.Condition{}, err
			}
			rewritten[index] = value
		}
		result, err := policyir.NewLogical(condition.ModelID(), op, rewritten)
		if err != nil {
			return policyir.Condition{}, fail(CodePolicy, condition.ModelID(), policyir.FieldID{}, "logical policy predicate could not be rebuilt", err)
		}
		return normalize.Condition(result)
	case policyir.ConditionRelation:
		field, relation, target, cardinality, child, ok := condition.Relation()
		if !ok {
			return policyir.Condition{}, fail(CodePolicy, condition.ModelID(), policyir.FieldID{}, "relation policy predicate is malformed", nil)
		}
		if depth >= expander.maximum {
			return policyir.Condition{}, fail(CodeLimit, condition.ModelID(), field, "relation policy depth limit exceeded", nil)
		}
		operator, ok := condition.Operator()
		if !ok {
			return policyir.Condition{}, fail(CodePolicy, condition.ModelID(), field, "relation policy operator is absent", nil)
		}
		own, ok := condition.RelationOwnRequirements()
		if !ok {
			return policyir.Condition{}, fail(CodePolicy, condition.ModelID(), field, "relation policy requirements are absent", nil)
		}
		targetRow, err := expander.model(target, depth+1)
		if err != nil {
			return policyir.Condition{}, err
		}
		var rewrittenChild policyir.Condition
		if child != nil {
			rewrittenChild, err = expander.condition(*child, depth+1)
			if err != nil {
				return policyir.Condition{}, err
			}
		}
		build := func(op policyir.OperatorID, predicate policyir.Condition) (policyir.Condition, error) {
			value, buildErr := policyir.NewRelation(condition.ModelID(), field, relation, target, cardinality, op, &predicate, own)
			if buildErr != nil {
				return policyir.Condition{}, fail(CodePolicy, condition.ModelID(), field, "relation policy predicate could not be rebuilt", buildErr)
			}
			return normalize.Condition(value)
		}
		switch operator {
		case policyir.OperatorRelationIsNull:
			exists, buildErr := build(policyir.OperatorRelationIs, targetRow)
			if buildErr != nil {
				return policyir.Condition{}, buildErr
			}
			return logicalNot(condition.ModelID(), exists)
		case policyir.OperatorRelationIsNotNull:
			return build(policyir.OperatorRelationIs, targetRow)
		case policyir.OperatorRelationEvery:
			if child == nil {
				return policyir.Condition{}, fail(CodeInput, condition.ModelID(), field, "every relation predicate has no child", nil)
			}
			negated, negateErr := logicalNot(target, rewrittenChild)
			if negateErr != nil {
				return policyir.Condition{}, negateErr
			}
			counterexample, mergeErr := and(target, targetRow, negated)
			if mergeErr != nil {
				return policyir.Condition{}, mergeErr
			}
			return build(policyir.OperatorRelationNone, counterexample)
		default:
			predicate := targetRow
			if child != nil {
				predicate, err = and(target, targetRow, rewrittenChild)
				if err != nil {
					return policyir.Condition{}, err
				}
			}
			return build(operator, predicate)
		}
	default:
		return policyir.Condition{}, fail(CodePolicy, condition.ModelID(), policyir.FieldID{}, "unknown policy condition kind", nil)
	}
}

func conditionHasRelation(condition policyir.Condition) bool {
	switch condition.Kind() {
	case policyir.ConditionRelation:
		return true
	case policyir.ConditionLogical:
		_, children, ok := condition.Logical()
		if !ok {
			return true
		}
		for _, child := range children {
			if conditionHasRelation(child) {
				return true
			}
		}
	}
	return false
}
