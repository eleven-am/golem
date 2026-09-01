package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

const (
	sqliteQueueTable   = `"main"."golem_queue"`
	sqliteQueueColumns = `"id","type","payload","status","attempt_count","max_attempts","available_at","lease_token","lease_until","dedupe_key","exclusive_key","cancel_requested_at","last_code","enqueued_at","finished_at","updated_at"`
	sqliteQueueSummary = `"id","type","status","attempt_count","max_attempts","available_at","cancel_requested_at","last_code","enqueued_at","finished_at"`
	sqliteQueueClaim   = `(("status"='pending' AND "available_at"<=?) OR ("status"='leased' AND "lease_until"<=?))`
	sqliteQueueFence   = ` WHERE "id"=? AND "lease_token"=? AND "status"='leased' AND "lease_until">` + sqliteDatabaseMicros
)

var sqliteQueueSchema = []string{
	`CREATE TABLE IF NOT EXISTS ` + sqliteQueueTable + ` ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BLOB NOT NULL,"status" TEXT NOT NULL,"attempt_count" INTEGER NOT NULL DEFAULT 0,"max_attempts" INTEGER NOT NULL,"available_at" INTEGER NOT NULL,"lease_token" TEXT,"lease_until" INTEGER,"resource_name" TEXT,"resource_cost" INTEGER,"resource_capacity" INTEGER,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" INTEGER,"last_code" TEXT,"enqueued_at" INTEGER NOT NULL,"finished_at" INTEGER,"updated_at" INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS "main"."golem_queue_claim" ON "golem_queue" ("status","available_at","type")`,
	`CREATE UNIQUE INDEX IF NOT EXISTS "main"."golem_queue_dedupe" ON "golem_queue" ("dedupe_key") WHERE "status" IN ('pending','leased')`,
	`CREATE INDEX IF NOT EXISTS "main"."golem_queue_exclusive" ON "golem_queue" ("exclusive_key") WHERE "status"='leased'`,
}

type queueStore struct {
	database *sqlx.DB
}

// QueueStore binds the durable job state machine to a live SQLite database.
// The table is storage the provider owns rather than a managed physical
// object, so the store creates it idempotently and accepts no caller-authored
// SQL or physical names.
func (*Provider) QueueStore(database *sqlx.DB) (queueprovider.Store, error) {
	if database == nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: database is nil")
	}
	return &queueStore{database: database}, nil
}

func (store *queueStore) EnsureSchema(ctx context.Context) error {
	for _, statement := range sqliteQueueSchema {
		if _, err := store.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("QUEUE_SQLITE_STORE: create durable job storage: %w", err)
		}
	}
	return store.ensureQueueLeaseColumns(ctx)
}

func (store *queueStore) ensureQueueLeaseColumns(ctx context.Context) error {
	connection, err := store.database.Connx(ctx)
	if err != nil {
		return fmt.Errorf("QUEUE_SQLITE_STORE: reserve schema connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("QUEUE_SQLITE_STORE: begin queue schema upgrade: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var columns []struct {
		CID          int            `db:"cid"`
		Name         string         `db:"name"`
		Type         string         `db:"type"`
		NotNull      int            `db:"notnull"`
		DefaultValue sql.NullString `db:"dflt_value"`
		PrimaryKey   int            `db:"pk"`
	}
	if err := connection.SelectContext(ctx, &columns, `PRAGMA main.table_info("golem_queue")`); err != nil {
		return fmt.Errorf("QUEUE_SQLITE_STORE: inspect queue storage: %w", err)
	}
	present := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		present[column.Name] = struct{}{}
	}
	for _, column := range []struct{ name, declaration string }{
		{name: "resource_name", declaration: `"resource_name" TEXT`},
		{name: "resource_cost", declaration: `"resource_cost" INTEGER`},
		{name: "resource_capacity", declaration: `"resource_capacity" INTEGER`},
	} {
		if _, exists := present[column.name]; exists {
			continue
		}
		if _, err := connection.ExecContext(ctx, `ALTER TABLE `+sqliteQueueTable+` ADD COLUMN `+column.declaration); err != nil {
			return fmt.Errorf("QUEUE_SQLITE_STORE: add %s lease snapshot: %w", column.name, err)
		}
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("QUEUE_SQLITE_STORE: commit queue schema upgrade: %w", err)
	}
	committed = true
	return nil
}

func (store *queueStore) Enqueue(ctx context.Context, executor queueprovider.Executor, request queueprovider.EnqueueRequest) (string, error) {
	if err := queueprovider.ValidateEnqueue(request); err != nil {
		return "", err
	}
	if executor == nil {
		executor = store.database
	}
	var now int64
	if err := executor.QueryRowContext(ctx, "SELECT "+sqliteDatabaseMicros).Scan(&now); err != nil {
		return "", fmt.Errorf("QUEUE_SQLITE_STORE: read database time: %w", err)
	}
	result, err := executor.ExecContext(ctx, `INSERT INTO `+sqliteQueueTable+` ("id","type","payload","status","attempt_count","max_attempts","available_at","dedupe_key","exclusive_key","enqueued_at","updated_at") VALUES (?,?,?,'pending',0,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		request.ID, request.Type, request.Payload, request.MaxAttempts, now+request.Delay.Microseconds(), optionalText(request.DedupeKey), optionalText(request.ExclusiveKey), now, now)
	if err != nil {
		return "", fmt.Errorf("QUEUE_SQLITE_STORE: insert job: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("QUEUE_SQLITE_STORE: insert result: %w", err)
	}
	if inserted == 1 {
		return request.ID, nil
	}
	if request.DedupeKey == "" {
		return "", fmt.Errorf("QUEUE_SQLITE_STORE: job identity is already durable")
	}
	var existing string
	if err := executor.QueryRowContext(ctx, `SELECT "id" FROM `+sqliteQueueTable+` WHERE "dedupe_key"=? AND "status" IN ('pending','leased')`, request.DedupeKey).Scan(&existing); err != nil {
		return "", fmt.Errorf("QUEUE_SQLITE_STORE: resolve coalesced job: %w", err)
	}
	return existing, nil
}

func (store *queueStore) Claim(ctx context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
	if err := queueprovider.ValidateClaim(options); err != nil {
		return nil, err
	}
	if len(options.Types) == 0 {
		return nil, nil
	}
	connection, err := store.database.Connx(ctx)
	if err != nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: reserve connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: begin immediate claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var now int64
	if err := connection.GetContext(ctx, &now, "SELECT "+sqliteDatabaseMicros); err != nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: read database time: %w", err)
	}
	resourceUsed := int64(0)
	resourceCapacity := int64(0)
	claimTypes := options.Types
	if options.Resource != nil {
		resourceUsed, resourceCapacity, err = store.resourceUsage(ctx, connection, *options.Resource, now)
		if err != nil {
			return nil, err
		}
		claimTypes = resourceEligibleTypes(options.Types, options.Resource.Costs, resourceCapacity-resourceUsed)
	}
	arguments := []any{now, now}
	typePredicate := "0"
	if len(claimTypes) != 0 {
		typePredicate = `job."type" IN (` + placeholders(len(claimTypes)) + `)`
		for _, name := range claimTypes {
			arguments = append(arguments, name)
		}
	}
	if options.Resource != nil {
		typePredicate = `(` + typePredicate + ` OR (job."type" IN (` + placeholders(len(options.Types)) + `) AND job."status"='leased' AND job."lease_until"<=? AND (job."cancel_requested_at" IS NOT NULL OR job."attempt_count">=job."max_attempts")))`
		for _, name := range options.Types {
			arguments = append(arguments, name)
		}
		arguments = append(arguments, now)
	}
	discoveryLimit := options.Limit
	if options.Resource != nil {
		discoveryLimit = queueprovider.MaximumClaimJobs
	}
	arguments = append(arguments, now, discoveryLimit)
	discovery := `SELECT "id","type","status","attempt_count","max_attempts","cancel_requested_at","exclusive_key" FROM ` + sqliteQueueTable + ` AS job WHERE ` + sqliteQueueClaim +
		` AND ` + typePredicate +
		` AND ("exclusive_key" IS NULL OR NOT EXISTS (SELECT 1 FROM ` + sqliteQueueTable + ` AS holder WHERE holder."exclusive_key"=job."exclusive_key" AND holder."id"<>job."id" AND holder."status"='leased' AND holder."lease_until">?))` +
		` ORDER BY "available_at","id" LIMIT ?`
	var candidates []struct {
		ID              string         `db:"id"`
		Type            string         `db:"type"`
		Status          string         `db:"status"`
		Attempts        int64          `db:"attempt_count"`
		MaxAttempts     int64          `db:"max_attempts"`
		CancelRequested sql.NullInt64  `db:"cancel_requested_at"`
		ExclusiveKey    sql.NullString `db:"exclusive_key"`
	}
	if err := connection.SelectContext(ctx, &candidates, discovery, arguments...); err != nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: discover claimable jobs: %w", err)
	}
	leaseUntil := now + options.LeaseDuration.Microseconds()
	held := make(map[string]struct{}, len(candidates))
	records := make([]queueprovider.Record, 0, len(candidates))
	for _, candidate := range candidates {
		if len(records) >= options.Limit {
			break
		}
		if candidate.Status == string(queueprovider.StateLeased) && candidate.CancelRequested.Valid {
			result, updateErr := connection.ExecContext(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='canceled',"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"=?,"finished_at"=?,"updated_at"=? WHERE "id"=? AND "status"='leased' AND "lease_until"<=? AND "cancel_requested_at" IS NOT NULL`,
				queueprovider.CodeCanceled, now, now, candidate.ID, now)
			if updateErr != nil {
				return nil, fmt.Errorf("QUEUE_SQLITE_STORE: cancel expired lease: %w", updateErr)
			}
			changed, resultErr := result.RowsAffected()
			if resultErr != nil {
				return nil, fmt.Errorf("QUEUE_SQLITE_STORE: expired cancellation result: %w", resultErr)
			}
			if changed != 1 {
				return nil, fmt.Errorf("QUEUE_SQLITE_STORE: expired cancellation changed %d rows", changed)
			}
			continue
		}
		if candidate.Status == string(queueprovider.StateLeased) && candidate.Attempts >= candidate.MaxAttempts {
			result, updateErr := connection.ExecContext(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='failed',"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"=?,"finished_at"=?,"updated_at"=? WHERE "id"=? AND "status"='leased' AND "lease_until"<=? AND "attempt_count">="max_attempts"`,
				queueprovider.CodeAttemptsExhausted, now, now, candidate.ID, now)
			if updateErr != nil {
				return nil, fmt.Errorf("QUEUE_SQLITE_STORE: fail exhausted lease: %w", updateErr)
			}
			changed, resultErr := result.RowsAffected()
			if resultErr != nil {
				return nil, fmt.Errorf("QUEUE_SQLITE_STORE: exhausted lease result: %w", resultErr)
			}
			if changed != 1 {
				return nil, fmt.Errorf("QUEUE_SQLITE_STORE: exhausted lease changed %d rows", changed)
			}
			continue
		}
		if candidate.ExclusiveKey.Valid {
			if _, taken := held[candidate.ExclusiveKey.String]; taken {
				continue
			}
		}
		candidateCost := int64(0)
		if options.Resource != nil {
			candidateCost = options.Resource.Costs[candidate.Type]
			if resourceUsed >= resourceCapacity || candidateCost > resourceCapacity-resourceUsed {
				continue
			}
		}
		token, tokenErr := queueprovider.NewIdentifier()
		if tokenErr != nil {
			return nil, tokenErr
		}
		var resourceName any
		var resourceCost any
		var snapshotCapacity any
		if options.Resource != nil {
			resourceName = options.Resource.Name
			resourceCost = candidateCost
			snapshotCapacity = options.Resource.Concurrency
		}
		result, updateErr := connection.ExecContext(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='leased',"attempt_count"=CASE WHEN "attempt_count"<9223372036854775807 THEN "attempt_count"+1 ELSE "attempt_count" END,"lease_token"=?,"lease_until"=?,"resource_name"=?,"resource_cost"=?,"resource_capacity"=?,"updated_at"=? WHERE "id"=? AND `+sqliteQueueClaim,
			token, leaseUntil, resourceName, resourceCost, snapshotCapacity, now, candidate.ID, now, now)
		if updateErr != nil {
			return nil, fmt.Errorf("QUEUE_SQLITE_STORE: lease job: %w", updateErr)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			continue
		}
		record, readErr := sqliteReadJob(ctx, connection, candidate.ID)
		if readErr != nil {
			return nil, readErr
		}
		if candidate.ExclusiveKey.Valid {
			held[candidate.ExclusiveKey.String] = struct{}{}
		}
		resourceUsed += candidateCost
		records = append(records, record)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: commit claim: %w", err)
	}
	committed = true
	return queueprovider.CloneRecords(records), nil
}

func (store *queueStore) resourceUsage(ctx context.Context, connection *sqlx.Conn, resource queueprovider.ClaimResource, now int64) (int64, int64, error) {
	var rows []struct {
		Cost     sql.NullInt64 `db:"resource_cost"`
		Capacity sql.NullInt64 `db:"resource_capacity"`
		Count    int64         `db:"count"`
	}
	query := `SELECT "resource_cost","resource_capacity",COUNT(*) AS "count" FROM ` + sqliteQueueTable + ` WHERE "status"='leased' AND "lease_until">? AND "resource_name"=? GROUP BY "resource_cost","resource_capacity"`
	if err := connection.SelectContext(ctx, &rows, query, now, resource.Name); err != nil {
		return 0, 0, fmt.Errorf("QUEUE_SQLITE_STORE: read resource usage: %w", err)
	}
	used := int64(0)
	capacity := resource.Concurrency
	for _, row := range rows {
		if !row.Cost.Valid || row.Cost.Int64 <= 0 || !row.Capacity.Valid || row.Capacity.Int64 <= 0 || row.Count <= 0 {
			return 0, 0, fmt.Errorf("QUEUE_SQLITE_STORE: resource lease snapshot is invalid")
		}
		if row.Capacity.Int64 < capacity {
			capacity = row.Capacity.Int64
		}
		if used >= capacity || row.Count > (capacity-used)/row.Cost.Int64 {
			used = capacity
			continue
		}
		used += row.Count * row.Cost.Int64
	}
	return used, capacity, nil
}

func resourceEligibleTypes(types []string, costs map[string]int64, remaining int64) []string {
	eligible := make([]string, 0, len(types))
	for _, name := range types {
		if cost := costs[name]; cost <= remaining {
			eligible = append(eligible, name)
		}
	}
	return eligible
}

func (store *queueStore) Renew(ctx context.Context, id, token string, duration time.Duration) (queueprovider.Renewal, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return queueprovider.Renewal{}, err
	}
	if err := queueprovider.ValidateLease(duration); err != nil {
		return queueprovider.Renewal{}, err
	}
	var requested sql.NullInt64
	err := store.database.GetContext(ctx, &requested, `UPDATE `+sqliteQueueTable+` SET "lease_until"=`+sqliteDatabaseMicros+`+?,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND "lease_token"=? AND "status"='leased' AND "lease_until">`+sqliteDatabaseMicros+` RETURNING "cancel_requested_at"`, duration.Microseconds(), id, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return queueprovider.Renewal{}, nil
		}
		return queueprovider.Renewal{}, fmt.Errorf("QUEUE_SQLITE_STORE: renew lease: %w", err)
	}
	return queueprovider.Renewal{Renewed: true, CancelRequested: requested.Valid}, nil
}

func (store *queueStore) Succeed(ctx context.Context, id, token, code string) (bool, error) {
	return store.terminal(ctx, id, token, code, string(queueprovider.StateSucceeded))
}

func (store *queueStore) Fail(ctx context.Context, id, token, code string) (bool, error) {
	return store.terminal(ctx, id, token, code, string(queueprovider.StateFailed))
}

func (store *queueStore) MarkCanceled(ctx context.Context, id, token, code string) (bool, error) {
	return store.terminal(ctx, id, token, code, string(queueprovider.StateCanceled))
}

func (store *queueStore) terminal(ctx context.Context, id, token, code, state string) (bool, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return false, err
	}
	if err := queueprovider.ValidateCode(code); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"=?,"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"=?,"finished_at"=`+sqliteDatabaseMicros+`,"updated_at"=`+sqliteDatabaseMicros+sqliteQueueFence, state, optionalText(code), id, token)
}

func (store *queueStore) RetryAt(ctx context.Context, id, token string, delay time.Duration, code string, uncounted bool) (bool, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return false, err
	}
	if err := queueprovider.ValidateDelay(delay); err != nil {
		return false, err
	}
	if err := queueprovider.ValidateCode(code); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='pending',"attempt_count"=CASE WHEN ? AND "attempt_count">0 THEN "attempt_count"-1 ELSE "attempt_count" END,"available_at"=`+sqliteDatabaseMicros+`+?,"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"=?,"updated_at"=`+sqliteDatabaseMicros+sqliteQueueFence, uncounted, delay.Microseconds(), optionalText(code), id, token)
}

func (store *queueStore) Release(ctx context.Context, id, token string) (bool, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='pending',"available_at"=`+sqliteDatabaseMicros+`,"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"updated_at"=`+sqliteDatabaseMicros+sqliteQueueFence, id, token)
}

func (store *queueStore) Cancel(ctx context.Context, id string) (queueprovider.CancelResult, error) {
	if id == "" || len(id) > 64 {
		return queueprovider.CancelResult{}, fmt.Errorf("QUEUE_SQLITE_STORE: job identity is invalid")
	}
	var typeName string
	var attempt int64
	var state string
	err := store.database.QueryRowxContext(ctx, `UPDATE `+sqliteQueueTable+` SET "status"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN 'canceled' ELSE "status" END,"lease_token"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN NULL ELSE "lease_token" END,"lease_until"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN NULL ELSE "lease_until" END,"resource_name"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN NULL ELSE "resource_name" END,"resource_cost"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN NULL ELSE "resource_cost" END,"resource_capacity"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN NULL ELSE "resource_capacity" END,"cancel_requested_at"=CASE WHEN "status"='leased' AND "lease_until">`+sqliteDatabaseMicros+` THEN `+sqliteDatabaseMicros+` ELSE "cancel_requested_at" END,"last_code"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN 'canceled' ELSE "last_code" END,"finished_at"=CASE WHEN "status"='pending' OR "lease_until"<=`+sqliteDatabaseMicros+` THEN `+sqliteDatabaseMicros+` ELSE "finished_at" END,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND ("status"='pending' OR ("status"='leased' AND ("lease_until"<=`+sqliteDatabaseMicros+` OR "cancel_requested_at" IS NULL))) RETURNING "type","attempt_count","status"`, id).Scan(&typeName, &attempt, &state)
	if err == nil {
		return queueprovider.CancelResult{Changed: true, Terminal: state == string(queueprovider.StateCanceled), Type: typeName, AttemptCount: attempt}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return queueprovider.CancelResult{}, fmt.Errorf("QUEUE_SQLITE_STORE: cancel job: %w", err)
	}
	return queueprovider.CancelResult{}, nil
}

func (store *queueStore) CancelMany(ctx context.Context, ids []string) (queueprovider.CancelBatch, error) {
	if err := queueprovider.ValidateOperatorIDs(ids); err != nil {
		return queueprovider.CancelBatch{}, err
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	batch := queueprovider.CancelBatch{}
	for _, id := range ordered {
		result, err := store.Cancel(ctx, id)
		if err != nil {
			return batch, err
		}
		if result.Changed {
			batch.Changed++
		}
		if result.Changed && result.Terminal {
			batch.Terminal = append(batch.Terminal, result)
		}
	}
	return batch, nil
}

func (store *queueStore) Requeue(ctx context.Context, id string) (bool, error) {
	if id == "" || len(id) > 64 {
		return false, fmt.Errorf("QUEUE_SQLITE_STORE: job identity is invalid")
	}
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` AS job SET "status"='pending',"attempt_count"=0,"available_at"=`+sqliteDatabaseMicros+`,"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"cancel_requested_at"=NULL,"last_code"=NULL,"finished_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND "status" IN ('failed','canceled') AND ("dedupe_key" IS NULL OR NOT EXISTS (SELECT 1 FROM `+sqliteQueueTable+` AS active WHERE active."id"<>job."id" AND active."dedupe_key"=job."dedupe_key" AND active."status" IN ('pending','leased')))`, id)
}

func (store *queueStore) RunRetention(ctx context.Context, policy queueprovider.RetentionPolicy) (int, error) {
	if err := queueprovider.ValidateRetention(policy); err != nil {
		return 0, err
	}
	states := queueprovider.RetentionStates(policy)
	arguments := make([]any, 0, len(states)*2+3)
	for _, state := range states {
		arguments = append(arguments, string(state))
	}
	cutoff := policy.OlderThan.UTC().UnixMicro()
	arguments = append(arguments, cutoff)
	for _, state := range states {
		arguments = append(arguments, string(state))
	}
	arguments = append(arguments, cutoff, policy.MaxRows)
	stateSet := placeholders(len(states))
	result, err := store.database.ExecContext(ctx, `DELETE FROM `+sqliteQueueTable+` WHERE "status" IN (`+stateSet+`) AND "finished_at"<=? AND "id" IN (SELECT "id" FROM `+sqliteQueueTable+` WHERE "status" IN (`+stateSet+`) AND "finished_at"<=? ORDER BY "finished_at","id" LIMIT ?)`, arguments...)
	if err != nil {
		return 0, fmt.Errorf("QUEUE_SQLITE_STORE: retire terminal jobs: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("QUEUE_SQLITE_STORE: retention result: %w", err)
	}
	return int(deleted), nil
}

func (store *queueStore) Inspect(ctx context.Context, id string) (queueprovider.Record, error) {
	record, err := sqliteReadJob(ctx, store.database, id)
	if errors.Is(err, sql.ErrNoRows) {
		return queueprovider.Record{}, queueprovider.ErrNotFound
	}
	return record, err
}

func (store *queueStore) List(ctx context.Context, query queueprovider.JobQuery) (queueprovider.JobPage, error) {
	if err := queueprovider.ValidateJobQuery(query); err != nil {
		return queueprovider.JobPage{}, err
	}
	where := []string{"1=1"}
	arguments := make([]any, 0, len(query.Types)+len(query.States)+4)
	if len(query.Types) != 0 {
		where = append(where, `"type" IN (`+placeholders(len(query.Types))+`)`)
		for _, name := range query.Types {
			arguments = append(arguments, name)
		}
	}
	if len(query.States) != 0 {
		where = append(where, `"status" IN (`+placeholders(len(query.States))+`)`)
		for _, state := range query.States {
			arguments = append(arguments, string(state))
		}
	}
	if query.Before != nil {
		enqueued := query.Before.EnqueuedAt.UTC().UnixMicro()
		where = append(where, `("enqueued_at"<? OR ("enqueued_at"=? AND "id"<?))`)
		arguments = append(arguments, enqueued, enqueued, query.Before.ID)
	}
	arguments = append(arguments, query.Limit+1)
	var rows []sqliteQueueSummaryRow
	statement := `SELECT ` + sqliteQueueSummary + ` FROM ` + sqliteQueueTable + ` WHERE ` + strings.Join(where, ` AND `) + ` ORDER BY "enqueued_at" DESC,"id" DESC LIMIT ?`
	if err := store.database.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return queueprovider.JobPage{}, fmt.Errorf("QUEUE_SQLITE_STORE: list jobs: %w", err)
	}
	page := queueprovider.JobPage{More: len(rows) > query.Limit}
	if page.More {
		rows = rows[:query.Limit]
	}
	page.Jobs = make([]queueprovider.Summary, len(rows))
	for index, row := range rows {
		page.Jobs[index] = row.summary()
	}
	return page, nil
}

func (store *queueStore) ListFailed(ctx context.Context, query queueprovider.FailedQuery) (queueprovider.FailedPage, error) {
	if err := queueprovider.ValidateFailedQuery(query); err != nil {
		return queueprovider.FailedPage{}, err
	}
	where := []string{`"status"='failed'`}
	arguments := make([]any, 0, len(query.Types)+4)
	if len(query.Types) != 0 {
		where = append(where, `"type" IN (`+placeholders(len(query.Types))+`)`)
		for _, name := range query.Types {
			arguments = append(arguments, name)
		}
	}
	if query.Before != nil {
		finished := query.Before.FinishedAt.UTC().UnixMicro()
		where = append(where, `("finished_at"<? OR ("finished_at"=? AND "id"<?))`)
		arguments = append(arguments, finished, finished, query.Before.ID)
	}
	arguments = append(arguments, query.Limit+1)
	var rows []sqliteQueueSummaryRow
	statement := `SELECT ` + sqliteQueueSummary + ` FROM ` + sqliteQueueTable + ` WHERE ` + strings.Join(where, ` AND `) + ` ORDER BY "finished_at" DESC,"id" DESC LIMIT ?`
	if err := store.database.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return queueprovider.FailedPage{}, fmt.Errorf("QUEUE_SQLITE_STORE: list failed jobs: %w", err)
	}
	page := queueprovider.FailedPage{More: len(rows) > query.Limit}
	if page.More {
		rows = rows[:query.Limit]
	}
	page.Jobs = make([]queueprovider.Summary, len(rows))
	for index, row := range rows {
		page.Jobs[index] = row.summary()
	}
	return page, nil
}

func (store *queueStore) CountByState(ctx context.Context, query queueprovider.CountQuery) (queueprovider.StateCounts, error) {
	if err := queueprovider.ValidateCountQuery(query); err != nil {
		return queueprovider.StateCounts{}, err
	}
	where := []string{"1=1"}
	arguments := make([]any, 0, len(query.Types))
	if len(query.Types) != 0 {
		where = append(where, `"type" IN (`+placeholders(len(query.Types))+`)`)
		for _, name := range query.Types {
			arguments = append(arguments, name)
		}
	}
	var rows []struct {
		State string `db:"status"`
		Count int64  `db:"count"`
	}
	statement := `SELECT "status",COUNT(*) AS "count" FROM ` + sqliteQueueTable + ` WHERE ` + strings.Join(where, ` AND `) + ` GROUP BY "status"`
	if err := store.database.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return queueprovider.StateCounts{}, fmt.Errorf("QUEUE_SQLITE_STORE: count jobs by state: %w", err)
	}
	counts := queueprovider.StateCounts{}
	for _, row := range rows {
		switch queueprovider.State(row.State) {
		case queueprovider.StatePending:
			counts.Pending = row.Count
		case queueprovider.StateLeased:
			counts.Leased = row.Count
		case queueprovider.StateSucceeded:
			counts.Succeeded = row.Count
		case queueprovider.StateFailed:
			counts.Failed = row.Count
		case queueprovider.StateCanceled:
			counts.Canceled = row.Count
		default:
			return queueprovider.StateCounts{}, fmt.Errorf("QUEUE_SQLITE_STORE: stored job state is invalid")
		}
	}
	return counts, nil
}

func (store *queueStore) RequeueFailed(ctx context.Context, ids []string) (int, error) {
	if err := queueprovider.ValidateOperatorIDs(ids); err != nil {
		return 0, err
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	changed := 0
	for _, id := range ordered {
		requeued, err := store.fenced(ctx, `UPDATE `+sqliteQueueTable+` AS job SET "status"='pending',"attempt_count"=0,"available_at"=`+sqliteDatabaseMicros+`,"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"cancel_requested_at"=NULL,"last_code"=NULL,"finished_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND "status"='failed' AND ("dedupe_key" IS NULL OR NOT EXISTS (SELECT 1 FROM `+sqliteQueueTable+` AS active WHERE active."id"<>job."id" AND active."dedupe_key"=job."dedupe_key" AND active."status" IN ('pending','leased')))`, id)
		if err != nil {
			return changed, err
		}
		if requeued {
			changed++
		}
	}
	return changed, nil
}

func (store *queueStore) fenced(ctx context.Context, statement string, arguments ...any) (bool, error) {
	result, err := store.database.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return false, fmt.Errorf("QUEUE_SQLITE_STORE: fenced transition: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("QUEUE_SQLITE_STORE: transition result: %w", err)
	}
	return changed == 1, nil
}

type sqliteQueueRow struct {
	ID                string         `db:"id"`
	Type              string         `db:"type"`
	Payload           []byte         `db:"payload"`
	Status            string         `db:"status"`
	AttemptCount      int64          `db:"attempt_count"`
	MaxAttempts       int64          `db:"max_attempts"`
	AvailableAt       int64          `db:"available_at"`
	LeaseToken        sql.NullString `db:"lease_token"`
	LeaseUntil        sql.NullInt64  `db:"lease_until"`
	DedupeKey         sql.NullString `db:"dedupe_key"`
	ExclusiveKey      sql.NullString `db:"exclusive_key"`
	CancelRequestedAt sql.NullInt64  `db:"cancel_requested_at"`
	LastCode          sql.NullString `db:"last_code"`
	EnqueuedAt        int64          `db:"enqueued_at"`
	FinishedAt        sql.NullInt64  `db:"finished_at"`
	UpdatedAt         int64          `db:"updated_at"`
}

type sqliteQueueSummaryRow struct {
	ID                string         `db:"id"`
	Type              string         `db:"type"`
	Status            string         `db:"status"`
	AttemptCount      int64          `db:"attempt_count"`
	MaxAttempts       int64          `db:"max_attempts"`
	AvailableAt       int64          `db:"available_at"`
	CancelRequestedAt sql.NullInt64  `db:"cancel_requested_at"`
	LastCode          sql.NullString `db:"last_code"`
	EnqueuedAt        int64          `db:"enqueued_at"`
	FinishedAt        sql.NullInt64  `db:"finished_at"`
}

func (row sqliteQueueSummaryRow) summary() queueprovider.Summary {
	return queueprovider.Summary{
		ID: row.ID, Type: row.Type, State: queueprovider.State(row.Status),
		AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts,
		AvailableAt: time.UnixMicro(row.AvailableAt).UTC(), CancelRequested: row.CancelRequestedAt.Valid,
		LastCode: row.LastCode.String, EnqueuedAt: time.UnixMicro(row.EnqueuedAt).UTC(),
		FinishedAt: sqliteOptionalTime(row.FinishedAt),
	}
}

func sqliteReadJob(ctx context.Context, queryer sqlx.QueryerContext, id string) (queueprovider.Record, error) {
	var row sqliteQueueRow
	if err := sqlx.GetContext(ctx, queryer, &row, `SELECT `+sqliteQueueColumns+` FROM `+sqliteQueueTable+` WHERE "id"=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return queueprovider.Record{}, err
		}
		return queueprovider.Record{}, fmt.Errorf("QUEUE_SQLITE_STORE: read job: %w", err)
	}
	return queueprovider.Record{
		ID: row.ID, Type: row.Type, Payload: row.Payload, State: queueprovider.State(row.Status),
		AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts,
		AvailableAt: time.UnixMicro(row.AvailableAt).UTC(), LeaseToken: row.LeaseToken.String,
		LeaseUntil: sqliteOptionalTime(row.LeaseUntil), DedupeKey: row.DedupeKey.String,
		ExclusiveKey: row.ExclusiveKey.String, CancelRequested: row.CancelRequestedAt.Valid,
		LastCode: row.LastCode.String, EnqueuedAt: time.UnixMicro(row.EnqueuedAt).UTC(),
		FinishedAt: sqliteOptionalTime(row.FinishedAt), UpdatedAt: time.UnixMicro(row.UpdatedAt).UTC(),
	}, nil
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
