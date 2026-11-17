package physical

import "github.com/eleven-am/golem/go/internal/compiler/ir"

// GeneratedFieldClosure returns the source and every generated field
// whose value can change with it. Callers provide a validated physical table;
// generated expressions reference only fields in that same table.
func GeneratedFieldClosure(table PhysicalTable, source ir.FieldID) map[ir.FieldID]bool {
	affected := map[ir.FieldID]bool{source: true}
	for changed := true; changed; {
		changed = false
		for _, column := range table.Columns {
			if column.Generated == nil || affected[column.ID] {
				continue
			}
			for _, dependency := range expressionFieldIDs(column.Generated.Expression) {
				if affected[dependency] {
					affected[column.ID] = true
					changed = true
					break
				}
			}
		}
	}
	return affected
}

// ExpressionReferencesAny tests a typed expression against stable fields.
func ExpressionReferencesAny(expression Expression, fields map[ir.FieldID]bool) bool {
	if expression.Column != nil && fields[*expression.Column] {
		return true
	}
	for _, operand := range expression.Operands {
		if ExpressionReferencesAny(operand, fields) {
			return true
		}
	}
	return false
}
