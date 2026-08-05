package phase0

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Normalize removes logical identities, flattens nested combinators, removes
// duplicate branches, and sorts commutative branches into a stable order.
func Normalize[M any](predicate Predicate[M]) Predicate[M] {
	return Predicate[M]{node: normalizeNode(predicate.node)}
}

// Canonical returns the deterministic representation used by tests, caches,
// policy explanations, and eventually SQL-plan identity.
func Canonical[M any](predicate Predicate[M]) (string, error) {
	encoded, err := json.Marshal(normalizeNode(predicate.node))
	if err != nil {
		return "", fmt.Errorf("encode canonical predicate: %w", err)
	}
	return string(encoded), nil
}

func normalizeNode(current node) node {
	children := make([]node, len(current.Children))
	for index, child := range current.Children {
		children[index] = normalizeNode(child)
	}
	current.Children = children

	switch current.Operator {
	case OpNot:
		if len(current.Children) != 1 {
			return current
		}
		child := current.Children[0]
		switch child.Operator {
		case OpAll:
			return node{Operator: OpNone}
		case OpNone:
			return node{Operator: OpAll}
		case OpNot:
			if len(child.Children) == 1 {
				return child.Children[0]
			}
		}
		return current
	case OpAnd, OpOr:
		return normalizeCommutative(current.Operator, current.Children)
	default:
		return current
	}
}

func normalizeCommutative(operator Operator, input []node) node {
	identity, annihilator := OpAll, OpNone
	if operator == OpOr {
		identity, annihilator = OpNone, OpAll
	}

	flattened := make([]node, 0, len(input))
	for _, child := range input {
		if child.Operator == annihilator {
			return node{Operator: annihilator}
		}
		if child.Operator == identity {
			continue
		}
		if child.Operator == operator {
			flattened = append(flattened, child.Children...)
			continue
		}
		flattened = append(flattened, child)
	}

	unique := make(map[string]node, len(flattened))
	keys := make([]string, 0, len(flattened))
	for _, child := range flattened {
		encoded, _ := json.Marshal(child)
		key := string(encoded)
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = child
		keys = append(keys, key)
	}
	sort.Strings(keys)

	if len(keys) == 0 {
		return node{Operator: identity}
	}
	if len(keys) == 1 {
		return unique[keys[0]]
	}
	children := make([]node, len(keys))
	for index, key := range keys {
		children[index] = unique[key]
	}
	return node{Operator: operator, Children: children}
}
