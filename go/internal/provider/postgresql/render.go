package postgresql

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

func (provider *Provider) renderInitial(schema physical.PhysicalSchema) (Script, error) {
	normalized, err := physical.Normalize(schema)
	if err != nil {
		return Script{}, err
	}
	if normalized.Provider.Provider != ir.PostgreSQL {
		return Script{}, fmt.Errorf("postgresql render: schema provider is %s", normalized.Provider.Provider)
	}
	renderer := ddlRenderer{schema: normalized, tables: map[ir.ModelID]physical.PhysicalTable{}}
	for _, table := range normalized.Tables {
		renderer.tables[table.ID] = table
	}
	statements := make([]string, 0, len(normalized.Tables)+len(normalized.Extensions)*4+2)
	if len(normalized.Extensions) != 0 {
		statements = append(statements, "CREATE EXTENSION IF NOT EXISTS vector")
	}
	statements = append(statements, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quote(normalized.Namespace.Name)))
	if normalized.System.Version != 0 {
		statements = append(statements, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quote(normalized.System.Namespace.Name)))
		for _, object := range normalized.System.Objects {
			switch {
			case object.Kind == physical.SystemMigrationLedger && object.Version == 1:
				statements = append(statements, renderLedger(normalized.System.Namespace.Name, object.Name))
			case object.Kind == physical.SystemMigrationLock && object.Version == 1:
				// PostgreSQL represents the lock as the probed transaction-scoped
				// advisory capability; it deliberately has no relation DDL.
			case object.Kind == physical.SystemOutbox && object.Version == 1:
				statements = append(statements, renderOutbox(normalized.System.Namespace.Name, object.Name)...)
			case object.Kind == physical.SystemOutboxDelivery && object.Version == 1:
				statements = append(statements, renderOutboxDelivery(normalized.System.Namespace.Name, object.Name)...)
			case object.Kind == physical.SystemUpsertGuard && object.Version == 1:
				// PostgreSQL implements selector serialization with a
				// transaction-scoped advisory lock; no relation is rendered.
			default:
				return Script{}, fmt.Errorf("postgresql render: unsupported system object kind=%s version=%d", object.Kind, object.Version)
			}
		}
	}
	for _, table := range normalized.Tables {
		statement, renderErr := renderer.table(table)
		if renderErr != nil {
			return Script{}, renderErr
		}
		statements = append(statements, statement)
	}
	// Foreign keys are emitted after every table/key so cyclic graphs are valid.
	for _, table := range normalized.Tables {
		for _, foreign := range table.ForeignKeys {
			statement, renderErr := renderer.foreignKey(table, foreign)
			if renderErr != nil {
				return Script{}, renderErr
			}
			statements = append(statements, statement)
		}
	}
	for _, table := range normalized.Tables {
		for _, index := range table.Indexes {
			statement, renderErr := renderer.index(table, index)
			if renderErr != nil {
				return Script{}, renderErr
			}
			statements = append(statements, statement)
		}
	}
	for _, extension := range normalized.Extensions {
		rendered, renderErr := renderPostgreSQLSemanticExtension(normalized.Namespace.Name, extension)
		if renderErr != nil {
			return Script{}, renderErr
		}
		statements = append(statements, rendered...)
	}
	return Script{statements: statements}, nil
}

func renderPostgreSQLSemanticExtension(namespace physical.PhysicalName, extension physical.Extension) ([]string, error) {
	descriptor, err := semanticstorage.Decode(extension)
	if err != nil {
		return nil, fmt.Errorf("postgresql render semantic extension %s: %w", extension.ID, err)
	}
	state := physical.PhysicalName(string(descriptor.Storage) + "_state")
	vectors := physical.PhysicalName(string(descriptor.Storage) + "_vec")
	index := physical.PhysicalName(string(descriptor.Storage) + "_hnsw")
	return []string{
		"CREATE TABLE " + qualified(namespace, state) + " (" +
			quote("record_key") + " text NOT NULL PRIMARY KEY, " +
			quote("source_hash") + " bytea NOT NULL, " +
			quote("space_fingerprint") + " text NOT NULL, " +
			quote("status") + " text NOT NULL CHECK (" + quote("status") + " IN ('pending','ready','failed')), " +
			quote("attempt_count") + " integer NOT NULL DEFAULT 0 CHECK (" + quote("attempt_count") + " >= 0), " +
			quote("error_code") + " text, " +
			quote("updated_at") + " bigint NOT NULL CHECK (" + quote("updated_at") + " >= 0))",
		"CREATE TABLE " + qualified(namespace, vectors) + " (" +
			quote("record_key") + " text NOT NULL PRIMARY KEY, " +
			quote("embedding") + " vector(" + strconv.Itoa(int(descriptor.Dimensions)) + ") NOT NULL)",
		"CREATE INDEX " + quote(index) + " ON " + qualified(namespace, vectors) + " USING hnsw (" + quote("embedding") + " vector_cosine_ops)",
	}, nil
}

type ddlRenderer struct {
	schema           physical.PhysicalSchema
	tables           map[ir.ModelID]physical.PhysicalTable
	beforeExtensions map[ir.ExtensionID]physical.Extension
}

func (r ddlRenderer) table(table physical.PhysicalTable) (string, error) {
	columns := make([]string, 0, len(table.Columns)+1+len(table.Uniques)+len(table.Checks))
	for _, column := range table.Columns {
		rendered, err := r.column(table, column)
		if err != nil {
			return "", err
		}
		columns = append(columns, rendered)
	}
	if table.PrimaryKey != nil {
		key, err := r.key(table, *table.PrimaryKey, "PRIMARY KEY")
		if err != nil {
			return "", err
		}
		columns = append(columns, key)
	}
	for _, key := range table.Uniques {
		rendered, err := r.key(table, key, "UNIQUE")
		if err != nil {
			return "", err
		}
		columns = append(columns, rendered)
	}
	for _, check := range table.Checks {
		expression, err := r.expression(table, check.Expression)
		if err != nil {
			return "", fmt.Errorf("postgresql render check %s: %w", check.ID, err)
		}
		columns = append(columns, fmt.Sprintf("CONSTRAINT %s CHECK (%s)", quote(check.Name), expression))
	}
	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", qualified(r.schema.Namespace.Name, table.Name), strings.Join(columns, ",\n  ")), nil
}

func (r ddlRenderer) column(table physical.PhysicalTable, column physical.PhysicalColumn) (string, error) {
	storage, err := renderStorage(column.Storage)
	if err != nil {
		return "", err
	}
	result := quote(column.Name) + " " + storage
	if column.Default.Kind == physical.DefaultProvider && column.Default.Expression != nil && column.Default.Expression.Symbol != nil && column.Default.Expression.Symbol.Identity == "golem.postgresql.default.identity.v1" {
		result += " GENERATED BY DEFAULT AS IDENTITY"
	} else if column.Default.Kind != physical.DefaultNone {
		value, defaultErr := r.defaultValue(table, column.Default)
		if defaultErr != nil {
			return "", defaultErr
		}
		result += " DEFAULT " + value
	}
	if column.Generated != nil {
		expression, expressionErr := r.expression(table, column.Generated.Expression)
		if expressionErr != nil {
			return "", expressionErr
		}
		if column.Generated.Kind != physical.GeneratedStored {
			return "", fmt.Errorf("postgresql render: virtual generated columns are not supported")
		}
		result += " GENERATED ALWAYS AS (" + expression + ") STORED"
	}
	if !column.Nullable {
		result += " NOT NULL"
	}
	return result, nil
}

func (r ddlRenderer) key(table physical.PhysicalTable, key physical.PhysicalKey, kind string) (string, error) {
	columns, err := r.columnNames(table, key.Columns)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("CONSTRAINT %s %s (%s)", quote(key.Name), kind, strings.Join(columns, ", ")), nil
}

func (r ddlRenderer) foreignKey(table physical.PhysicalTable, foreign physical.PhysicalForeignKey) (string, error) {
	referenced, ok := r.tables[foreign.ReferencedTable]
	if !ok {
		return "", fmt.Errorf("postgresql render: foreign key %s references unknown table", foreign.ID)
	}
	localColumns, err := r.columnNames(table, foreign.Columns)
	if err != nil {
		return "", err
	}
	remoteColumns, err := r.columnNames(referenced, foreign.ReferencedColumns)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s) ON UPDATE %s ON DELETE %s%s",
		qualified(r.schema.Namespace.Name, table.Name), quote(foreign.Name), strings.Join(localColumns, ", "),
		qualified(r.schema.Namespace.Name, referenced.Name), strings.Join(remoteColumns, ", "),
		renderAction(foreign.OnUpdate), renderAction(foreign.OnDelete), renderDeferrable(foreign.Deferrable)), nil
}

func (r ddlRenderer) index(table physical.PhysicalTable, index physical.PhysicalIndex) (string, error) {
	keys := make([]string, len(index.Keys))
	for position, key := range index.Keys {
		var item string
		if key.Column != nil {
			var err error
			item, err = r.columnName(table, *key.Column)
			if err != nil {
				return "", err
			}
		} else if key.Expression != nil {
			expression, err := r.expression(table, *key.Expression)
			if err != nil {
				return "", err
			}
			item = "(" + expression + ")"
		}
		if key.Collation != nil {
			return "", fmt.Errorf("postgresql render: unregistered collation %s", key.Collation.Identity)
		}
		if key.OpClass != nil {
			return "", fmt.Errorf("postgresql render: unregistered operator class %s", key.OpClass.Identity)
		}
		if key.Direction == ir.SortDesc {
			item += " DESC"
		} else {
			item += " ASC"
		}
		if key.Nulls == ir.NullsFirst {
			item += " NULLS FIRST"
		} else if key.Nulls == ir.NullsLast {
			item += " NULLS LAST"
		}
		keys[position] = item
	}
	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}
	// PostgreSQL forbids a schema-qualified CREATE INDEX name; the fully
	// qualified parent table fixes the index schema without search_path.
	statement := fmt.Sprintf("CREATE %sINDEX %s ON %s USING %s (%s)", unique, quote(index.Name), qualified(r.schema.Namespace.Name, table.Name), strings.ToUpper(string(index.Method)), strings.Join(keys, ", "))
	if len(index.Include) > 0 {
		included, err := r.columnNames(table, index.Include)
		if err != nil {
			return "", err
		}
		statement += " INCLUDE (" + strings.Join(included, ", ") + ")"
	}
	if index.Predicate != nil {
		predicate, err := r.expression(table, *index.Predicate)
		if err != nil {
			return "", err
		}
		statement += " WHERE " + predicate
	}
	return statement, nil
}

func (r ddlRenderer) defaultValue(table physical.PhysicalTable, value physical.PhysicalDefault) (string, error) {
	if value.Kind == physical.DefaultLiteral && value.Literal != nil {
		return renderLiteral(*value.Literal)
	}
	if value.Kind == physical.DefaultProvider && value.Expression != nil {
		return r.expression(table, *value.Expression)
	}
	return "", fmt.Errorf("postgresql render: invalid physical default")
}

func (r ddlRenderer) expression(table physical.PhysicalTable, value physical.Expression) (string, error) {
	switch value.Kind {
	case physical.ExpressionColumn:
		if value.Column == nil {
			return "", fmt.Errorf("column expression has no field")
		}
		return r.columnName(table, *value.Column)
	case physical.ExpressionLiteral:
		if value.Literal == nil {
			return "", fmt.Errorf("literal expression has no value")
		}
		return renderLiteral(*value.Literal)
	}
	if value.Symbol == nil {
		return "", fmt.Errorf("registered expression lacks symbol")
	}
	operands := make([]string, len(value.Operands))
	for index, operand := range value.Operands {
		rendered, err := r.expression(table, operand)
		if err != nil {
			return "", err
		}
		operands[index] = rendered
	}
	switch value.Symbol.Identity {
	case schemaexpr.Add:
		return joinOperator("+", operands)
	case schemaexpr.Subtract:
		return joinOperator("-", operands)
	case schemaexpr.Multiply:
		return joinOperator("*", operands)
	case schemaexpr.Divide:
		return joinOperator("/", operands)
	case schemaexpr.Remainder:
		return joinOperator("%", operands)
	case schemaexpr.Concat:
		return joinOperator("||", operands)
	case schemaexpr.Equal:
		return binaryOperator("=", operands)
	case schemaexpr.NotEqual:
		return binaryOperator("<>", operands)
	case schemaexpr.Less:
		return binaryOperator("<", operands)
	case schemaexpr.LessEqual:
		return binaryOperator("<=", operands)
	case schemaexpr.Greater:
		return binaryOperator(">", operands)
	case schemaexpr.GreaterEq:
		return binaryOperator(">=", operands)
	case schemaexpr.IsNull:
		return postfix("IS NULL", operands)
	case schemaexpr.IsNotNull:
		return postfix("IS NOT NULL", operands)
	case schemaexpr.In:
		if len(operands) < 2 {
			return "", fmt.Errorf("IN requires at least two operands")
		}
		return "(" + operands[0] + " IN (" + strings.Join(operands[1:], ", ") + "))", nil
	case schemaexpr.Lower:
		return function("lower", operands, 1)
	case schemaexpr.Upper:
		return function("upper", operands, 1)
	case schemaexpr.Length:
		return function("char_length", operands, 1)
	case schemaexpr.Coalesce:
		if len(operands) < 2 {
			return "", fmt.Errorf("coalesce requires operands")
		}
		return "COALESCE(" + strings.Join(operands, ", ") + ")", nil
	case "golem.postgresql.function.octet-length.v1":
		return function("octet_length", operands, 1)
	case "golem.postgresql.function.jsonb-typeof.v1":
		return function("jsonb_typeof", operands, 1)
	case "golem.schema.predicate.and.v1":
		return joinOperator("AND", operands)
	case "golem.schema.predicate.or.v1":
		return joinOperator("OR", operands)
	case "golem.schema.predicate.not.v1":
		if len(operands) != 1 {
			return "", fmt.Errorf("NOT requires one operand")
		}
		return "(NOT " + operands[0] + ")", nil
	case schemaexpr.CastInt16ToInt32:
		return cast(operands, "integer")
	case schemaexpr.CastInt16ToInt64, schemaexpr.CastInt32ToInt64:
		return cast(operands, "bigint")
	case schemaexpr.CastInt64ToString:
		return cast(operands, "text")
	default:
		return "", fmt.Errorf("postgresql render: unregistered semantic symbol %q", value.Symbol.Identity)
	}
}

func (r ddlRenderer) columnName(table physical.PhysicalTable, id ir.FieldID) (string, error) {
	for _, column := range table.Columns {
		if column.ID == id {
			return quote(column.Name), nil
		}
	}
	return "", fmt.Errorf("postgresql render: field %s does not belong to table %s", id, table.ID)
}
func (r ddlRenderer) columnNames(table physical.PhysicalTable, ids []ir.FieldID) ([]string, error) {
	result := make([]string, len(ids))
	for i, id := range ids {
		name, err := r.columnName(table, id)
		if err != nil {
			return nil, err
		}
		result[i] = name
	}
	return result, nil
}

func renderStorage(storage physical.StorageType) (string, error) {
	switch storage.Kind {
	case physical.StoragePostgreSQLBoolean:
		return "boolean", nil
	case physical.StoragePostgreSQLSmallInt:
		return "smallint", nil
	case physical.StoragePostgreSQLInteger:
		return "integer", nil
	case physical.StoragePostgreSQLBigInt:
		return "bigint", nil
	case physical.StoragePostgreSQLReal:
		return "real", nil
	case physical.StoragePostgreSQLDouble:
		return "double precision", nil
	case physical.StoragePostgreSQLNumeric:
		return fmt.Sprintf("numeric(%d,%d)", storage.Precision, storage.Scale), nil
	case physical.StoragePostgreSQLText:
		return "text", nil
	case physical.StoragePostgreSQLBytea:
		return "bytea", nil
	case physical.StoragePostgreSQLUUID:
		return "uuid", nil
	case physical.StoragePostgreSQLDate:
		return "date", nil
	case physical.StoragePostgreSQLTime:
		return fmt.Sprintf("time(%d) without time zone", storage.Length), nil
	case physical.StoragePostgreSQLTimestampTZ:
		return fmt.Sprintf("timestamp(%d) with time zone", storage.Length), nil
	case physical.StoragePostgreSQLJSONB:
		return "jsonb", nil
	default:
		return "", fmt.Errorf("postgresql render: unsupported storage %q", storage.Kind)
	}
}

func renderLiteral(value ir.TypedLiteralIR) (string, error) {
	switch value.Kind {
	case ir.LiteralBool:
		if value.Canonical != "true" && value.Canonical != "false" {
			return "", fmt.Errorf("invalid boolean literal")
		}
		return strings.ToUpper(value.Canonical), nil
	case ir.LiteralInteger:
		if _, err := strconv.ParseInt(value.Canonical, 10, 64); err != nil {
			return "", fmt.Errorf("invalid integer literal: %w", err)
		}
		return value.Canonical, nil
	case ir.LiteralFloat, ir.LiteralDecimal:
		if _, err := strconv.ParseFloat(value.Canonical, 64); err != nil || strings.ContainsAny(strings.ToLower(value.Canonical), "in") {
			return "", fmt.Errorf("invalid finite numeric literal")
		}
		return value.Canonical, nil
	case ir.LiteralBytes:
		decoded, err := base64.RawStdEncoding.DecodeString(value.Canonical)
		if err != nil {
			return "", err
		}
		return quoteLiteral(`\x`+hex.EncodeToString(decoded)) + "::bytea", nil
	case ir.LiteralUUID:
		return quoteLiteral(value.Canonical) + "::uuid", nil
	case ir.LiteralDate:
		return quoteLiteral(value.Canonical) + "::date", nil
	case ir.LiteralTime:
		return quoteLiteral(value.Canonical) + "::time", nil
	case ir.LiteralDateTime:
		return quoteLiteral(value.Canonical) + "::timestamptz", nil
	case ir.LiteralJSON, ir.LiteralList:
		return quoteLiteral(value.Canonical) + "::jsonb", nil
	case ir.LiteralString, ir.LiteralEnum:
		return quoteLiteral(value.Canonical), nil
	default:
		return "", fmt.Errorf("unsupported literal kind %q", value.Kind)
	}
}

func renderAction(value ir.ReferentialAction) string {
	return map[ir.ReferentialAction]string{ir.ActionNoAction: "NO ACTION", ir.ActionRestrict: "RESTRICT", ir.ActionCascade: "CASCADE", ir.ActionSetNull: "SET NULL", ir.ActionSetDefault: "SET DEFAULT"}[value]
}
func renderDeferrable(value ir.Deferrability) string {
	if value == ir.InitiallyImmediate {
		return " DEFERRABLE INITIALLY IMMEDIATE"
	}
	if value == ir.InitiallyDeferred {
		return " DEFERRABLE INITIALLY DEFERRED"
	}
	return " NOT DEFERRABLE"
}
func quote(value physical.PhysicalName) string {
	return `"` + strings.ReplaceAll(string(value), `"`, `""`) + `"`
}
func qualified(namespace, name physical.PhysicalName) string {
	return quote(namespace) + "." + quote(name)
}
func quoteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func joinOperator(operator string, values []string) (string, error) {
	if len(values) < 2 {
		return "", fmt.Errorf("%s requires at least two operands", operator)
	}
	return "(" + strings.Join(values, " "+operator+" ") + ")", nil
}
func binaryOperator(operator string, values []string) (string, error) {
	if len(values) != 2 {
		return "", fmt.Errorf("%s requires two operands", operator)
	}
	return "(" + values[0] + " " + operator + " " + values[1] + ")", nil
}
func postfix(operator string, values []string) (string, error) {
	if len(values) != 1 {
		return "", fmt.Errorf("%s requires one operand", operator)
	}
	return "(" + values[0] + " " + operator + ")", nil
}
func function(name string, values []string, arity int) (string, error) {
	if len(values) != arity {
		return "", fmt.Errorf("%s requires %d operands", name, arity)
	}
	return name + "(" + strings.Join(values, ", ") + ")", nil
}
func cast(values []string, target string) (string, error) {
	if len(values) != 1 {
		return "", fmt.Errorf("cast requires one operand")
	}
	return "CAST(" + values[0] + " AS " + target + ")", nil
}

func renderLedger(namespace, name physical.PhysicalName) string {
	return fmt.Sprintf("CREATE TABLE %s (\n  %s text PRIMARY KEY,\n  %s text NOT NULL,\n  %s text NOT NULL,\n  %s jsonb NOT NULL,\n  %s text NOT NULL,\n  %s text NOT NULL,\n  %s jsonb NOT NULL,\n  %s timestamptz(6) NOT NULL\n)", qualified(namespace, name), quote("migration_id"), quote("parent_chain_hash"), quote("chain_hash"), quote("file_checksums"), quote("before_physical_fingerprint"), quote("after_physical_fingerprint"), quote("phases"), quote("applied_at"))
}

func renderOutbox(namespace, name physical.PhysicalName) []string {
	table := fmt.Sprintf("CREATE TABLE %s (\n  %s text PRIMARY KEY,\n  %s integer NOT NULL CHECK (%s > 0),\n  %s text NOT NULL,\n  %s text NOT NULL,\n  %s text NOT NULL,\n  %s text NOT NULL CHECK (%s IN ('created','updated','deleted')),\n  %s bytea,\n  %s bytea,\n  %s text NOT NULL,\n  %s integer NOT NULL CHECK (%s >= 0),\n  %s bytea NOT NULL,\n  %s bytea,\n  %s timestamptz(6) NOT NULL,\n  UNIQUE (%s, %s),\n  CHECK ((%s = 'created' AND %s IS NULL AND %s IS NOT NULL) OR (%s = 'updated' AND %s IS NOT NULL AND %s IS NOT NULL) OR (%s = 'deleted' AND %s IS NOT NULL AND %s IS NULL))\n)",
		qualified(namespace, name), quote("event_id"), quote("fact_version"), quote("fact_version"), quote("codec_identity"), quote("generation_fingerprint"), quote("model_id"), quote("action"), quote("action"), quote("before_identity"), quote("after_identity"), quote("causation_id"), quote("transaction_ordinal"), quote("transaction_ordinal"), quote("metadata"), quote("delete_snapshot"), quote("recorded_at"), quote("causation_id"), quote("transaction_ordinal"), quote("action"), quote("before_identity"), quote("after_identity"), quote("action"), quote("before_identity"), quote("after_identity"), quote("action"), quote("before_identity"), quote("after_identity"))
	index := fmt.Sprintf("CREATE INDEX %s ON %s (%s, %s)", quote("_golem_outbox_pending"), qualified(namespace, name), quote("recorded_at"), quote("event_id"))
	return []string{table, index}
}

func renderOutboxDelivery(namespace, name physical.PhysicalName) []string {
	q := quote
	table := "CREATE TABLE " + qualified(namespace, name) + " (" +
		q("causation_id") + " text NOT NULL, " +
		q("status") + " text NOT NULL, " +
		q("first_recorded_at") + " timestamptz(6) NOT NULL, " +
		q("attempt_count") + " bigint NOT NULL, " +
		q("available_at") + " timestamptz(6) NOT NULL, " +
		q("lease_token") + " text, " +
		q("lease_until") + " timestamptz(6), " +
		q("delivered_at") + " timestamptz(6), " +
		q("last_failure_code") + " text, " +
		q("blocked_at") + " timestamptz(6), " +
		q("retired_at") + " timestamptz(6), " +
		q("updated_at") + " timestamptz(6) NOT NULL, " +
		"PRIMARY KEY (" + q("causation_id") + "), " +
		"CHECK (" + q("causation_id") + " ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'), " +
		"CHECK (" + q("status") + " IN ('pending','leased','delivered','blocked','retired')), " +
		"CHECK (" + q("attempt_count") + " >= 0), " +
		"CHECK (" + q("lease_token") + " IS NULL OR " + q("lease_token") + " ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'), " +
		"CHECK (" + q("last_failure_code") + " IS NULL OR " + q("last_failure_code") + " ~ '^[a-z0-9][a-z0-9._-]{0,127}$'), " +
		"CHECK ((" + q("status") + " = 'pending' AND " + q("lease_token") + " IS NULL AND " + q("lease_until") + " IS NULL AND " + q("delivered_at") + " IS NULL AND " + q("blocked_at") + " IS NULL AND " + q("retired_at") + " IS NULL) OR (" + q("status") + " = 'leased' AND " + q("lease_token") + " IS NOT NULL AND " + q("lease_until") + " IS NOT NULL AND " + q("delivered_at") + " IS NULL AND " + q("blocked_at") + " IS NULL AND " + q("retired_at") + " IS NULL) OR (" + q("status") + " = 'delivered' AND " + q("lease_token") + " IS NULL AND " + q("lease_until") + " IS NULL AND " + q("delivered_at") + " IS NOT NULL AND " + q("blocked_at") + " IS NULL AND " + q("retired_at") + " IS NULL) OR (" + q("status") + " = 'blocked' AND " + q("lease_token") + " IS NULL AND " + q("lease_until") + " IS NULL AND " + q("delivered_at") + " IS NULL AND " + q("blocked_at") + " IS NOT NULL AND " + q("retired_at") + " IS NULL AND " + q("last_failure_code") + " IS NOT NULL) OR (" + q("status") + " = 'retired' AND " + q("lease_token") + " IS NULL AND " + q("lease_until") + " IS NULL AND " + q("delivered_at") + " IS NULL AND " + q("blocked_at") + " IS NULL AND " + q("retired_at") + " IS NOT NULL))" +
		")"
	index := "CREATE INDEX " + q("_golem_outbox_delivery_pending") + " ON " + qualified(namespace, name) + " (" + q("status") + ", " + q("available_at") + ", " + q("first_recorded_at") + ", " + q("causation_id") + ")"
	return []string{table, index}
}
