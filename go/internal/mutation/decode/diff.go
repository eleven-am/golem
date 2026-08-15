package decode

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// ChangedFields returns the exact persisted before/after diff, including
// database-owned changes. Results are sorted by stable field identity.
func ChangedFields(before, after Row) ([]policyir.FieldID, error) {
	if before.model == (policyir.ModelID{}) || before.model != after.model || len(before.cells) != len(after.cells) {
		return nil, fmt.Errorf("P4_MUTATION_DIFF: before and after must be complete images of one model")
	}
	result := make([]policyir.FieldID, 0)
	for index := range before.cells {
		if before.cells[index].field != after.cells[index].field {
			return nil, fmt.Errorf("P4_MUTATION_DIFF: row image field sets disagree")
		}
		if !EqualCell(before.cells[index], after.cells[index]) {
			result = append(result, before.cells[index].field)
		}
	}
	return result, nil
}

// AuthoredFields intersects the exact persisted diff with the explicit input
// inventory. Database-generated, database/read-only, contract read-only, and
// runtime-updated fields can never be reported as caller-authored changes.
func AuthoredFields(registry *schema.Registry, before, after Row, authored []policyir.FieldID) ([]policyir.FieldID, error) {
	changed, err := ChangedFields(before, after)
	if err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, fmt.Errorf("P4_MUTATION_DIFF: active schema registry is required")
	}
	changedSet := make(map[policyir.FieldID]struct{}, len(changed))
	for _, field := range changed {
		changedSet[field] = struct{}{}
	}
	seen := make(map[policyir.FieldID]struct{}, len(authored))
	result := make([]policyir.FieldID, 0, len(authored))
	for _, fieldID := range authored {
		if _, duplicate := seen[fieldID]; duplicate {
			return nil, fmt.Errorf("P4_MUTATION_DIFF: authored field %x appears more than once", fieldID)
		}
		seen[fieldID] = struct{}{}
		field, ok := registry.Field(golem.ModelID(before.model), golem.FieldID(fieldID))
		if !ok || field.Kind() == compilerir.FieldRelation {
			return nil, fmt.Errorf("P4_MUTATION_DIFF: authored field %x is absent, foreign, or relational", fieldID)
		}
		if databaseOwned(field) {
			continue
		}
		if _, didChange := changedSet[fieldID]; didChange {
			result = append(result, fieldID)
		}
	}
	sort.Slice(result, func(i, j int) bool { return string(result[i][:]) < string(result[j][:]) })
	return result, nil
}

func databaseOwned(field schema.Field) bool {
	if field.DatabaseReadOnly() || field.Updated() {
		return true
	}
	if _, generated := field.Generation(); generated {
		return true
	}
	for _, mode := range field.Modes() {
		if mode == compilerir.ModeReadOnly {
			return true
		}
	}
	return false
}
