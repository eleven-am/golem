package golem

// RuntimeModelRowFromTyped erases only the generated Go model witness while
// retaining the complete authorized/masked occurrence-aware P3 row.
func RuntimeModelRowFromTyped[M any](row Row[M]) RuntimeModelRow {
	return cloneRuntimeModelRow(RuntimeModelRow{model: row.model, cells: row.cells, counts: row.counts, occurrences: row.occurrences})
}
