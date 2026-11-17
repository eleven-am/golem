package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/mutation/rowversion"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/jmoiron/sqlx"
)

const sqliteCorrectionKeys = "_golem_data_correction_keys"
const sqliteCorrectionTrigger = "_golem_data_correction_capture"
const sqliteCorrectionIndex = "_golem_data_correction_order"

type sqliteDataCorrection struct {
	operation migration.OperationID
	table     physical.PhysicalTable
	column    physical.PhysicalColumn
	companion migration.ManualCompanion
}

func planSQLiteDataCorrection(entry migration.ManifestEntry) (IncrementalPlan, error) {
	operation := entry.Operations[0]
	field := ir.FieldID(operation.ObjectID)
	model, ok := migration.BackfillOwner(entry.BeforeSnapshot, field)
	if !ok {
		return IncrementalPlan{}, fmt.Errorf("sqlite data correction owner is absent")
	}
	table := tableMap(entry.BeforeSnapshot.Tables)[model]
	column, ok := findColumnByID(table, field)
	if !ok || len(entry.Manual) != 1 || entry.Manual[0].OperationID != operation.ID || entry.Manual[0].Postcondition != migration.DataCorrectionPostcondition(model, field) {
		return IncrementalPlan{}, fmt.Errorf("sqlite data correction requires its exact reviewed companion")
	}
	correction := &sqliteDataCorrection{operation: operation.ID, table: table, column: column, companion: entry.Manual[0]}
	plan := IncrementalPlan{MigrationID: entry.ID, guards: []structuralGuard{{tableID: model, action: "data correction"}}}
	// Managed FTS triggers do not expand author SQL authority. The existing
	// provider renderer rebuilds their projection in this same transaction.
	for _, extension := range fullTextExtensions(entry.BeforeSnapshot, model) {
		drop, err := dropFullTextExtension(extension)
		if err != nil {
			return IncrementalPlan{}, err
		}
		plan.steps = append(plan.steps, migrationStep{statements: drop})
	}
	plan.steps = append(plan.steps, migrationStep{correction: correction})
	for _, extension := range fullTextExtensions(entry.AfterSnapshot, model) {
		create, err := renderFullTextExtension(extension, table)
		if err != nil {
			return IncrementalPlan{}, err
		}
		fill, err := renderFullTextBackfill(extension, table)
		if err != nil {
			return IncrementalPlan{}, err
		}
		plan.steps = append(plan.steps, migrationStep{statements: append(create, fill...)})
	}
	return plan, nil
}

func executeSQLiteDataCorrection(ctx context.Context, connection *sqlx.Conn, transaction *sqlx.Tx, entry migration.ManifestEntry, files map[string][]byte, correction sqliteDataCorrection) error {
	var collision bool
	if err := transaction.GetContext(ctx, &collision, `SELECT EXISTS(SELECT 1 FROM sqlite_temp_schema WHERE name IN (?,?,?))`, sqliteCorrectionKeys, sqliteCorrectionTrigger, sqliteCorrectionIndex); err != nil {
		return err
	}
	if collision {
		return fmt.Errorf("sqlite data correction temporary namespace is occupied")
	}
	var projected, keys, values, predicates []string
	for index, field := range correction.table.PrimaryKey.Columns {
		column, ok := findColumnByID(correction.table, field)
		if !ok {
			return fmt.Errorf("sqlite data correction primary identity is absent")
		}
		key := quote(physical.PhysicalName(fmt.Sprintf("key_%d", index)))
		keys = append(keys, key)
		projected = append(projected, quote(column.Name)+" AS "+key)
		values = append(values, "NEW."+quote(column.Name))
		predicates = append(predicates, quote(column.Name)+" IS ?")
	}
	keyColumns := strings.Join(keys, ",")
	keyTable := `"temp"."` + sqliteCorrectionKeys + `"`
	owner := `"main".` + quote(correction.table.Name)
	if _, err := transaction.ExecContext(ctx, `CREATE TEMP TABLE "`+sqliteCorrectionKeys+`" AS SELECT `+strings.Join(projected, ",")+` FROM `+owner+` WHERE 0`); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `CREATE UNIQUE INDEX "temp"."`+sqliteCorrectionIndex+`" ON "`+sqliteCorrectionKeys+`" (`+keyColumns+`)`); err != nil {
		return err
	}
	oldValue := "OLD." + quote(correction.column.Name)
	newValue := "NEW." + quote(correction.column.Name)
	changed := `typeof(` + oldValue + `)<>typeof(` + newValue + `) OR CAST(` + oldValue + ` AS BLOB) IS NOT CAST(` + newValue + ` AS BLOB)`
	capture := `CREATE TEMP TRIGGER "` + sqliteCorrectionTrigger + `" AFTER UPDATE OF ` + quote(correction.column.Name) + ` ON ` + owner + ` WHEN ` + changed + ` BEGIN INSERT INTO "` + sqliteCorrectionKeys + `" (` + keyColumns + `) VALUES (` + strings.Join(values, ",") + `); END`
	if _, err := transaction.ExecContext(ctx, capture); err != nil {
		return err
	}
	if err := executeSQLiteReviewedUpdate(ctx, connection, entry, files, correction.companion, correction.operation, correction.table.Name, correction.column.Name, sqliteCorrectionTrigger); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `DROP TRIGGER "temp"."`+sqliteCorrectionTrigger+`"`); err != nil {
		return err
	}
	var count int64
	if err := transaction.GetContext(ctx, &count, `SELECT count(*) FROM `+keyTable); err != nil {
		return err
	}
	versioned := false
	for _, column := range correction.table.Columns {
		versioned = versioned || column.Name == physical.RowVersionColumn
	}
	if versioned {
		var counter int64
		if err := transaction.GetContext(ctx, &counter, `SELECT "value" FROM "golem_row_version"`); err != nil {
			return err
		}
		if counter < 0 || count > math.MaxInt64-counter {
			return fmt.Errorf("sqlite data correction row version source is exhausted")
		}
		for _, table := range entry.BeforeSnapshot.Tables {
			for _, column := range table.Columns {
				if column.Name != physical.RowVersionColumn {
					continue
				}
				var invalid bool
				if err := transaction.GetContext(ctx, &invalid, `SELECT EXISTS(SELECT 1 FROM "main".`+quote(table.Name)+` WHERE typeof(`+quote(column.Name)+`) <> 'integer' OR `+quote(column.Name)+`<0 OR `+quote(column.Name)+`>?)`, counter); err != nil {
					return err
				}
				if invalid {
					return fmt.Errorf("sqlite data correction row version source is behind retained versions")
				}
			}
		}
	}
	var assignments []string
	if versioned {
		next, err := rowversion.Next(policyir.ProviderSQLite, entry.BeforeSnapshot.System.Namespace.Name)
		if err != nil {
			return err
		}
		assignments = append(assignments, quote(physical.RowVersionColumn)+"="+next)
	}
	var concurrency physical.PhysicalName
	if correction.table.OptimisticConcurrency != nil {
		column, ok := findColumnByID(correction.table, *correction.table.OptimisticConcurrency)
		if !ok {
			return fmt.Errorf("sqlite data correction concurrency field is absent")
		}
		concurrency = column.Name
		assignments = append(assignments, quote(concurrency)+"="+quote(concurrency)+"+1")
	}
	// Consume an indexed primary-key ordered work table, one key at a time.
	// Memory is independent of the size of the correction and values stay native.
	for remaining := count; remaining > 0; remaining-- {
		arguments := make([]any, len(keys))
		destinations := make([]any, len(keys))
		for index := range arguments {
			destinations[index] = &arguments[index]
		}
		rows, err := transaction.QueryxContext(ctx, `SELECT `+keyColumns+` FROM `+keyTable+` ORDER BY `+keyColumns+` LIMIT 1`)
		if err != nil {
			return err
		}
		if !rows.Next() {
			rows.Close()
			return fmt.Errorf("sqlite data correction captured key disappeared")
		}
		err = rows.Scan(destinations...)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		where := strings.Join(predicates, " AND ")
		if concurrency != "" {
			var version sql.NullInt64
			var integral bool
			if err := transaction.QueryRowxContext(ctx, `SELECT `+quote(concurrency)+`,typeof(`+quote(concurrency)+`)='integer' FROM `+owner+` WHERE `+where, arguments...).Scan(&version, &integral); err != nil {
				return err
			}
			if !integral || !version.Valid || version.Int64 < 1 || version.Int64 == math.MaxInt64 {
				return fmt.Errorf("sqlite data correction concurrency version is invalid or exhausted")
			}
		}
		if len(assignments) > 0 {
			result, err := transaction.ExecContext(ctx, `UPDATE `+owner+` SET `+strings.Join(assignments, ",")+` WHERE `+where, arguments...)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				return fmt.Errorf("sqlite data correction changed-key identity was lost")
			}
		}
		if versioned {
			var advanced int64
			if err := transaction.GetContext(ctx, &advanced, rowversion.Advance(`"value"`)); err != nil {
				return err
			}
		}
		keyPredicates := make([]string, len(keys))
		for index, key := range keys {
			keyPredicates[index] = key + " IS ?"
		}
		if _, err := transaction.ExecContext(ctx, `DELETE FROM `+keyTable+` WHERE `+strings.Join(keyPredicates, " AND "), arguments...); err != nil {
			return err
		}
	}
	_, err := transaction.ExecContext(ctx, `DROP TABLE `+keyTable)
	return err
}
