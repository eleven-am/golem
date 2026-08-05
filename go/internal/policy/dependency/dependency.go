// Package dependency derives the deterministic hydration plan required to
// evaluate policy conditions without guessing about under-selected records.
package dependency

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// Kind identifies whether one dependency entry is a persisted value on the
// current model or a relation whose target subtree must be hydrated.
type Kind uint8

const (
	Scalar   Kind = 1
	Relation Kind = 2
)

// Entry is one immutable field dependency. Relation entries retain the target
// model even when Children is empty: relation presence and empty quantifiers
// still require the relation itself to be loaded.
type Entry struct {
	field    ir.FieldID
	kind     Kind
	target   ir.ModelID
	children Tree
}

// Tree is an ordered dependency tree rooted at exactly one model. Entry order
// is first-seen condition order and is preserved while trees are merged.
type Tree struct {
	model   ir.ModelID
	entries []Entry
}

// Plan contains both views required by later read planning. Requires is flat
// and stops at a relation hop; Dependencies recursively describes hydration.
type Plan struct {
	model        ir.ModelID
	requires     []ir.FieldID
	dependencies Tree
}

func (entry Entry) FieldID() ir.FieldID { return entry.field }
func (entry Entry) Kind() Kind          { return entry.kind }
func (entry Entry) TargetModel() (ir.ModelID, bool) {
	return entry.target, entry.kind == Relation
}
func (entry Entry) Children() Tree { return entry.children.clone() }

func (tree Tree) ModelID() ir.ModelID { return tree.model }
func (tree Tree) Entries() []Entry {
	result := make([]Entry, len(tree.entries))
	for index := range tree.entries {
		result[index] = tree.entries[index].clone()
	}
	return result
}

func (plan Plan) ModelID() ir.ModelID { return plan.model }
func (plan Plan) Requires() []ir.FieldID {
	return append([]ir.FieldID(nil), plan.requires...)
}
func (plan Plan) Dependencies() Tree { return plan.dependencies.clone() }

// Empty returns a valid dependency tree for an unconditional classification.
// Keeping the root model explicit prevents later planners from confusing an
// empty plan with a plan for a different model.
func Empty(model ir.ModelID) (Tree, error) {
	if model == (ir.ModelID{}) {
		return Tree{}, fmt.Errorf("policy dependency: zero root model")
	}
	return Tree{model: model}, nil
}

// Collect merges one or more conditions rooted at the same model. Inputs are
// traversed in caller order and are never mutated.
func Collect(conditions ...ir.Condition) (Plan, error) {
	if len(conditions) == 0 {
		return Plan{}, fmt.Errorf("policy dependency: at least one condition is required")
	}
	model := conditions[0].ModelID()
	if model == (ir.ModelID{}) {
		return Plan{}, fmt.Errorf("policy dependency: zero root model")
	}
	collector := planCollector{
		model: model,
		tree:  Tree{model: model},
	}
	for index, condition := range conditions {
		if err := condition.Validate(); err != nil {
			return Plan{}, fmt.Errorf("policy dependency: condition %d: %w", index, err)
		}
		if condition.ModelID() != model {
			return Plan{}, fmt.Errorf("policy dependency: condition %d root model mismatch", index)
		}
		if err := collector.walk(condition, true); err != nil {
			return Plan{}, fmt.Errorf("policy dependency: condition %d: %w", index, err)
		}
	}
	return Plan{
		model:        model,
		requires:     append([]ir.FieldID(nil), collector.requires...),
		dependencies: collector.tree.clone(),
	}, nil
}

type planCollector struct {
	model       ir.ModelID
	requires    []ir.FieldID
	requireSeen map[ir.FieldID]struct{}
	tree        Tree
}

func (collector *planCollector) walk(condition ir.Condition, local bool) error {
	switch condition.Kind() {
	case ir.ConditionConstant:
		return nil
	case ir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := collector.walk(child, local); err != nil {
				return err
			}
		}
		return nil
	case ir.ConditionScalar, ir.ConditionList, ir.ConditionJSON:
		field, _ := condition.Field()
		if local {
			collector.addRequired(field)
		}
		return collector.tree.merge(Entry{field: field, kind: Scalar})
	case ir.ConditionRelation:
		field, _, target, _, child, _ := condition.Relation()
		if local {
			collector.addRequired(field)
		}
		children := Tree{model: target}
		if child != nil {
			nested := planCollector{model: target, tree: Tree{model: target}}
			if err := nested.walk(*child, false); err != nil {
				return err
			}
			children = nested.tree
		}
		return collector.tree.merge(Entry{field: field, kind: Relation, target: target, children: children})
	default:
		return fmt.Errorf("unknown condition kind %d", condition.Kind())
	}
}

func (collector *planCollector) addRequired(field ir.FieldID) {
	if collector.requireSeen == nil {
		collector.requireSeen = make(map[ir.FieldID]struct{})
	}
	if _, exists := collector.requireSeen[field]; exists {
		return
	}
	collector.requireSeen[field] = struct{}{}
	collector.requires = append(collector.requires, field)
}

func (tree *Tree) merge(incoming Entry) error {
	if incoming.field == (ir.FieldID{}) || incoming.kind != Scalar && incoming.kind != Relation {
		return fmt.Errorf("invalid dependency entry")
	}
	for index := range tree.entries {
		current := &tree.entries[index]
		if current.field != incoming.field {
			continue
		}
		if current.kind != incoming.kind {
			return fmt.Errorf("field %x is both scalar and relation", incoming.field)
		}
		if current.kind == Scalar {
			return nil
		}
		if current.target != incoming.target || current.children.model != incoming.children.model {
			return fmt.Errorf("relation field %x target mismatch", incoming.field)
		}
		for _, child := range incoming.children.entries {
			if err := current.children.merge(child); err != nil {
				return err
			}
		}
		return nil
	}
	tree.entries = append(tree.entries, incoming.clone())
	return nil
}

func (entry Entry) clone() Entry {
	entry.children = entry.children.clone()
	return entry
}

func (tree Tree) clone() Tree {
	copy := Tree{model: tree.model, entries: make([]Entry, len(tree.entries))}
	for index := range tree.entries {
		copy.entries[index] = tree.entries[index].clone()
	}
	return copy
}
