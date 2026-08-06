package golem

import "fmt"

// RuntimeComputedRelationDependency maps one masked relation occurrence into
// the ordinary field cell exposed to a generated Row[M]. The occurrence was
// selected solely for the computed resolver and was never a response slot.
type RuntimeComputedRelationDependency struct {
	Field      FieldID
	Occurrence RuntimeOccurrenceID
}

// RuntimeComputedDependencyRow constructs the least-authority parent row for a
// computed resolver. Only named scalar dependencies and named relation
// occurrences are copied. Null/masked state is preserved; private P3 hydration,
// unrelated selected fields, counts, and response occurrences are excluded.
func RuntimeComputedDependencyRow(source RuntimeModelRow, scalars []FieldID, relations []RuntimeComputedRelationDependency) (RuntimeModelRow, error) {
	if source.model == (ModelID{}) {
		return RuntimeModelRow{}, fmt.Errorf("computed dependency row: source model is absent")
	}
	result := RuntimeModelRow{model: source.model, cells: make(map[FieldID]readCell, len(scalars)+len(relations)), counts: map[relationCountKey]readCell{}, occurrences: map[runtimeOccurrenceKey]readCell{}}
	for _, field := range scalars {
		if field == (FieldID{}) {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: scalar field is zero")
		}
		cell, selected := source.cells[field]
		if !selected || cell.state == ReadUnselected {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: scalar field %x is unselected", field)
		}
		if _, duplicate := result.cells[field]; duplicate {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: field %x is duplicated", field)
		}
		result.cells[field] = cloneReadCell(cell)
	}
	for _, relation := range relations {
		if relation.Field == (FieldID{}) || relation.Occurrence == 0 {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: relation identity is incomplete")
		}
		cell, selected := source.occurrences[runtimeOccurrenceKey{field: relation.Field, occurrence: uint32(relation.Occurrence)}]
		if !selected || cell.state == ReadUnselected {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: relation field %x is unselected", relation.Field)
		}
		if _, duplicate := result.cells[relation.Field]; duplicate {
			return RuntimeModelRow{}, fmt.Errorf("computed dependency row: field %x is duplicated", relation.Field)
		}
		result.cells[relation.Field] = cloneReadCell(cell)
	}
	return result, nil
}
