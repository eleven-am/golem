package sqlite

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	"github.com/eleven-am/golem/go/internal/physical"
)

func (*Provider) renderInitial(schema physical.PhysicalSchema) (Script, error) {
	normalized, err := physical.Normalize(schema)
	if err != nil {
		return Script{}, err
	}
	if normalized.Provider.Provider != ir.SQLite {
		return Script{}, fmt.Errorf("sqlite render: physical provider is %s", normalized.Provider.Provider)
	}
	tables := make(map[ir.ModelID]physical.PhysicalTable, len(normalized.Tables))
	for _, table := range normalized.Tables {
		tables[table.ID] = table
	}
	statements := make([]string, 0, len(normalized.Tables)+len(normalized.System.Objects))
	for _, object := range normalized.System.Objects {
		statement, renderErr := renderSystemObject(object)
		if renderErr != nil {
			return Script{}, renderErr
		}
		statements = append(statements, statement)
	}
	for _, table := range normalized.Tables {
		statement, renderErr := renderTable(table, tables)
		if renderErr != nil {
			return Script{}, renderErr
		}
		statements = append(statements, statement)
	}
	for _, table := range normalized.Tables {
		for _, index := range table.Indexes {
			statement, renderErr := renderIndex(table, index)
			if renderErr != nil {
				return Script{}, renderErr
			}
			statements = append(statements, statement)
		}
	}
	return Script{statements: statements}, nil
}

func renderSystemObject(object physical.SystemObject) (string, error) {
	if object.Version != 1 {
		return "", fmt.Errorf("sqlite render: unsupported system object %s version %d", object.Kind, object.Version)
	}
	switch object.Kind {
	case physical.SystemMigrationLedger:
		return "CREATE TABLE " + quote(object.Name) + " (" +
			quote("migration_id") + " TEXT NOT NULL, " +
			quote("parent_chain_hash") + " TEXT NOT NULL, " +
			quote("chain_hash") + " TEXT NOT NULL, " +
			quote("file_checksums") + " TEXT NOT NULL, " +
			quote("before_physical_fingerprint") + " TEXT NOT NULL, " +
			quote("after_physical_fingerprint") + " TEXT NOT NULL, " +
			quote("phases") + " TEXT NOT NULL, " +
			quote("applied_at") + " TEXT NOT NULL, PRIMARY KEY (" + quote("migration_id") + ")) STRICT", nil
	case physical.SystemMigrationLock:
		return "CREATE TABLE " + quote(object.Name) + " (" + quote("lock_id") + " INTEGER NOT NULL, PRIMARY KEY (" + quote("lock_id") + "), CHECK (" + quote("lock_id") + " = 1)) STRICT", nil
	default:
		return "", fmt.Errorf("sqlite render: unsupported P1 system object kind %q", object.Kind)
	}
}

func renderTable(table physical.PhysicalTable, tables map[ir.ModelID]physical.PhysicalTable) (string, error) {
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.ID] = column
	}
	parts := make([]string, 0, len(table.Columns)+1+len(table.Uniques)+len(table.ForeignKeys)+len(table.Checks))
	for _, column := range table.Columns {
		rendered, err := renderColumn(column, columns)
		if err != nil {
			return "", fmt.Errorf("sqlite render table=%s column=%s: %w", table.ID, column.ID, err)
		}
		parts = append(parts, rendered)
	}
	if table.PrimaryKey != nil {
		rendered, err := renderKey("PRIMARY KEY", *table.PrimaryKey, columns)
		if err != nil {
			return "", err
		}
		parts = append(parts, rendered)
	}
	for _, key := range table.Uniques {
		rendered, err := renderKey("UNIQUE", key, columns)
		if err != nil {
			return "", err
		}
		parts = append(parts, rendered)
	}
	for _, foreignKey := range table.ForeignKeys {
		referenced, exists := tables[foreignKey.ReferencedTable]
		if !exists {
			return "", fmt.Errorf("sqlite render: referenced table %s not found", foreignKey.ReferencedTable)
		}
		referencedColumns := make(map[ir.FieldID]physical.PhysicalColumn, len(referenced.Columns))
		for _, column := range referenced.Columns {
			referencedColumns[column.ID] = column
		}
		local, err := renderFieldList(foreignKey.Columns, columns)
		if err != nil {
			return "", err
		}
		remote, err := renderFieldList(foreignKey.ReferencedColumns, referencedColumns)
		if err != nil {
			return "", err
		}
		parts = append(parts, "CONSTRAINT "+quote(foreignKey.Name)+" FOREIGN KEY ("+local+") REFERENCES "+quote(referenced.Name)+" ("+remote+") ON UPDATE "+renderAction(foreignKey.OnUpdate)+" ON DELETE "+renderAction(foreignKey.OnDelete)+renderDeferrability(foreignKey.Deferrable))
	}
	for _, check := range table.Checks {
		expression, err := renderExpression(check.Expression, columns)
		if err != nil {
			return "", fmt.Errorf("sqlite render check=%s: %w", check.ID, err)
		}
		parts = append(parts, "CONSTRAINT "+quote(check.Name)+" CHECK ("+expression+")")
	}
	return "CREATE TABLE " + quote(table.Name) + " (\n  " + strings.Join(parts, ",\n  ") + "\n) STRICT", nil
}

func renderColumn(column physical.PhysicalColumn, columns map[ir.FieldID]physical.PhysicalColumn) (string, error) {
	result := quote(column.Name) + " " + renderStorage(column.Storage)
	if !column.Nullable {
		result += " NOT NULL"
	}
	if column.Default.Kind == physical.DefaultLiteral {
		literal, err := renderLiteral(*column.Default.Literal)
		if err != nil {
			return "", err
		}
		result += " DEFAULT " + literal
	} else if column.Default.Kind == physical.DefaultProvider {
		if column.Default.Expression == nil || column.Default.Expression.Symbol == nil || column.Default.Expression.Symbol.Identity != "sqlite.identity" {
			return "", fmt.Errorf("unsupported provider default")
		}
	}
	if column.Generated != nil {
		expression, err := renderExpression(column.Generated.Expression, columns)
		if err != nil {
			return "", err
		}
		result += " GENERATED ALWAYS AS (" + expression + ") " + strings.ToUpper(string(column.Generated.Kind))
	}
	return result, nil
}

func renderKey(kind string, key physical.PhysicalKey, columns map[ir.FieldID]physical.PhysicalColumn) (string, error) {
	fields, err := renderFieldList(key.Columns, columns)
	if err != nil {
		return "", err
	}
	return "CONSTRAINT " + quote(key.Name) + " " + kind + " (" + fields + ")", nil
}

func renderIndex(table physical.PhysicalTable, index physical.PhysicalIndex) (string, error) {
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.ID] = column
	}
	keys := make([]string, len(index.Keys))
	for keyIndex, key := range index.Keys {
		var rendered string
		if key.Column != nil {
			column, exists := columns[*key.Column]
			if !exists {
				return "", fmt.Errorf("sqlite render index=%s missing field=%s", index.ID, *key.Column)
			}
			rendered = quote(column.Name)
		} else {
			expression, err := renderExpression(*key.Expression, columns)
			if err != nil {
				return "", err
			}
			rendered = "(" + expression + ")"
		}
		if key.Direction == ir.SortDesc {
			rendered += " DESC"
		} else {
			rendered += " ASC"
		}
		keys[keyIndex] = rendered
	}
	result := "CREATE "
	if index.Unique {
		result += "UNIQUE "
	}
	result += "INDEX " + quote(index.Name) + " ON " + quote(table.Name) + " (" + strings.Join(keys, ", ") + ")"
	if index.Predicate != nil {
		predicate, err := renderExpression(*index.Predicate, columns)
		if err != nil {
			return "", err
		}
		result += " WHERE " + predicate
	}
	return result, nil
}

func renderExpression(expression physical.Expression, columns map[ir.FieldID]physical.PhysicalColumn) (string, error) {
	switch expression.Kind {
	case physical.ExpressionColumn:
		if expression.Column == nil {
			return "", fmt.Errorf("column expression has no field ID")
		}
		column, exists := columns[*expression.Column]
		if !exists {
			return "", fmt.Errorf("column %s not found", *expression.Column)
		}
		return quote(column.Name), nil
	case physical.ExpressionLiteral:
		if expression.Literal == nil {
			return "", fmt.Errorf("literal expression has no literal")
		}
		return renderLiteral(*expression.Literal)
	case physical.ExpressionOperator, physical.ExpressionFunction, physical.ExpressionCast:
		if expression.Symbol == nil {
			return "", fmt.Errorf("registered expression has no symbol")
		}
		operands := make([]string, len(expression.Operands))
		for index, operand := range expression.Operands {
			rendered, err := renderExpression(operand, columns)
			if err != nil {
				return "", err
			}
			operands[index] = rendered
		}
		return renderSymbol(expression.Symbol.Identity, operands)
	default:
		return "", fmt.Errorf("unsupported expression kind %q", expression.Kind)
	}
}

func renderSymbol(identity string, operands []string) (string, error) {
	binary := map[string]string{
		schemaexpr.Add: "+", schemaexpr.Subtract: "-", schemaexpr.Multiply: "*", schemaexpr.Divide: "/", schemaexpr.Remainder: "%", schemaexpr.Concat: "||",
		schemaexpr.Equal: "=", schemaexpr.NotEqual: "<>", schemaexpr.Less: "<", schemaexpr.LessEqual: "<=", schemaexpr.Greater: ">", schemaexpr.GreaterEq: ">=",
	}
	if operator, exists := binary[identity]; exists && len(operands) == 2 {
		return "(" + operands[0] + " " + operator + " " + operands[1] + ")", nil
	}
	switch identity {
	case schemaexpr.Lower:
		return unaryFunction("lower", operands)
	case schemaexpr.Upper:
		return unaryFunction("upper", operands)
	case schemaexpr.Length:
		return unaryFunction("length", operands)
	case schemaexpr.Coalesce:
		return "coalesce(" + strings.Join(operands, ", ") + ")", nil
	case schemaexpr.IsNull:
		return unarySuffix(" IS NULL", operands)
	case schemaexpr.IsNotNull:
		return unarySuffix(" IS NOT NULL", operands)
	case schemaexpr.In:
		if len(operands) < 2 {
			return "", fmt.Errorf("IN requires operands")
		}
		return "(" + operands[0] + " IN (" + strings.Join(operands[1:], ", ") + "))", nil
	case schemaexpr.CastInt16ToInt32, schemaexpr.CastInt16ToInt64, schemaexpr.CastInt32ToInt64:
		if len(operands) != 1 {
			return "", fmt.Errorf("integer cast requires one operand")
		}
		return "CAST(" + operands[0] + " AS INTEGER)", nil
	case schemaexpr.CastInt64ToString:
		if len(operands) != 1 {
			return "", fmt.Errorf("string cast requires one operand")
		}
		return "CAST(" + operands[0] + " AS TEXT)", nil
	case "sqlite.predicate.and":
		return "(" + strings.Join(operands, " AND ") + ")", nil
	case "sqlite.predicate.or":
		return "(" + strings.Join(operands, " OR ") + ")", nil
	case "sqlite.predicate.not":
		if len(operands) != 1 {
			return "", fmt.Errorf("NOT requires one operand")
		}
		return "(NOT " + operands[0] + ")", nil
	case "sqlite.check.bool-domain":
		if len(operands) != 1 {
			return "", fmt.Errorf("bool check arity")
		}
		return "(" + operands[0] + " IS NULL OR " + operands[0] + " IN (0, 1))", nil
	case "sqlite.check.integer-range":
		if len(operands) != 3 {
			return "", fmt.Errorf("range check arity")
		}
		return "(" + operands[0] + " IS NULL OR " + operands[0] + " BETWEEN " + operands[1] + " AND " + operands[2] + ")", nil
	case "sqlite.check.max-length":
		if len(operands) != 2 {
			return "", fmt.Errorf("length check arity")
		}
		return "(" + operands[0] + " IS NULL OR length(" + operands[0] + ") <= " + operands[1] + ")", nil
	case "sqlite.check.enum-membership":
		if len(operands) < 2 {
			return "", fmt.Errorf("enum check arity")
		}
		return "(" + operands[0] + " IS NULL OR " + operands[0] + " IN (" + strings.Join(operands[1:], ", ") + "))", nil
	case "sqlite.check.json-valid":
		if len(operands) != 1 {
			return "", fmt.Errorf("JSON check arity")
		}
		return "(" + operands[0] + " IS NULL OR json_valid(" + operands[0] + "))", nil
	case "sqlite.check.json-array":
		if len(operands) != 1 {
			return "", fmt.Errorf("JSON array check arity")
		}
		return "(" + operands[0] + " IS NULL OR (json_valid(" + operands[0] + ") AND json_type(" + operands[0] + ") = 'array'))", nil
	case "sqlite.check.uuid-canonical":
		if len(operands) != 1 {
			return "", fmt.Errorf("UUID check arity")
		}
		value := operands[0]
		return "(" + value + " IS NULL OR (length(" + value + ") = 36 AND lower(" + value + ") = " + value + " AND substr(" + value + ", 9, 1) = '-' AND substr(" + value + ", 14, 1) = '-' AND substr(" + value + ", 19, 1) = '-' AND substr(" + value + ", 24, 1) = '-' AND " + value + " NOT GLOB '*[^0-9a-f-]*'))", nil
	case "sqlite.check.date-canonical":
		if len(operands) != 1 {
			return "", fmt.Errorf("Date check arity")
		}
		value := operands[0]
		return "(" + value + " IS NULL OR (length(" + value + ") = 10 AND substr(" + value + ", 5, 1) = '-' AND substr(" + value + ", 8, 1) = '-' AND date(" + value + ") = " + value + "))", nil
	case "sqlite.check.time-canonical":
		if len(operands) != 2 {
			return "", fmt.Errorf("Time check arity")
		}
		precision, err := strconv.Atoi(operands[1])
		if err != nil || precision < 0 || precision > 6 {
			return "", fmt.Errorf("Time precision is invalid")
		}
		length := 8
		fraction := ""
		if precision > 0 {
			length += 1 + precision
			fraction = " AND substr(" + operands[0] + ", 9, 1) = '.' AND substr(" + operands[0] + ", 10, " + fmt.Sprint(precision) + ") NOT GLOB '*[^0-9]*'"
		}
		return fmt.Sprintf("(%s IS NULL OR (length(%s) = %d AND substr(%s, 1, 8) GLOB '[0-2][0-9]:[0-5][0-9]:[0-5][0-9]' AND CAST(substr(%s, 1, 2) AS INTEGER) <= 23%s))", operands[0], operands[0], length, operands[0], operands[0], fraction), nil
	case "sqlite.check.datetime-precision":
		if len(operands) != 2 {
			return "", fmt.Errorf("DateTime precision check arity")
		}
		return "(" + operands[0] + " IS NULL OR " + operands[0] + " % " + operands[1] + " = 0)", nil
	default:
		return "", fmt.Errorf("unregistered SQLite expression symbol %q", identity)
	}
}

func renderLiteral(literal ir.TypedLiteralIR) (string, error) {
	switch literal.Kind {
	case ir.LiteralBool:
		if literal.Canonical == "true" {
			return "1", nil
		}
		if literal.Canonical == "false" {
			return "0", nil
		}
	case ir.LiteralInteger, ir.LiteralFloat, ir.LiteralDecimal:
		if _, err := strconv.ParseFloat(literal.Canonical, 64); err == nil {
			return literal.Canonical, nil
		}
	case ir.LiteralString, ir.LiteralUUID, ir.LiteralDate, ir.LiteralTime, ir.LiteralDateTime, ir.LiteralJSON, ir.LiteralEnum, ir.LiteralList:
		return quoteLiteral(literal.Canonical), nil
	case ir.LiteralBytes:
		decoded, err := base64.RawStdEncoding.DecodeString(literal.Canonical)
		if err != nil {
			return "", fmt.Errorf("invalid canonical bytes literal: %w", err)
		}
		return "X'" + hex.EncodeToString(decoded) + "'", nil
	}
	return "", fmt.Errorf("unsupported canonical literal %s(%q)", literal.Kind, literal.Canonical)
}

func renderStorage(storage physical.StorageType) string {
	switch storage.Kind {
	case physical.StorageSQLiteInteger:
		return "INTEGER"
	case physical.StorageSQLiteReal:
		return "REAL"
	case physical.StorageSQLiteText:
		return "TEXT"
	case physical.StorageSQLiteBlob:
		return "BLOB"
	default:
		panic("validated SQLite storage kind is unreachable: " + storage.Kind)
	}
}

func renderFieldList(fields []ir.FieldID, columns map[ir.FieldID]physical.PhysicalColumn) (string, error) {
	values := make([]string, len(fields))
	for index, field := range fields {
		column, exists := columns[field]
		if !exists {
			return "", fmt.Errorf("sqlite render missing field=%s", field)
		}
		values[index] = quote(column.Name)
	}
	return strings.Join(values, ", "), nil
}

func renderAction(action ir.ReferentialAction) string {
	return map[ir.ReferentialAction]string{ir.ActionNoAction: "NO ACTION", ir.ActionRestrict: "RESTRICT", ir.ActionCascade: "CASCADE", ir.ActionSetNull: "SET NULL", ir.ActionSetDefault: "SET DEFAULT"}[action]
}

func renderDeferrability(value ir.Deferrability) string {
	switch value {
	case ir.InitiallyImmediate:
		return " DEFERRABLE INITIALLY IMMEDIATE"
	case ir.InitiallyDeferred:
		return " DEFERRABLE INITIALLY DEFERRED"
	default:
		return " NOT DEFERRABLE"
	}
}

func quote(name physical.PhysicalName) string {
	return `"` + strings.ReplaceAll(string(name), `"`, `""`) + `"`
}
func quoteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func unaryFunction(name string, values []string) (string, error) {
	if len(values) != 1 {
		return "", fmt.Errorf("%s requires one operand", name)
	}
	return name + "(" + values[0] + ")", nil
}
func unarySuffix(suffix string, values []string) (string, error) {
	if len(values) != 1 {
		return "", fmt.Errorf("suffix requires one operand")
	}
	return "(" + values[0] + suffix + ")", nil
}
