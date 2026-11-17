package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

// Frozen from go/v0.8.8. No old job values are rewritten during the additive
// upgrade: priority defaults to Normal and only the claim index is replaced.
const sqliteLegacyQueueDefinition = `CREATE TABLE "golem_queue" ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BLOB NOT NULL,"status" TEXT NOT NULL,"attempt_count" INTEGER NOT NULL DEFAULT 0,"max_attempts" INTEGER NOT NULL,"available_at" INTEGER NOT NULL,"lease_token" TEXT,"lease_until" INTEGER,"resource_name" TEXT,"resource_cost" INTEGER,"resource_capacity" INTEGER,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" INTEGER,"last_code" TEXT,"enqueued_at" INTEGER NOT NULL,"finished_at" INTEGER,"updated_at" INTEGER NOT NULL)`
const sqliteLegacyQueueClaim = `CREATE INDEX "golem_queue_claim" ON "golem_queue" ("status","available_at","type")`
const sqlitePriorityColumn = `"priority" INTEGER NOT NULL DEFAULT 0 CHECK ("priority" IN (0,1))`
const sqliteResourceColumns = `"resource_name" TEXT,"resource_cost" INTEGER,"resource_capacity" INTEGER`

func (store *queueStore) InspectStorage(ctx context.Context) (queueprovider.StorageInspection, error) {
	transaction, err := store.database.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	defer transaction.Rollback()
	return inspectSQLiteQueueStorage(ctx, transaction)
}

func inspectSQLiteQueueStorage(ctx context.Context, query catalogQuerier) (queueprovider.StorageInspection, error) {
	var rows []struct {
		Type, Name, Table string
		SQL               sql.NullString
	}
	const statement = `SELECT type AS "type",name AS "name",tbl_name AS "table",sql AS "sql" FROM main.sqlite_schema WHERE tbl_name='golem_queue' OR name IN ('golem_queue','golem_queue_claim','golem_queue_dedupe','golem_queue_exclusive','golem_queue_enqueued','golem_queue_history','golem_queue_terminal') ORDER BY type,name`
	if err := query.SelectContext(ctx, &rows, statement); err != nil {
		return queueprovider.StorageInspection{}, err
	}
	if len(rows) == 0 {
		return queueprovider.StorageInspection{Version: queueprovider.StorageAbsent}, nil
	}
	result := queueprovider.StorageInspection{}
	indexes := map[string]string{}
	autoIndexes := 0
	for _, row := range rows {
		if row.Table != "golem_queue" {
			return result, queueprovider.ErrStorageUnrecognized
		}
		switch row.Type {
		case "table":
			if row.Name != "golem_queue" || result.Version != "" {
				return result, queueprovider.ErrStorageUnrecognized
			}
			actual, err := parseDDL(row.SQL.String)
			if err != nil {
				return result, queueprovider.ErrStorageUnrecognized
			}
			// v0.8.8 also upgraded earlier resource-less tables by appending
			// all three lease snapshot columns. Retain that exact emitted form.
			appendedResources := strings.TrimSuffix(strings.Replace(sqliteLegacyQueueDefinition, sqliteResourceColumns+",", "", 1), ")") + "," + sqliteResourceColumns + ")"
			current := strings.Replace(sqliteQueueSchema[0], `CREATE TABLE IF NOT EXISTS "main"."golem_queue"`, `CREATE TABLE "golem_queue"`, 1)
			for _, candidate := range []struct {
				version    queueprovider.StorageVersion
				definition string
			}{
				{queueprovider.StorageLegacyPreResources, strings.Replace(sqliteLegacyQueueDefinition, sqliteResourceColumns+",", "", 1)},
				{queueprovider.StorageLegacyV088, sqliteLegacyQueueDefinition},
				{queueprovider.StorageLegacyV088, appendedResources},
				{queueprovider.StoragePriorityV1, current},
				{queueprovider.StoragePriorityV1, strings.TrimSuffix(sqliteLegacyQueueDefinition, ")") + "," + sqlitePriorityColumn + ")"},
				{queueprovider.StoragePriorityV1, strings.TrimSuffix(appendedResources, ")") + "," + sqlitePriorityColumn + ")"},
			} {
				expected, err := parseDDL(candidate.definition)
				if err != nil {
					return result, fmt.Errorf("queue storage declaration: %w", err)
				}
				if reflect.DeepEqual(actual, expected) {
					result.Version = candidate.version
					break
				}
			}
			if result.Version == "" {
				return result, queueprovider.ErrStorageUnrecognized
			}
		case "index":
			if !row.SQL.Valid {
				if row.Name != "sqlite_autoindex_golem_queue_1" {
					return result, queueprovider.ErrStorageUnrecognized
				}
				autoIndexes++
			} else {
				indexes[row.Name] = row.SQL.String
			}
		default:
			return result, queueprovider.ErrStorageUnrecognized
		}
	}
	if result.Version == "" || autoIndexes != 1 {
		return result, queueprovider.ErrStorageUnrecognized
	}
	expected := sqliteQueueIndexDefinitions()
	if result.Version != queueprovider.StoragePriorityV1 {
		expected["golem_queue_claim"] = sqliteLegacyQueueClaim
	}
	for name, actualSQL := range indexes {
		definition, known := expected[name]
		if !known {
			return result, queueprovider.ErrStorageUnrecognized
		}
		actual, err := parseDDL(actualSQL)
		if err != nil {
			return result, queueprovider.ErrStorageUnrecognized
		}
		want, err := parseDDL(definition)
		if err != nil {
			return result, err
		}
		if !reflect.DeepEqual(actual, want) {
			return result, queueprovider.ErrStorageUnrecognized
		}
	}
	for _, name := range []string{"golem_queue_claim", "golem_queue_dedupe", "golem_queue_exclusive"} {
		if _, exists := indexes[name]; !exists {
			return result, queueprovider.ErrStorageUnrecognized
		}
	}
	history := 0
	for _, name := range []string{"golem_queue_enqueued", "golem_queue_history", "golem_queue_terminal"} {
		if _, present := indexes[name]; present {
			history++
		}
	}
	if history != 0 && history != 3 {
		return result, queueprovider.ErrStorageUnrecognized
	}
	result.HistoryIndexes = history == 3
	return result, nil
}

func (store *queueStore) UpgradeStorage(ctx context.Context) (queueprovider.StorageInspection, error) {
	connection, err := store.database.Connx(ctx)
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return queueprovider.StorageInspection{}, err
	}
	defer func() {
		cleanup, cancel := sqliteCleanupContext(ctx)
		defer cancel()
		_, _ = connection.ExecContext(cleanup, "ROLLBACK")
	}()
	before, err := inspectSQLiteQueueStorage(ctx, connection)
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	if before.Version == queueprovider.StorageAbsent {
		return queueprovider.StorageInspection{}, fmt.Errorf("QUEUE_STORAGE_ABSENT: no existing queue storage to upgrade")
	}
	if before.Version == queueprovider.StorageLegacyPreResources {
		for _, column := range strings.Split(sqliteResourceColumns, ",") {
			if _, err := connection.ExecContext(ctx, `ALTER TABLE `+sqliteQueueTable+` ADD COLUMN `+column); err != nil {
				return queueprovider.StorageInspection{}, err
			}
		}
	}
	if before.Version != queueprovider.StoragePriorityV1 {
		for _, statement := range []string{`ALTER TABLE ` + sqliteQueueTable + ` ADD COLUMN ` + sqlitePriorityColumn, `DROP INDEX "main"."golem_queue_claim"`, sqliteQueueClaimDefinition} {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return queueprovider.StorageInspection{}, err
			}
		}
	}
	after, err := inspectSQLiteQueueStorage(ctx, connection)
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	if after.Version != queueprovider.StoragePriorityV1 || after.HistoryIndexes != before.HistoryIndexes {
		return queueprovider.StorageInspection{}, queueprovider.ErrStorageUnrecognized
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return queueprovider.StorageInspection{}, err
	}
	return after, nil
}
