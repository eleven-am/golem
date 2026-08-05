// Package imply proves conservative implications between normalized policy
// conditions using structural rules and a bounded exact propositional fallback.
package imply

import (
	"bytes"
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

const maximumDepth = 256
const maximumPropositionalAtoms = 12

// Condition returns true only when the admitted structural or bounded
// propositional rules prove that selecting entails required. A false result
// means "not proved", not necessarily "logically impossible".
func Condition(selecting, required ir.Condition) (bool, error) {
	if selecting.ModelID() == (ir.ModelID{}) || selecting.ModelID() != required.ModelID() {
		return false, fmt.Errorf("policy implication: condition model mismatch")
	}
	left, err := normalize.Condition(selecting)
	if err != nil {
		return false, fmt.Errorf("policy implication: normalize selecting: %w", err)
	}
	right, err := normalize.Condition(required)
	if err != nil {
		return false, fmt.Errorf("policy implication: normalize required: %w", err)
	}
	proved, err := condition(left, right, 0)
	if err != nil || proved {
		return proved, err
	}
	return propositionalImplication(left, right)
}

// Same reports normalized canonical structural equality. It intentionally
// uses the closed canonical encoder rather than reflect.DeepEqual.
func Same(left, right ir.Condition) (bool, error) {
	if left.ModelID() == (ir.ModelID{}) || left.ModelID() != right.ModelID() {
		return false, nil
	}
	left, err := normalize.Condition(left)
	if err != nil {
		return false, fmt.Errorf("policy implication: normalize left: %w", err)
	}
	right, err = normalize.Condition(right)
	if err != nil {
		return false, fmt.Errorf("policy implication: normalize right: %w", err)
	}
	return sameNormalized(left, right)
}

func condition(selecting, required ir.Condition, depth int) (bool, error) {
	if depth > maximumDepth {
		return false, fmt.Errorf("policy implication: maximum proof depth exceeded")
	}
	needed := conjuncts(required)
	if len(needed) == 0 {
		return true, nil
	}
	held := conjuncts(selecting)
	for _, need := range needed {
		proved, err := conjunctImplied(held, need, depth+1)
		if err != nil {
			return false, err
		}
		if !proved {
			return false, nil
		}
	}
	return true, nil
}

func conjunctImplied(held []ir.Condition, need ir.Condition, depth int) (bool, error) {
	for _, candidate := range held {
		same, err := sameNormalized(candidate, need)
		if err != nil {
			return false, err
		}
		if same {
			return true, nil
		}
	}

	if branches, disjunction := orBranches(need); disjunction {
		selection, err := conjunction(need.ModelID(), held)
		if err != nil {
			return false, err
		}
		for _, branch := range branches {
			proved, err := condition(selection, branch, depth+1)
			if err != nil {
				return false, err
			}
			if proved {
				return true, nil
			}
		}
		// Required disjunctions deliberately do not fall through to the held
		// disjunction rule. This is the conservative pinned behavior.
		return false, nil
	}

	for _, candidate := range held {
		branches, disjunction := orBranches(candidate)
		if !disjunction || len(branches) == 0 {
			continue
		}
		all := true
		for _, branch := range branches {
			proved, err := condition(branch, need, depth+1)
			if err != nil {
				return false, err
			}
			if !proved {
				all = false
				break
			}
		}
		if all {
			return true, nil
		}
	}
	return false, nil
}

func conjuncts(condition ir.Condition) []ir.Condition {
	if truth, constant := condition.Constant(); constant && truth {
		return nil
	}
	if logical, children, ok := condition.Logical(); ok && logical == ir.LogicalAnd {
		result := make([]ir.Condition, 0, len(children))
		for _, child := range children {
			result = append(result, conjuncts(child)...)
		}
		return result
	}
	return []ir.Condition{condition}
}

func orBranches(condition ir.Condition) ([]ir.Condition, bool) {
	logical, children, ok := condition.Logical()
	return children, ok && logical == ir.LogicalOr
}

func conjunction(model ir.ModelID, conditions []ir.Condition) (ir.Condition, error) {
	switch len(conditions) {
	case 0:
		return ir.NewConstant(model, true)
	case 1:
		return conditions[0], nil
	default:
		condition, err := ir.NewLogical(model, ir.LogicalAnd, conditions)
		if err != nil {
			return ir.Condition{}, fmt.Errorf("policy implication: rebuild conjunction: %w", err)
		}
		return normalize.Condition(condition)
	}
}

func sameNormalized(left, right ir.Condition) (bool, error) {
	leftBytes, err := ir.CanonicalCondition(left)
	if err != nil {
		return false, fmt.Errorf("policy implication: encode left: %w", err)
	}
	rightBytes, err := ir.CanonicalCondition(right)
	if err != nil {
		return false, fmt.Errorf("policy implication: encode right: %w", err)
	}
	return bytes.Equal(leftBytes, rightBytes), nil
}

// propositionalImplication is a bounded exact fallback over boolean structure.
// Every non-logical condition is an opaque atom identified by canonical bytes;
// this never invents scalar, JSON, or relation semantics. The bound makes an
// unusually large proof fail closed rather than turn classification into an
// exponential request-time workload.
func propositionalImplication(selecting, required ir.Condition) (bool, error) {
	atoms := make(map[string]int)
	if err := collectAtoms(selecting, atoms, 0); err != nil {
		return false, err
	}
	if err := collectAtoms(required, atoms, 0); err != nil {
		return false, err
	}
	if len(atoms) > maximumPropositionalAtoms {
		return false, nil
	}
	assignment := make([]bool, len(atoms))
	satisfiable := false
	for mask := uint64(0); mask < uint64(1)<<len(atoms); mask++ {
		for index := range assignment {
			assignment[index] = mask&(uint64(1)<<index) != 0
		}
		selected, err := evaluateProposition(selecting, atoms, assignment, 0)
		if err != nil {
			return false, err
		}
		if !selected {
			continue
		}
		satisfiable = true
		needed, err := evaluateProposition(required, atoms, assignment, 0)
		if err != nil {
			return false, err
		}
		if !needed {
			return false, nil
		}
	}
	// Deliberately reject vacuous proofs from an impossible selector. This is
	// the same fail-closed guard as the structural empty-OR rule.
	return satisfiable, nil
}

func collectAtoms(condition ir.Condition, atoms map[string]int, depth int) error {
	if depth > maximumDepth {
		return fmt.Errorf("policy implication: maximum proposition depth exceeded")
	}
	if _, constant := condition.Constant(); constant {
		return nil
	}
	if _, children, logical := condition.Logical(); logical {
		for _, child := range children {
			if err := collectAtoms(child, atoms, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	canonical, err := ir.CanonicalCondition(condition)
	if err != nil {
		return fmt.Errorf("policy implication: encode proposition atom: %w", err)
	}
	key := string(canonical)
	if _, exists := atoms[key]; !exists {
		atoms[key] = len(atoms)
	}
	return nil
}

func evaluateProposition(condition ir.Condition, atoms map[string]int, assignment []bool, depth int) (bool, error) {
	if depth > maximumDepth {
		return false, fmt.Errorf("policy implication: maximum proposition depth exceeded")
	}
	if truth, constant := condition.Constant(); constant {
		return truth, nil
	}
	if operator, children, logical := condition.Logical(); logical {
		switch operator {
		case ir.LogicalNot:
			value, err := evaluateProposition(children[0], atoms, assignment, depth+1)
			return !value, err
		case ir.LogicalAnd:
			for _, child := range children {
				value, err := evaluateProposition(child, atoms, assignment, depth+1)
				if err != nil || !value {
					return false, err
				}
			}
			return true, nil
		case ir.LogicalOr:
			for _, child := range children {
				value, err := evaluateProposition(child, atoms, assignment, depth+1)
				if err != nil {
					return false, err
				}
				if value {
					return true, nil
				}
			}
			return false, nil
		default:
			return false, fmt.Errorf("policy implication: unknown logical operator %d", operator)
		}
	}
	canonical, err := ir.CanonicalCondition(condition)
	if err != nil {
		return false, fmt.Errorf("policy implication: encode proposition atom: %w", err)
	}
	index, ok := atoms[string(canonical)]
	if !ok || index >= len(assignment) {
		return false, fmt.Errorf("policy implication: missing proposition atom")
	}
	return assignment[index], nil
}
