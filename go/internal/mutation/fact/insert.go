package fact

import (
	"fmt"
	"strings"
	"time"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

const OutboxColumnCount = 13

var outboxColumns = [...]string{
	"event_id",
	"fact_version",
	"codec_identity",
	"generation_fingerprint",
	"model_id",
	"action",
	"before_identity",
	"after_identity",
	"causation_id",
	"transaction_ordinal",
	"metadata",
	"delete_snapshot",
	"recorded_at",
}

// InsertStatement is one closed, fully-bound _golem_outbox insertion. SQL text
// is derived only from the provider and row count; no fact value is ever
// interpolated into it.
type InsertStatement struct {
	sql  string
	args []any
}

func (statement InsertStatement) SQL() string { return statement.sql }

func (statement InsertStatement) Args() []any {
	result := make([]any, len(statement.args))
	for index, value := range statement.args {
		result[index] = cloneInsertValue(value)
	}
	return result
}

// RenderInserts deterministically chunks immutable outbox rows under the
// configured statement-parameter limit. The original rows and their byte
// slices are never retained by the returned program.
func RenderInserts(provider policyir.Provider, rows []OutboxRow, maxParameters int) ([]InsertStatement, error) {
	namespace := "main"
	if provider == policyir.ProviderPostgreSQL {
		namespace = "_golem"
	}
	return RenderInsertsAt(provider, namespace, rows, maxParameters)
}

func RenderInsertsAt(provider policyir.Provider, namespace string, rows []OutboxRow, maxParameters int) ([]InsertStatement, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	if provider != policyir.ProviderSQLite && provider != policyir.ProviderPostgreSQL {
		return nil, fmt.Errorf("P4_FACT_INSERT: unsupported provider %d", provider)
	}
	if maxParameters < OutboxColumnCount {
		return nil, fmt.Errorf("P4_FACT_INSERT: statement parameter limit %d is below one outbox row", maxParameters)
	}
	rowsPerStatement := maxParameters / OutboxColumnCount
	statements := make([]InsertStatement, 0, (len(rows)+rowsPerStatement-1)/rowsPerStatement)
	for start := 0; start < len(rows); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(rows) {
			end = len(rows)
		}
		statement, err := renderInsert(provider, namespace, rows[start:end])
		if err != nil {
			return nil, fmt.Errorf("P4_FACT_INSERT: row %d: %w", start, err)
		}
		statements = append(statements, statement)
	}
	return statements, nil
}

func renderInsert(provider policyir.Provider, namespace string, rows []OutboxRow) (InsertStatement, error) {
	if namespace == "" || strings.ContainsAny(namespace, "\x00\"") {
		return InsertStatement{}, fmt.Errorf("invalid system namespace")
	}
	var sql strings.Builder
	sql.WriteString(`INSERT INTO "`)
	sql.WriteString(namespace)
	sql.WriteString(`"."_golem_outbox" (`)
	for index, column := range outboxColumns {
		if index != 0 {
			sql.WriteString(", ")
		}
		sql.WriteByte('"')
		sql.WriteString(column)
		sql.WriteByte('"')
	}
	sql.WriteString(") VALUES ")
	args := make([]any, 0, len(rows)*OutboxColumnCount)
	parameter := 1
	for rowIndex, row := range rows {
		if err := validateOutboxRow(row); err != nil {
			return InsertStatement{}, err
		}
		if rowIndex != 0 {
			sql.WriteString(", ")
		}
		sql.WriteByte('(')
		for column := 0; column < OutboxColumnCount; column++ {
			if column != 0 {
				sql.WriteString(", ")
			}
			if provider == policyir.ProviderPostgreSQL {
				fmt.Fprintf(&sql, "$%d", parameter)
			} else {
				sql.WriteByte('?')
			}
			parameter++
		}
		sql.WriteByte(')')
		args = append(args,
			row.EventID,
			row.FactVersion,
			row.CodecIdentity,
			row.GenerationFingerprint,
			row.ModelID,
			row.Action,
			cloneBytesOrNil(row.BeforeIdentity),
			cloneBytesOrNil(row.AfterIdentity),
			row.CausationID,
			row.TransactionOrdinal,
			cloneBytesOrNil(row.Metadata),
			cloneBytesOrNil(row.DeleteSnapshot),
			providerRecordedAt(provider, row.RecordedAt),
		)
	}
	return InsertStatement{sql: sql.String(), args: args}, nil
}

func validateOutboxRow(row OutboxRow) error {
	if row.EventID == "" || row.CausationID == "" || row.GenerationFingerprint == "" || row.ModelID == "" || row.CodecIdentity == "" {
		return fmt.Errorf("required identity is empty")
	}
	if row.FactVersion != int64(FormatVersion) || row.CodecIdentity != CodecIdentity {
		return fmt.Errorf("unsupported fact codec %q version %d", row.CodecIdentity, row.FactVersion)
	}
	if row.Action != "created" && row.Action != "updated" && row.Action != "deleted" {
		return fmt.Errorf("invalid action %q", row.Action)
	}
	if row.TransactionOrdinal <= 0 {
		return fmt.Errorf("transaction ordinal must be positive")
	}
	if len(row.Metadata) == 0 {
		return fmt.Errorf("metadata is empty")
	}
	if row.RecordedAt.IsZero() {
		return fmt.Errorf("recorded time is zero")
	}
	return nil
}

func providerRecordedAt(provider policyir.Provider, value time.Time) any {
	value = value.UTC().Truncate(time.Microsecond)
	if provider == policyir.ProviderSQLite {
		return value.UnixMicro()
	}
	return value
}

func cloneInsertValue(value any) any {
	if binary, ok := value.([]byte); ok {
		return cloneBytesOrNil(binary)
	}
	return value
}

func cloneBytesOrNil(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}
