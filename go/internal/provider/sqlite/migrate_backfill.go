package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/jmoiron/sqlx"
	sqlite3 "github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// sqliteReviewedBackfill is the sole supported SQLite companion: an offline
// fill of one newly staged required column. The ordinary migration transaction
// continues to own the connection, rollback, rebuild, verification and ledger.
type sqliteReviewedBackfill struct {
	operation  migration.OperationID
	validation migration.OperationID
	model      ir.ModelID
	table      physical.PhysicalName
	column     physical.PhysicalColumn
	companion  migration.ManualCompanion
}

func planSQLiteReviewedBackfill(entry migration.ManifestEntry) (*sqliteReviewedBackfill, error) {
	var result *sqliteReviewedBackfill
	for _, operation := range entry.Operations {
		if operation.Kind != migration.BackfillColumn {
			continue
		}
		if result != nil {
			return nil, fmt.Errorf("sqlite migration %s supports exactly one required-column backfill", entry.ID)
		}
		field := ir.FieldID(operation.ObjectID)
		model, found := migration.BackfillOwner(entry.AfterSnapshot, field)
		before, existed := tableMap(entry.BeforeSnapshot.Tables)[model]
		after := tableMap(entry.AfterSnapshot.Tables)[model]
		column, present := findColumnByID(after, field)
		_, hadColumn := findColumnByID(before, field)
		if !found || !existed || !present || hadColumn || column.Nullable || column.Generated != nil || column.Default.Kind != physical.DefaultNone {
			return nil, fmt.Errorf("sqlite migration %s backfill %s must fill a new required stored column", entry.ID, operation.ID)
		}
		result = &sqliteReviewedBackfill{operation: operation.ID, model: model, table: before.Name, column: column}
		result.column.Nullable = true
		for _, candidate := range entry.Operations {
			if candidate.Kind == migration.ValidateConstraint && candidate.ObjectID == operation.ObjectID {
				result.validation = candidate.ID
			}
		}
		if result.validation == "" {
			return nil, fmt.Errorf("sqlite migration %s backfill %s lacks its generated postcondition", entry.ID, operation.ID)
		}
		// Raw maintenance never enters runtime semantic marking. Even when the
		// projection is introduced in this migration, do not promise a ready
		// semantic state for a field filled outside that owner.
		for _, snapshot := range []physical.PhysicalSchema{entry.BeforeSnapshot, entry.AfterSnapshot} {
			for _, extension := range snapshot.Extensions {
				if extension.Kind != semanticcontract.IndexKind {
					continue
				}
				descriptor, err := semanticstorage.Decode(extension)
				if err != nil {
					return nil, fmt.Errorf("sqlite migration %s semantic projection is invalid", entry.ID)
				}
				for _, input := range descriptor.Fields {
					if input == field {
						return nil, fmt.Errorf("sqlite migration %s backfill %s affects a semantic input and requires explicit semantic invalidation", entry.ID, operation.ID)
					}
				}
			}
		}
	}
	if result == nil {
		if len(entry.Manual) != 0 {
			return nil, fmt.Errorf("sqlite migration %s manual companions require a required-column backfill", entry.ID)
		}
		return nil, nil
	}
	if len(entry.Manual) != 1 || entry.Manual[0].OperationID != result.operation || entry.Manual[0].Postcondition != migration.BackfillPostcondition(result.model, result.column.ID) {
		return nil, fmt.Errorf("sqlite migration %s backfill %s requires exactly one companion with its generated no-NULL postcondition", entry.ID, result.operation)
	}
	result.companion = entry.Manual[0]
	return result, nil
}

func (backfill sqliteReviewedBackfill) stagingStatement() (string, error) {
	column, err := renderColumn(backfill.column, map[ir.FieldID]physical.PhysicalColumn{backfill.column.ID: backfill.column})
	if err != nil {
		return "", err
	}
	return "ALTER TABLE " + quote(backfill.table) + " ADD COLUMN " + column, nil
}

func verifySQLiteBackfillPostcondition(ctx context.Context, transaction *sqlx.Tx, id migration.MigrationID, backfill sqliteReviewedBackfill) error {
	var remaining int64
	statement := "SELECT count(*) FROM " + quote(backfill.table) + " WHERE " + quote(backfill.column.Name) + " IS NULL"
	if err := transaction.GetContext(ctx, &remaining, statement); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sqlite migration %s postcondition %s could not be evaluated", id, backfill.validation)
	}
	if remaining != 0 {
		return fmt.Errorf("sqlite migration %s postcondition %s failed: %d rows violate the generated invariant", id, backfill.validation, remaining)
	}
	return nil
}

func executeSQLiteReviewedBackfill(ctx context.Context, connection *sqlx.Conn, entry migration.ManifestEntry, files map[string][]byte, backfill sqliteReviewedBackfill) error {
	return executeSQLiteReviewedUpdate(ctx, connection, entry, files, backfill.companion, backfill.operation, backfill.table, backfill.column.Name, "")
}

// One native authorizer owns reviewed UPDATE execution for staged backfills
// and existing-field corrections. Only the correction's generated TEMP key
// capture trigger receives additional authority, never application triggers.
func executeSQLiteReviewedUpdate(ctx context.Context, connection *sqlx.Conn, entry migration.ManifestEntry, files map[string][]byte, companion migration.ManualCompanion, operation migration.OperationID, table, column physical.PhysicalName, captureTrigger string) error {
	content, exists := files[companion.File.Path]
	if !exists || migration.Checksum(content) != companion.File.SHA256 {
		return fmt.Errorf("sqlite migration %s reviewed backfill %s artifact is missing or rewritten", entry.ID, operation)
	}
	if err := migration.ValidateReviewedBackfillArtifact(content); err != nil {
		return fmt.Errorf("sqlite migration %s reviewed backfill %s artifact is invalid", entry.ID, operation)
	}
	readable := make(map[string]bool, len(entry.BeforeSnapshot.Tables))
	for _, table := range entry.BeforeSnapshot.Tables {
		readable[string(table.Name)] = true
	}
	err := connection.Raw(func(driverConnection any) (resultErr error) {
		driver, ok := driverConnection.(sqlitedriver.Conn)
		if !ok || driver.Raw().GetAutocommit() {
			return fmt.Errorf("required provider transaction is absent")
		}
		native := driver.Raw()
		previousContext := native.SetInterrupt(ctx)
		defer native.SetInterrupt(previousContext)
		functions, err := sqliteBackfillFunctions(native)
		if err != nil {
			return err
		}
		updated := false
		if err := native.SetAuthorizer(func(action sqlite3.AuthorizerActionCode, first, second, namespace, inner string) sqlite3.AuthorizerReturnCode {
			// No view/trigger may expand the reviewed target authority. Managed
			// FTS is already removed by the rebuild plan and recreated afterward.
			if inner != "" && (captureTrigger == "" || inner != captureTrigger) {
				return sqlite3.AUTH_DENY
			}
			if inner == captureTrigger && captureTrigger != "" && action == sqlite3.AUTH_INSERT && namespace == "temp" && first == sqliteCorrectionKeys {
				return sqlite3.AUTH_OK
			}
			switch action {
			case sqlite3.AUTH_SELECT, sqlite3.AUTH_RECURSIVE:
				return sqlite3.AUTH_OK
			case sqlite3.AUTH_FUNCTION:
				if functions[strings.ToLower(second)] {
					return sqlite3.AUTH_OK
				}
			case sqlite3.AUTH_READ:
				if namespace == "main" && readable[first] {
					return sqlite3.AUTH_OK
				}
			case sqlite3.AUTH_UPDATE:
				if inner == "" && namespace == "main" && first == string(table) && second == string(column) {
					updated = true
					return sqlite3.AUTH_OK
				}
			}
			return sqlite3.AUTH_DENY
		}); err != nil {
			return err
		}
		defer func() {
			if err := native.SetAuthorizer(nil); err != nil {
				resultErr = err
			}
		}()
		statement, tail, err := native.Prepare(string(content))
		if err != nil {
			return err
		}
		if statement == nil {
			return fmt.Errorf("required UPDATE is absent")
		}
		defer statement.Close()
		if strings.TrimSpace(tail) != "" || !updated || statement.ReadOnly() || statement.BindCount() != 0 || statement.ColumnCount() != 0 {
			return fmt.Errorf("required single parameter-free UPDATE is absent")
		}
		invalidMutation := false
		if captureTrigger != "" {
			native.PreUpdateHook(func(change sqlite3.PreUpdateData) {
				if change.Schema == "main" && (change.Op != sqlite3.AUTH_UPDATE || change.Table != string(table)) {
					invalidMutation = true
				}
			})
			defer native.PreUpdateHook(nil)
		}
		if statement.Step() {
			return fmt.Errorf("unexpected companion result row")
		}
		if err := statement.Err(); err != nil {
			return err
		}
		if invalidMutation {
			return fmt.Errorf("companion changed rows outside its UPDATE authority")
		}
		if native.GetAutocommit() {
			return fmt.Errorf("companion escaped the migration transaction")
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Driver/authorizer errors may include private SQL or row contents.
		return fmt.Errorf("sqlite migration %s reviewed backfill %s failed its restricted UPDATE contract", entry.ID, operation)
	}
	return nil
}

// The installed SQLite function catalog owns function safety. Require every
// overload of a name to be innocuous instead of copying a builtin allowlist or
// allowing extension functions with filesystem or other external side effects.
func sqliteBackfillFunctions(connection *sqlite3.Conn) (map[string]bool, error) {
	statement, _, err := connection.Prepare("PRAGMA function_list")
	if err != nil {
		return nil, err
	}
	defer statement.Close()
	functions := make(map[string]bool)
	for statement.Step() {
		name := strings.ToLower(statement.ColumnText(0))
		innocuous := statement.ColumnInt64(5)&int64(sqlite3.INNOCUOUS) != 0
		previous, seen := functions[name]
		functions[name] = innocuous && (!seen || previous)
	}
	return functions, statement.Err()
}
