package graphql

import (
	"fmt"
	"sort"

	"github.com/vektah/gqlparser/v2/ast"
)

type ListBounds struct {
	entries map[string]int
}

func NewListBounds(entries map[string]int) ListBounds {
	copied := make(map[string]int, len(entries))
	for key, bound := range entries {
		copied[key] = bound
	}
	return ListBounds{entries: copied}
}

func (bounds ListBounds) of(parent, field string) (int, bool) {
	bound, ok := bounds.entries[parent+"."+field]
	return bound, ok
}

func undefaultedLists(schema *ast.Schema) map[string]bool {
	required := map[string]bool{}
	for _, definition := range schema.Types {
		if definition.Kind != ast.Object && definition.Kind != ast.Interface {
			continue
		}
		for _, field := range definition.Fields {
			if field.Type == nil || field.Type.Elem == nil || isIntrospectionName(definition.Name) || isIntrospectionName(field.Name) {
				continue
			}
			element := schema.Types[field.Type.Name()]
			if element == nil || !element.IsCompositeType() {
				continue
			}
			if take := field.Arguments.ForName("take"); take != nil && take.DefaultValue != nil {
				continue
			}
			required[definition.Name+"."+field.Name] = true
		}
	}
	return required
}

func isIntrospectionName(name string) bool {
	return len(name) >= 2 && name[:2] == "__"
}

func validateListBounds(schema *ast.Schema, bounds ListBounds) error {
	required := undefaultedLists(schema)
	keys := make([]string, 0, len(required))
	for key := range required {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, declared := bounds.entries[key]; !declared {
			return fmt.Errorf("GraphQL executor declares no list bound for %s, a list without a take default", key)
		}
	}
	declared := make([]string, 0, len(bounds.entries))
	for key := range bounds.entries {
		declared = append(declared, key)
	}
	sort.Strings(declared)
	for _, key := range declared {
		if !required[key] {
			return fmt.Errorf("GraphQL executor declares a list bound for %s, which is not a list without a take default", key)
		}
		if bounds.entries[key] < 1 {
			return fmt.Errorf("GraphQL executor list bound for %s must be at least 1", key)
		}
	}
	return nil
}
