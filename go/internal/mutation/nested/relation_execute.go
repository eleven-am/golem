package nested

import (
	"context"
	"fmt"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/jmoiron/sqlx"
)

// ExecuteRelationSQL runs one already-rendered relation statement on the
// caller-owned transaction/queryer and decodes only through the active schema.
// Expansion cardinality is refused (not truncated) when the renderer's
// sentinel row is observed. Membership writes must affect exactly one row.
func ExecuteRelationSQL(ctx context.Context, queryer sqlx.QueryerContext, registry *schema.Registry, provider policyir.Provider, statement RelationSQLStatement) ([]mutationdecode.Row, error) {
	if ctx == nil || queryer == nil || registry == nil || statement.model == (policyir.ModelID{}) || statement.text == "" || statement.maxRows == 0 || len(statement.columns) == 0 {
		return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_INPUT: context, queryer, registry, and closed statement are required")
	}
	fields := make([]policyir.FieldID, len(statement.columns))
	for index, column := range statement.columns {
		if column.field == (policyir.FieldID{}) || column.alias == "" {
			return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_INPUT: statement column %d is invalid", index)
		}
		fields[index] = column.field
	}
	decoder, err := readdecode.NewFields(statement.model, registry, provider, fields)
	if err != nil {
		return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_DECODE: %w", err)
	}
	rows, err := queryer.QueryxContext(ctx, statement.text, statement.Args()...)
	if err != nil {
		return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_QUERY: %w", err)
	}
	defer rows.Close()
	result := make([]mutationdecode.Row, 0)
	for rows.Next() {
		if uint64(len(result)) >= uint64(statement.maxRows) {
			return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_LIMIT: statement returned more than %d rows", statement.maxRows)
		}
		scan := decoder.NewScan()
		if err := rows.Scan(scan.Destinations()...); err != nil {
			return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_SCAN: %w", err)
		}
		cells, err := scan.Decode()
		if err != nil {
			return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_DECODE: %w", err)
		}
		row, err := mutationdecode.FromReadCells(registry, statement.model, cells)
		if err != nil {
			return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_DECODE: %w", err)
		}
		complete, err := row.IsComplete(registry)
		if err != nil || !complete {
			if err == nil {
				err = fmt.Errorf("decoded relation row is incomplete")
			}
			return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_DECODE: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_QUERY: %w", err)
	}
	if statement.role == ApplyMembershipConnect || statement.role == ApplyMembershipDisconnect {
		if len(result) != 1 {
			return nil, fmt.Errorf("P4_NESTED_SQL_EXEC_CARDINALITY: membership write returned %d rows; expected 1", len(result))
		}
	}
	return result, nil
}
