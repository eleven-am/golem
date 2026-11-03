package golem

import "reflect"

// RuntimeModelRowFromTyped erases only the generated Go model witness while
// retaining the complete authorized/masked occurrence-aware P3 row.
func RuntimeModelRowFromTyped[M any](row Row[M]) RuntimeModelRow {
	return cloneRuntimeModelRow(RuntimeModelRow{model: row.model, cells: row.cells, counts: row.counts, occurrences: row.occurrences, identity: row.identity})
}

type erasableRow interface{ erasedRuntimeRow() RuntimeModelRow }

func (row Row[M]) erasedRuntimeRow() RuntimeModelRow { return RuntimeModelRowFromTyped(row) }

var erasableRowType = reflect.TypeOf((*erasableRow)(nil)).Elem()

func RuntimeEraseRows(value any) (any, bool) {
	if value == nil {
		return nil, false
	}
	if row, ok := value.(erasableRow); ok {
		return row.erasedRuntimeRow(), true
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice || !holdsErasableRows(reflected.Type().Elem()) {
		return nil, false
	}
	items := make([]any, reflected.Len())
	for index := range items {
		items[index], _ = RuntimeEraseRows(reflected.Index(index).Interface())
	}
	return items, true
}

func holdsErasableRows(typ reflect.Type) bool {
	if typ.Implements(erasableRowType) {
		return true
	}
	return typ.Kind() == reflect.Slice && holdsErasableRows(typ.Elem())
}
