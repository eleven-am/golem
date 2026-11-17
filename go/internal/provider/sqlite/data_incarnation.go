package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

type dataIncarnationQuery interface {
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (*Provider) ReadDataIncarnation(ctx context.Context, database *sqlx.DB, system physical.SystemSchema) (dataincarnation.Value, error) {
	if database == nil {
		return dataincarnation.Value{}, fmt.Errorf("data incarnation database is nil")
	}
	return readDataIncarnation(ctx, database, system)
}

func dataIncarnationTable(system physical.SystemSchema) (string, error) {
	if !physical.HasDataIncarnation(system) {
		return "", fmt.Errorf("data incarnation is not declared")
	}
	if system.Namespace.Name != "main" {
		return "", fmt.Errorf("data incarnation requires SQLite main namespace")
	}
	return `"main"."_golem_data_incarnation"`, nil
}

func readDataIncarnation(ctx context.Context, query dataIncarnationQuery, system physical.SystemSchema) (dataincarnation.Value, error) {
	table, err := dataIncarnationTable(system)
	if err != nil {
		return dataincarnation.Value{}, err
	}
	rows, err := query.QueryxContext(ctx, `SELECT typeof("singleton")='integer' AND "singleton"=1, typeof("value")='text', length("value"), CASE WHEN typeof("value")='text' THEN substr("value",1,33) ELSE '' END FROM `+table+` LIMIT 2`)
	if err != nil {
		return dataincarnation.Value{}, err
	}
	defer rows.Close()
	var value dataincarnation.Value
	count := 0
	for rows.Next() {
		var singleton, textual bool
		var length int64
		var text string
		if err := rows.Scan(&singleton, &textual, &length, &text); err != nil {
			return dataincarnation.Value{}, err
		}
		count++
		if !singleton || !textual || length != 32 || count != 1 {
			return dataincarnation.Value{}, fmt.Errorf("data incarnation requires exactly one singleton row")
		}
		value, err = dataincarnation.Parse(text)
		if err != nil {
			return dataincarnation.Value{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return dataincarnation.Value{}, err
	}
	if count != 1 {
		return dataincarnation.Value{}, fmt.Errorf("data incarnation singleton is missing")
	}
	return value, nil
}

// Only a previously sealed reviewed entry can authorize initialization. This
// helper is called inside its DDL/ledger transaction, never by App admission.
func applyDataIncarnation(ctx context.Context, query dataIncarnationQuery, entry migration.ManifestEntry, previous dataincarnation.Value) error {
	if !physical.HasDataIncarnation(entry.AfterSnapshot.System) {
		return nil
	}
	if !physical.HasDataIncarnation(entry.BeforeSnapshot.System) {
		plan, err := reviewedEntryPlan(entry)
		if err != nil {
			return err
		}
		admitted := plan.Initial
		for _, operation := range plan.Operations {
			if operation.Kind == migration.AddSystemObject && operation.ObjectID == string(physical.DataIncarnationObjectIDV1) {
				admitted = true
			}
		}
		if !admitted {
			return fmt.Errorf("data incarnation initialization lacks reviewed authority")
		}
		value, err := dataincarnation.New()
		if err != nil {
			return err
		}
		table, err := dataIncarnationTable(entry.AfterSnapshot.System)
		if err != nil {
			return err
		}
		if _, err := query.ExecContext(ctx, `INSERT INTO `+table+` ("singleton","value") VALUES (1,?)`, value.String()); err != nil {
			return err
		}
		previous = value
	}
	actual, err := readDataIncarnation(ctx, query, entry.AfterSnapshot.System)
	if err != nil {
		return err
	}
	if !previous.Valid() || actual != previous {
		return fmt.Errorf("reviewed migration changed data incarnation unexpectedly")
	}
	return nil
}

func renderDataIncarnation(name physical.PhysicalName) string {
	return `CREATE TABLE ` + quote(name) + ` ("singleton" INTEGER NOT NULL PRIMARY KEY CHECK ("singleton"=1), "value" TEXT NOT NULL CHECK (typeof("value")='text' AND length("value")=32 AND "value" NOT GLOB '*[^0-9a-f]*' AND "value"<>'00000000000000000000000000000000'))`
}

func beforeDataIncarnation(ctx context.Context, query dataIncarnationQuery, entry migration.ManifestEntry) (dataincarnation.Value, error) {
	if !physical.HasDataIncarnation(entry.BeforeSnapshot.System) {
		return dataincarnation.Value{}, nil
	}
	return readDataIncarnation(ctx, query, entry.BeforeSnapshot.System)
}

// rotateDataIncarnation belongs to the caller's existing transaction. It never
// bootstraps storage and compares the exact admitted old identity before writing.
func rotateDataIncarnation(ctx context.Context, query dataIncarnationQuery, system physical.SystemSchema, previous dataincarnation.Value) (dataincarnation.Value, error) {
	actual, err := readDataIncarnation(ctx, query, system)
	if err != nil {
		return dataincarnation.Value{}, err
	}
	if !previous.Valid() || actual != previous {
		return dataincarnation.Value{}, fmt.Errorf("data incarnation changed before rotation")
	}
	var next dataincarnation.Value
	for {
		next, err = dataincarnation.New()
		if err != nil {
			return dataincarnation.Value{}, err
		}
		if next != previous {
			break
		}
	}
	table, err := dataIncarnationTable(system)
	if err != nil {
		return dataincarnation.Value{}, err
	}
	result, err := query.ExecContext(ctx, `UPDATE `+table+` SET "value"=? WHERE "singleton"=1 AND "value"=?`, next.String(), previous.String())
	if err != nil {
		return dataincarnation.Value{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return dataincarnation.Value{}, err
	}
	if count != 1 {
		return dataincarnation.Value{}, fmt.Errorf("data incarnation compare-and-swap failed")
	}
	return next, nil
}
