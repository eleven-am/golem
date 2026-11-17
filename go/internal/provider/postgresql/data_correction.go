package postgresql

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/eleven-am/golem/go/internal/dataincarnation"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/mutation/rowversion"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/jmoiron/sqlx"
)

func postgresqlCorrectionTarget(entry migration.ManifestEntry) (physical.PhysicalTable, physical.PhysicalColumn, error) {
	plan, err := migration.ReviewedEntryPlan(entry)
	if err != nil {
		return physical.PhysicalTable{}, physical.PhysicalColumn{}, err
	}
	if !migration.IsDataCorrection(entry) || len(plan.Operations) != 1 {
		return physical.PhysicalTable{}, physical.PhysicalColumn{}, fmt.Errorf("postgresql data correction lacks exact reviewed authority")
	}
	if err := migration.ValidatePlan(plan, entry.Approvals); err != nil {
		return physical.PhysicalTable{}, physical.PhysicalColumn{}, err
	}
	operation := plan.Operations[0]
	for _, table := range entry.AfterSnapshot.Tables {
		for _, column := range table.Columns {
			if string(column.ID) != operation.ObjectID {
				continue
			}
			if len(entry.Manual) != 1 || entry.Manual[0].OperationID != operation.ID || entry.Manual[0].Postcondition != migration.DataCorrectionPostcondition(table.ID, column.ID) {
				return table, column, fmt.Errorf("postgresql data correction requires its exact generated companion postcondition")
			}
			return table, column, nil
		}
	}
	return physical.PhysicalTable{}, physical.PhysicalColumn{}, fmt.Errorf("postgresql data correction target is absent")
}

func planPostgreSQLCorrection(entry migration.ManifestEntry) (IncrementalPlan, error) {
	if _, _, err := postgresqlCorrectionTarget(entry); err != nil {
		return IncrementalPlan{}, err
	}
	return IncrementalPlan{MigrationID: entry.ID, steps: []incrementalStep{{correction: entry.Operations[0].ID}}}, nil
}

func executePostgreSQLCorrection(ctx context.Context, tx *sqlx.Tx, entry migration.ManifestEntry, files map[string][]byte) error {
	table, column, err := postgresqlCorrectionTarget(entry)
	if err != nil {
		return err
	}
	companion := entry.Manual[0]
	content, exists := files[companion.File.Path]
	if !exists || migration.Checksum(content) != companion.File.SHA256 {
		return fmt.Errorf("postgresql data correction companion is missing or rewritten")
	}
	command, err := renderPostgreSQLCorrection(content, entry.AfterSnapshot.Namespace.Name, table, column)
	if err != nil {
		return err
	}
	if err := verifyPostgreSQLCorrectionBehavior(ctx, tx, entry.BeforeSnapshot, table); err != nil {
		return err
	}
	// Restrict unqualified builtin operator/type resolution as well as author SQL.
	// No caller text enters executable SQL except through the closed renderer.
	var previousPath string
	if err := tx.GetContext(ctx, &previousPath, `SELECT pg_catalog.current_setting('search_path')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_catalog.set_config('search_path','pg_catalog,pg_temp',true)`); err != nil {
		return err
	}
	target := qualified(entry.AfterSnapshot.Namespace.Name, table.Name)
	var keys, oldKeys, join []string
	for _, field := range table.PrimaryKey.Columns {
		for _, item := range table.Columns {
			if item.ID == field {
				key := quote(item.Name)
				keys = append(keys, key)
				oldKeys = append(oldKeys, "OLD."+key)
				join = append(join, "target."+key+" = changed."+key)
			}
		}
	}
	if len(keys) != len(table.PrimaryKey.Columns) {
		return fmt.Errorf("postgresql data correction identity is unresolved")
	}
	const capture = `pg_temp."golem_correction_changed"`
	const function = `pg_temp."golem_correction_capture"`
	const trigger = `"golem_correction_capture"`
	left, right := "OLD."+quote(column.Name), "NEW."+quote(column.Name)
	if column.Storage.Kind == physical.StoragePostgreSQLText || column.Storage.Kind == physical.StoragePostgreSQLVarchar {
		left += ` COLLATE pg_catalog."C"`
		right += ` COLLATE pg_catalog."C"`
	}
	statements := []string{
		`CREATE TEMP TABLE "golem_correction_changed" ON COMMIT DROP AS SELECT ` + strings.Join(keys, ",") + ` FROM ` + target + ` WITH NO DATA`,
		`CREATE FUNCTION ` + function + `() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS ` + correctionSQLString(`BEGIN IF `+left+` IS DISTINCT FROM `+right+` THEN INSERT INTO `+capture+` (`+strings.Join(keys, ",")+`) VALUES (`+strings.Join(oldKeys, ",")+`); END IF; RETURN NEW; END;`),
		`CREATE TRIGGER ` + trigger + ` AFTER UPDATE OF ` + quote(column.Name) + ` ON ` + target + ` FOR EACH ROW EXECUTE FUNCTION ` + function + `()`,
		command,
		`DROP TRIGGER ` + trigger + ` ON ` + target,
	}
	for index, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("postgresql data correction execution failed at generated stage %d", index)
		}
	}
	if err := verifyPostgreSQLCorrectionVersions(ctx, tx, entry, table, capture, strings.Join(join, ` AND `)); err != nil {
		return err
	}
	var assignments []string
	for _, item := range table.Columns {
		if item.Name == physical.RowVersionColumn {
			next, err := rowversion.Next(policyir.ProviderPostgreSQL, entry.AfterSnapshot.System.Namespace.Name)
			if err != nil {
				return err
			}
			assignments = append(assignments, quote(item.Name)+` = `+next)
		}
		if table.OptimisticConcurrency != nil && item.ID == *table.OptimisticConcurrency {
			// PostgreSQL's bigint addition rejects overflow; no wrapping or saturation.
			assignments = append(assignments, quote(item.Name)+` = target.`+quote(item.Name)+` + 1`)
		}
	}
	if len(assignments) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE `+target+` AS target SET `+strings.Join(assignments, ",")+` FROM `+capture+` AS changed WHERE `+strings.Join(join, ` AND `)); err != nil {
			return fmt.Errorf("postgresql data correction version advancement failed")
		}
	}
	for _, statement := range []string{`DROP FUNCTION ` + function + `()`, `DROP TABLE ` + capture} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("postgresql data correction capture cleanup failed")
		}
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_catalog.set_config('search_path',$1,true)`, previousPath); err != nil {
		return err
	}
	return nil
}

func rotatePostgreSQLCorrectionIncarnation(ctx context.Context, query dataIncarnationQuery, entry migration.ManifestEntry, previous dataincarnation.Value) error {
	actual, err := readDataIncarnation(ctx, query, entry.AfterSnapshot.System)
	if err != nil {
		return err
	}
	if !previous.Valid() || actual != previous {
		return fmt.Errorf("postgresql data correction changed identity outside its owner")
	}
	next, err := dataincarnation.New()
	if err != nil {
		return err
	}
	for next == previous {
		next, err = dataincarnation.New()
		if err != nil {
			return err
		}
	}
	table, err := dataIncarnationTable(entry.AfterSnapshot.System)
	if err != nil {
		return err
	}
	result, err := query.ExecContext(ctx, `UPDATE `+table+` SET "value"=$1 WHERE "singleton"=1 AND "value"=$2`, next.String(), previous.String())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("postgresql data correction identity changed concurrently")
	}
	return nil
}

// The caller has already verified the native sequence catalog contract. Check
// its current position against every retained model version before drawing any
// values. PostgreSQL nextval is nontransactional: a later rollback can leave
// gaps, as for ordinary mutations, but must never publish a backwards stamp.
func verifyPostgreSQLCorrectionVersions(ctx context.Context, tx *sqlx.Tx, entry migration.ManifestEntry, table physical.PhysicalTable, capture, join string) error {
	var count int64
	if err := tx.GetContext(ctx, &count, `SELECT count(*) FROM `+capture); err != nil {
		return err
	}
	versioned := false
	for _, column := range table.Columns {
		versioned = versioned || column.Name == physical.RowVersionColumn
	}
	if versioned {
		var last int64
		var called bool
		if err := tx.QueryRowxContext(ctx, `SELECT last_value,is_called FROM `+qualified(entry.BeforeSnapshot.System.Namespace.Name, physical.RowVersionObject)).Scan(&last, &called); err != nil {
			return err
		}
		if last < 1 {
			return fmt.Errorf("postgresql data correction row version source is invalid")
		}
		consumed := last
		if !called {
			consumed--
		}
		if count > math.MaxInt64-consumed {
			return fmt.Errorf("postgresql data correction row version source is exhausted")
		}
		for _, retained := range entry.BeforeSnapshot.Tables {
			for _, column := range retained.Columns {
				if column.Name != physical.RowVersionColumn {
					continue
				}
				var invalid bool
				name := quote(column.Name)
				if err := tx.GetContext(ctx, &invalid, `SELECT EXISTS(SELECT 1 FROM `+qualified(entry.BeforeSnapshot.Namespace.Name, retained.Name)+` WHERE `+name+` IS NULL OR `+name+`<0 OR `+name+`>$1)`, consumed); err != nil {
					return err
				}
				if invalid {
					return fmt.Errorf("postgresql data correction row version source is behind retained versions")
				}
			}
		}
	}
	if table.OptimisticConcurrency != nil {
		for _, column := range table.Columns {
			if column.ID != *table.OptimisticConcurrency {
				continue
			}
			name := `target.` + quote(column.Name)
			var invalid bool
			if err := tx.GetContext(ctx, &invalid, `SELECT EXISTS(SELECT 1 FROM `+qualified(entry.BeforeSnapshot.Namespace.Name, table.Name)+` AS target JOIN `+capture+` AS changed ON `+join+` WHERE `+name+` IS NULL OR `+name+`<1 OR `+name+`=$1)`, int64(math.MaxInt64)); err != nil {
				return err
			}
			if invalid {
				return fmt.Errorf("postgresql data correction concurrency version is invalid or exhausted")
			}
		}
	}
	return nil
}

// Unmanaged catalog admission does not grant correction write authority. Only
// full-text triggers whose exact shape/function were verified by the preceding
// starting-catalog inspection may run on the managed correction target.
func verifyPostgreSQLCorrectionBehavior(ctx context.Context, tx *sqlx.Tx, snapshot physical.PhysicalSchema, table physical.PhysicalTable) error {
	// Stabilize target behavior until commit, including against concurrent DDL.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE `+qualified(snapshot.Namespace.Name, table.Name)+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	var oid int64
	if err := tx.GetContext(ctx, &oid, `SELECT c.oid::bigint FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, string(snapshot.Namespace.Name), string(table.Name)); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, extension := range snapshot.Extensions {
		if extension.Kind != fulltextcontract.IndexKind {
			continue
		}
		descriptor, err := fulltextstorage.Decode(extension)
		if err != nil {
			return err
		}
		if descriptor.ModelID != table.ID {
			continue
		}
		names := postgresqlFullTextNames(descriptor)
		for _, name := range []physical.PhysicalName{names.insert, names.update, names.delete, names.truncate} {
			allowed["trigger\x00"+string(name)] = true
		}
	}
	if err := rejectUnexpectedBehaviorObjects(ctx, tx, snapshot.Namespace.Name, map[int64]*physical.PhysicalTable{oid: &table}, nil, nil, map[string]map[string]bool{string(table.Name): allowed}); err != nil {
		return fmt.Errorf("postgresql data correction target behavior is outside its write authority: %w", err)
	}
	return nil
}
