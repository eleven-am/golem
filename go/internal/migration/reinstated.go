package migration

import (
	"math/big"
	"reflect"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func snapshotRequiresApproval(before, after physical.PhysicalSchema, operations []Operation, operation Operation) bool {
	if isDerivedExtensionDrop(operation, before) || reinstatesDroppedConstraint(before, after, operations, operation) {
		return false
	}
	return RequiresApproval(operation)
}

func snapshotOperationRisk(before, after physical.PhysicalSchema, operations []Operation, operation Operation) Risk {
	switch {
	case isDerivedExtensionDrop(operation, before):
		return RiskSafe
	case reinstatesDroppedConstraint(before, after, operations, operation):
		return RiskLocking
	case operation.Kind == AlterColumnType && sqliteStringWideningOperation(before, after, operation):
		return RiskRewrite
	}
	return operation.Risk
}

func sqliteStringWideningOperation(before, after physical.PhysicalSchema, operation Operation) bool {
	if before.Provider.Provider != ir.SQLite || after.Provider.Provider != ir.SQLite {
		return false
	}
	previous, hadBefore := schemaColumn(before, ir.FieldID(operation.ObjectID))
	current, hasAfter := schemaColumn(after, ir.FieldID(operation.ObjectID))
	return hadBefore && hasAfter && SQLiteStringWidening(previous.Storage, current.Storage)
}

func schemaColumn(schema physical.PhysicalSchema, field ir.FieldID) (physical.PhysicalColumn, bool) {
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			if column.ID == field {
				return column, true
			}
		}
	}
	return physical.PhysicalColumn{}, false
}

func reinstatesDroppedConstraint(before, after physical.PhysicalSchema, operations []Operation, operation Operation) bool {
	drop, reinstatable := map[OperationKind]OperationKind{AddPrimaryKey: DropPrimaryKey, AddUnique: DropUnique, AddCheck: DropCheck}[operation.Kind]
	if !reinstatable {
		return false
	}
	dropped := false
	for _, candidate := range operations {
		if candidate.Kind == drop && candidate.ObjectID == operation.ObjectID {
			dropped = true
		}
	}
	if !dropped {
		return false
	}
	if operation.Kind == AddCheck {
		beforeTable, previous, hadBefore := schemaCheck(before, operation.ObjectID)
		afterTable, current, hasAfter := schemaCheck(after, operation.ObjectID)
		return hadBefore && hasAfter && beforeTable == afterTable && reflect.DeepEqual(previous.RequiredCapabilities, current.RequiredCapabilities) && checkAdmitsEveryPriorValue(previous.Expression, current.Expression)
	}
	beforeTable, previous, hadBefore := schemaKey(before, operation.Kind == AddPrimaryKey, operation.ObjectID)
	afterTable, current, hasAfter := schemaKey(after, operation.Kind == AddPrimaryKey, operation.ObjectID)
	previous.Name, current.Name = "", ""
	return hadBefore && hasAfter && beforeTable == afterTable && reflect.DeepEqual(previous, current)
}

func schemaCheck(schema physical.PhysicalSchema, id string) (ir.ModelID, physical.PhysicalCheck, bool) {
	for _, table := range schema.Tables {
		for _, check := range table.Checks {
			if string(check.ID) == id {
				return table.ID, check, true
			}
		}
	}
	return "", physical.PhysicalCheck{}, false
}

func schemaKey(schema physical.PhysicalSchema, primary bool, id string) (ir.ModelID, physical.PhysicalKey, bool) {
	for _, table := range schema.Tables {
		if primary {
			if table.PrimaryKey != nil && string(table.PrimaryKey.ID) == id {
				return table.ID, *table.PrimaryKey, true
			}
			continue
		}
		for _, key := range table.Uniques {
			if string(key.ID) == id {
				return table.ID, key, true
			}
		}
	}
	return "", physical.PhysicalKey{}, false
}

func checkAdmitsEveryPriorValue(before, after physical.Expression) bool {
	left, right := constraintShape(before), constraintShape(after)
	if left.Kind != physical.ExpressionOperator || right.Kind != physical.ExpressionOperator || left.Symbol == nil || right.Symbol == nil || !reflect.DeepEqual(left.Symbol, right.Symbol) {
		return reflect.DeepEqual(left, right)
	}
	switch left.Symbol.Identity {
	case "sqlite.check.max-length":
		return boundedOperands(left, right, 2) && integerLiteralAtMost(left.Operands[1], right.Operands[1])
	case "sqlite.check.integer-range":
		return boundedOperands(left, right, 3) && integerLiteralAtMost(right.Operands[1], left.Operands[1]) && integerLiteralAtMost(left.Operands[2], right.Operands[2])
	}
	return reflect.DeepEqual(left, right)
}

func boundedOperands(left, right physical.Expression, arity int) bool {
	if len(left.Operands) != arity || len(right.Operands) != arity || !reflect.DeepEqual(left.Operands[0], right.Operands[0]) {
		return false
	}
	leftShell, rightShell := left, right
	leftShell.Operands, rightShell.Operands = nil, nil
	return reflect.DeepEqual(leftShell, rightShell)
}

func integerLiteralAtMost(lower, upper physical.Expression) bool {
	low, lowOK := integerLiteral(lower)
	high, highOK := integerLiteral(upper)
	return lowOK && highOK && low.Cmp(high) <= 0
}

func integerLiteral(expression physical.Expression) (*big.Int, bool) {
	if expression.Kind != physical.ExpressionLiteral || expression.Literal == nil || expression.Literal.Kind != ir.LiteralInteger || len(expression.Operands) != 0 {
		return nil, false
	}
	return new(big.Int).SetString(expression.Literal.Canonical, 10)
}

func constraintShape(expression physical.Expression) physical.Expression {
	result := expression
	result.Nullable = false
	if result.Kind == physical.ExpressionColumn {
		result.Type = physical.StorageType{}
	}
	result.Operands = make([]physical.Expression, len(expression.Operands))
	for index, operand := range expression.Operands {
		result.Operands[index] = constraintShape(operand)
	}
	return result
}
