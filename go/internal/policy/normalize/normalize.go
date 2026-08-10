// Package normalize applies the conservative, provider-neutral condition
// rewrites admitted by the P2 policy contract.
package normalize

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// Condition returns the deterministic normal form of input. It never mutates
// input, and all returned nodes are rebuilt through the immutable IR
// constructors so their derived requirements match their retained children.
func Condition(input ir.Condition) (ir.Condition, error) {
	if err := input.Validate(); err != nil {
		return ir.Condition{}, fmt.Errorf("normalize condition: %w", err)
	}
	return condition(input)
}

// Policy returns input with each present rule condition in deterministic normal
// form. Rule order, positions, action/effect, scope, and field declaration order
// are semantic and are therefore preserved exactly. The returned policy owns a
// rebuilt copy of every rule and condition; input is never mutated.
//
// Binding and normalization remain separate operations by design. Callers bind
// a frozen policy first, then pass the successfully bound policy here.
func Policy(input ir.Policy) (ir.Policy, error) {
	rules := input.Rules()
	validated, err := ir.NewPolicy(input.ModelID(), rules)
	if err != nil {
		return ir.Policy{}, fmt.Errorf("normalize policy: %w", err)
	}

	model := validated.ModelID()
	rules = validated.Rules()
	normalized := make([]ir.Rule, len(rules))
	for index, rule := range rules {
		condition, present := rule.Condition()
		var conditionPointer *ir.Condition
		if present {
			condition, err = Condition(condition)
			if err != nil {
				return ir.Policy{}, fmt.Errorf("normalize policy: rule %d: %w", index, err)
			}
			conditionPointer = &condition
		}

		fields, modelWide := rule.Fields()
		if modelWide {
			normalized[index], err = ir.NewModelRule(
				rule.Action(), rule.Effect(), model, conditionPointer, rule.Position(),
			)
		} else {
			normalized[index], err = ir.NewFieldRule(
				rule.Action(), rule.Effect(), model, conditionPointer,
				fields[0], fields[1:], rule.Position(),
			)
		}
		if err != nil {
			return ir.Policy{}, fmt.Errorf("normalize policy: rebuild rule %d: %w", index, err)
		}
	}

	result, err := ir.NewPolicy(model, normalized)
	if err != nil {
		return ir.Policy{}, fmt.Errorf("normalize policy: rebuild: %w", err)
	}
	return result, nil
}

func condition(input ir.Condition) (ir.Condition, error) {
	switch input.Kind() {
	case ir.ConditionConstant:
		truth, _ := input.Constant()
		return ir.NewConstant(input.ModelID(), truth)
	case ir.ConditionLogical:
		return normalizeLogical(input)
	case ir.ConditionScalar:
		return normalizeScalar(input)
	case ir.ConditionList:
		return normalizeList(input)
	case ir.ConditionJSON:
		return normalizeJSON(input)
	case ir.ConditionRelation:
		return normalizeRelation(input)
	default:
		return ir.Condition{}, fmt.Errorf("normalize condition: unknown condition kind %d", input.Kind())
	}
}

func normalizeLogical(input ir.Condition) (ir.Condition, error) {
	operator, children, _ := input.Logical()
	if operator == ir.LogicalNot {
		child, err := condition(children[0])
		if err != nil {
			return ir.Condition{}, err
		}
		if truth, ok := child.Constant(); ok {
			return ir.NewConstant(input.ModelID(), !truth)
		}
		if nestedOperator, nestedChildren, ok := child.Logical(); ok && nestedOperator == ir.LogicalNot {
			return nestedChildren[0], nil
		}
		return ir.NewLogical(input.ModelID(), ir.LogicalNot, []ir.Condition{child})
	}

	normalized := make([]ir.Condition, 0, len(children))
	for _, child := range children {
		child, err := condition(child)
		if err != nil {
			return ir.Condition{}, err
		}
		if nestedOperator, nestedChildren, ok := child.Logical(); ok && nestedOperator == operator {
			normalized = append(normalized, nestedChildren...)
		} else {
			normalized = append(normalized, child)
		}
	}

	retained := normalized[:0]
	for _, child := range normalized {
		if truth, ok := child.Constant(); ok {
			if operator == ir.LogicalAnd && !truth || operator == ir.LogicalOr && truth {
				return ir.NewConstant(input.ModelID(), truth)
			}
			// The other constant is the operator's identity.
			continue
		}
		retained = append(retained, child)
	}

	unique, err := canonicalUnique(retained)
	if err != nil {
		return ir.Condition{}, err
	}
	switch len(unique) {
	case 0:
		return ir.NewConstant(input.ModelID(), operator == ir.LogicalAnd)
	case 1:
		return unique[0].condition, nil
	default:
		children = make([]ir.Condition, len(unique))
		for index := range unique {
			children[index] = unique[index].condition
		}
		return ir.NewLogical(input.ModelID(), operator, children)
	}
}

type canonicalChild struct {
	condition ir.Condition
	bytes     []byte
}

func canonicalUnique(children []ir.Condition) ([]canonicalChild, error) {
	encoded := make([]canonicalChild, 0, len(children))
	for _, child := range children {
		canonical, err := ir.CanonicalCondition(child)
		if err != nil {
			return nil, fmt.Errorf("normalize condition: encode child: %w", err)
		}
		encoded = append(encoded, canonicalChild{condition: child, bytes: canonical})
	}
	sort.Slice(encoded, func(i, j int) bool { return bytes.Compare(encoded[i].bytes, encoded[j].bytes) < 0 })
	unique := encoded[:0]
	for _, child := range encoded {
		if len(unique) == 0 || !bytes.Equal(unique[len(unique)-1].bytes, child.bytes) {
			unique = append(unique, child)
		}
	}
	return unique, nil
}

func normalizeScalar(input ir.Condition) (ir.Condition, error) {
	field, _ := input.Field()
	typ, _ := input.FieldType()
	operator, _ := input.Operator()
	mode, _ := input.Mode()
	operand, _ := input.Operand()
	return ir.NewScalar(input.ModelID(), field, typ, operator, mode, operand, input.Requirements())
}

func normalizeList(input ir.Condition) (ir.Condition, error) {
	field, _ := input.Field()
	typ, _ := input.FieldType()
	operator, _ := input.Operator()
	operand, _ := input.Operand()
	return ir.NewList(input.ModelID(), field, typ, operator, operand, input.Requirements())
}

func normalizeJSON(input ir.Condition) (ir.Condition, error) {
	field, _ := input.Field()
	typ, _ := input.FieldType()
	operator, _ := input.Operator()
	mode, _ := input.Mode()
	path, _ := input.Path()
	operand, _ := input.Operand()
	return ir.NewJSON(input.ModelID(), field, typ, operator, mode, path, operand, input.Requirements())
}

func normalizeRelation(input ir.Condition) (ir.Condition, error) {
	field, relationID, target, cardinality, child, _ := input.Relation()
	operator, _ := input.Operator()
	ownRequirements, _ := input.RelationOwnRequirements()
	if child == nil {
		return ir.NewRelation(input.ModelID(), field, relationID, target, cardinality, operator, nil, ownRequirements)
	}
	normalized, err := condition(*child)
	if err != nil {
		return ir.Condition{}, err
	}
	return ir.NewRelation(input.ModelID(), field, relationID, target, cardinality, operator, &normalized, ownRequirements)
}
