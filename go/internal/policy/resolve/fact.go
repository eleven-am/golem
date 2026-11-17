package resolve

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

// resolveFacts binds references against the definition table owned by this
// policy. Each definition is resolved once per question; named boundaries are
// retained so later planners can distinguish physical facts from readable
// relation predicates. The immutable policy constructor has already proved
// that the complete definition graph is root-scoped and acyclic.
func resolveFacts(policy ir.Policy, condition ir.Condition) (ir.Condition, error) {
	definitions := policy.Facts()
	if len(definitions) == 0 {
		return condition, nil
	}
	resolver := factResolver{
		root:        policy.ModelID(),
		scope:       policy.FactScope(),
		definitions: make(map[ir.FactID]ir.FactDefinition, len(definitions)),
		resolved:    make(map[ir.FactID]ir.Condition, len(definitions)),
	}
	for _, definition := range definitions {
		resolver.definitions[definition.ID()] = definition
	}
	bound, err := resolver.condition(condition)
	if err != nil {
		return ir.Condition{}, err
	}
	if err := ir.CheckFactExpansion(bound); err != nil {
		return ir.Condition{}, err
	}
	return bound, nil
}

type factResolver struct {
	root        ir.ModelID
	scope       ir.FactScope
	definitions map[ir.FactID]ir.FactDefinition
	resolved    map[ir.FactID]ir.Condition
	depth       int
}

func (resolver *factResolver) condition(input ir.Condition) (ir.Condition, error) {
	switch input.Kind() {
	case ir.ConditionConstant, ir.ConditionScalar, ir.ConditionList, ir.ConditionJSON:
		return input, nil
	case ir.ConditionFactReference:
		id, _ := input.FactReference()
		if input.ModelID() != resolver.root {
			return ir.Condition{}, fmt.Errorf("resolve fact: reference belongs to another root")
		}
		if bound, ok := resolver.resolved[id]; ok {
			return bound, nil
		}
		if resolver.depth >= ir.MaxFactDepth {
			return ir.Condition{}, &ir.FactLimitError{Limit: ir.FactDepth, Maximum: ir.MaxFactDepth}
		}
		resolver.depth++
		defer func() { resolver.depth-- }()
		definition, ok := resolver.definitions[id]
		if !ok {
			return ir.Condition{}, fmt.Errorf("resolve fact: definition is absent from the policy root")
		}
		body, err := resolver.condition(definition.Body())
		if err != nil {
			return ir.Condition{}, err
		}
		body, err = normalize.Condition(body)
		if err != nil {
			return ir.Condition{}, fmt.Errorf("resolve fact %q: %w", definition.Name(), err)
		}
		bound, err := ir.NewFact(resolver.root, id, body, resolver.scope)
		if binding, present := definition.Binding(); present {
			bound, err = ir.NewBoundFact(resolver.root, id, body, resolver.scope, binding)
		}
		if err != nil {
			return ir.Condition{}, err
		}
		resolver.resolved[id] = bound
		return bound, nil
	case ir.ConditionLogical:
		logical, children, _ := input.Logical()
		for index, child := range children {
			bound, err := resolver.condition(child)
			if err != nil {
				return ir.Condition{}, err
			}
			children[index] = bound
		}
		return ir.NewLogical(input.ModelID(), logical, children)
	case ir.ConditionRelation:
		field, relation, target, cardinality, child, _ := input.Relation()
		if child == nil {
			return input, nil
		}
		bound, err := resolver.condition(*child)
		if err != nil {
			return ir.Condition{}, err
		}
		operation, _ := input.Operator()
		requirements, _ := input.RelationOwnRequirements()
		return ir.NewRelation(input.ModelID(), field, relation, target, cardinality, operation, &bound, requirements)
	default:
		return ir.Condition{}, fmt.Errorf("resolve fact: unexpected authored condition kind %d", input.Kind())
	}
}
