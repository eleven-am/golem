// Package resolve compiles an immutable ordered policy into row and field
// conditions. It preserves rule declaration priority and does not evaluate or
// render predicate leaves.
package resolve

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

// RowConstraint returns the rows on which action is granted. Model-wide rules
// and field-scoped grants participate; field-scoped denials do not hide rows.
func RowConstraint(policy ir.Policy, action ir.Action, model ir.ModelID) (ir.Condition, error) {
	condition, _, err := rowConstraint(policy, action, model)
	if err != nil {
		return ir.Condition{}, err
	}
	return normalize.Condition(condition)
}

// FieldCondition returns the rows on which field is granted for action.
// Model-wide rules and field-scoped rules naming field participate.
func FieldCondition(policy ir.Policy, action ir.Action, model ir.ModelID, field ir.FieldID) (ir.Condition, error) {
	condition, _, err := fieldCondition(policy, action, model, field)
	if err != nil {
		return ir.Condition{}, err
	}
	return normalize.Condition(condition)
}

// ActionAllowed derives the action gate solely from the already-resolved row
// constraint. Only the false constant refuses the action.
func ActionAllowed(rowConstraint ir.Condition) (bool, error) {
	normalized, err := normalize.Condition(rowConstraint)
	if err != nil {
		return false, fmt.Errorf("resolve action gate: %w", err)
	}
	truth, constant := normalized.Constant()
	return !constant || truth, nil
}

func rowConstraint(policy ir.Policy, action ir.Action, model ir.ModelID) (ir.Condition, []uint32, error) {
	if err := validateQuestion(policy, action, model); err != nil {
		return ir.Condition{}, nil, err
	}

	chain := chainForRow(policy, action, model)
	branches := make([]ir.Condition, 0, len(chain))
	denials := make([]ir.Condition, 0, len(chain))
	trace := make([]uint32, 0, len(chain))
	openGrant := false

	for _, rule := range chain {
		trace = append(trace, rule.Position())
		condition, conditional := rule.Condition()
		if rule.Effect() == ir.EffectDeny {
			if !conditional {
				break
			}
			negated, err := logicalNot(condition)
			if err != nil {
				return ir.Condition{}, nil, err
			}
			denials = append(denials, negated)
			continue
		}

		if !conditional {
			openGrant = true
			break
		}
		branch, err := conjoin(model, appendCondition(condition, denials))
		if err != nil {
			return ir.Condition{}, nil, err
		}
		branches = append(branches, branch)
	}

	if openGrant {
		switch {
		case len(denials) == 0:
			condition, err := constant(model, true)
			return condition, trace, err
		case len(branches) == 0:
			condition, err := conjoin(model, denials)
			return condition, trace, err
		default:
			fallback, err := conjoin(model, denials)
			if err != nil {
				return ir.Condition{}, nil, err
			}
			branches = append(branches, fallback)
		}
	}
	condition, err := disjoin(model, branches)
	return condition, trace, err
}

func fieldCondition(policy ir.Policy, action ir.Action, model ir.ModelID, field ir.FieldID) (ir.Condition, []uint32, error) {
	if err := validateQuestion(policy, action, model); err != nil {
		return ir.Condition{}, nil, err
	}
	if field == (ir.FieldID{}) {
		return ir.Condition{}, nil, fmt.Errorf("resolve field condition: zero field ID")
	}

	chain := chainForField(policy, action, model, field)
	branches := make([]ir.Condition, 0, len(chain))
	unmatchedEarlier := make([]ir.Condition, 0, len(chain))
	trace := make([]uint32, 0, len(chain))

	for _, rule := range chain {
		trace = append(trace, rule.Position())
		condition, conditional := rule.Condition()
		if !conditional {
			if rule.Effect() == ir.EffectGrant {
				branch, err := conjoin(model, unmatchedEarlier)
				if err != nil {
					return ir.Condition{}, nil, err
				}
				branches = append(branches, branch)
			}
			result, err := disjoin(model, branches)
			return result, trace, err
		}

		if rule.Effect() == ir.EffectGrant {
			branch, err := conjoin(model, appendCondition(condition, unmatchedEarlier))
			if err != nil {
				return ir.Condition{}, nil, err
			}
			branches = append(branches, branch)
		}
		negated, err := logicalNot(condition)
		if err != nil {
			return ir.Condition{}, nil, err
		}
		unmatchedEarlier = append(unmatchedEarlier, negated)
	}

	result, err := disjoin(model, branches)
	return result, trace, err
}

func validateQuestion(policy ir.Policy, action ir.Action, model ir.ModelID) error {
	if model == (ir.ModelID{}) {
		return fmt.Errorf("resolve policy: zero model ID")
	}
	if policy.ModelID() != model {
		return fmt.Errorf("resolve policy: model mismatch")
	}
	switch action {
	case ir.ActionRead, ir.ActionCreate, ir.ActionUpdate, ir.ActionDelete:
		return nil
	default:
		return fmt.Errorf("resolve policy: invalid action %d", action)
	}
}

func chainForRow(policy ir.Policy, action ir.Action, model ir.ModelID) []ir.Rule {
	rules := policy.Rules()
	chain := make([]ir.Rule, 0, len(rules))
	for index := len(rules) - 1; index >= 0; index-- {
		rule := rules[index]
		if rule.Action() != action || rule.ModelID() != model {
			continue
		}
		_, modelWide := rule.Fields()
		if modelWide || rule.Effect() == ir.EffectGrant {
			chain = append(chain, rule)
		}
	}
	return chain
}

func chainForField(policy ir.Policy, action ir.Action, model ir.ModelID, field ir.FieldID) []ir.Rule {
	rules := policy.Rules()
	chain := make([]ir.Rule, 0, len(rules))
	for index := len(rules) - 1; index >= 0; index-- {
		rule := rules[index]
		if rule.Action() != action || rule.ModelID() != model {
			continue
		}
		fields, modelWide := rule.Fields()
		if modelWide || containsField(fields, field) {
			chain = append(chain, rule)
		}
	}
	return chain
}

func containsField(fields []ir.FieldID, field ir.FieldID) bool {
	// Keep the authored first-seen order intact. A map is unnecessary here and
	// would discard the ordering that later diagnostics and dependency planning
	// must preserve.
	for _, candidate := range fields {
		if candidate == field {
			return true
		}
	}
	return false
}

func appendCondition(first ir.Condition, rest []ir.Condition) []ir.Condition {
	conditions := make([]ir.Condition, 1, len(rest)+1)
	conditions[0] = first
	return append(conditions, rest...)
}

func constant(model ir.ModelID, truth bool) (ir.Condition, error) {
	condition, err := ir.NewConstant(model, truth)
	if err != nil {
		return ir.Condition{}, fmt.Errorf("resolve constant: %w", err)
	}
	return condition, nil
}

func logicalNot(condition ir.Condition) (ir.Condition, error) {
	result, err := ir.NewLogical(condition.ModelID(), ir.LogicalNot, []ir.Condition{condition})
	if err != nil {
		return ir.Condition{}, fmt.Errorf("resolve negation: %w", err)
	}
	return result, nil
}

func conjoin(model ir.ModelID, conditions []ir.Condition) (ir.Condition, error) {
	switch len(conditions) {
	case 0:
		return constant(model, true)
	case 1:
		return conditions[0], nil
	default:
		condition, err := ir.NewLogical(model, ir.LogicalAnd, conditions)
		if err != nil {
			return ir.Condition{}, fmt.Errorf("resolve conjunction: %w", err)
		}
		return condition, nil
	}
}

func disjoin(model ir.ModelID, conditions []ir.Condition) (ir.Condition, error) {
	switch len(conditions) {
	case 0:
		return constant(model, false)
	case 1:
		return conditions[0], nil
	default:
		condition, err := ir.NewLogical(model, ir.LogicalOr, conditions)
		if err != nil {
			return ir.Condition{}, fmt.Errorf("resolve disjunction: %w", err)
		}
		return condition, nil
	}
}
