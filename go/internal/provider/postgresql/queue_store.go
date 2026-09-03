package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

const (
	postgresqlQueueColumns = `"id","type","payload","status","attempt_count","max_attempts","available_at","lease_token","lease_until","dedupe_key","exclusive_key","cancel_requested_at","last_code","enqueued_at","finished_at","updated_at"`
	postgresqlQueueSummary = `"id","type","status","attempt_count","max_attempts","available_at","cancel_requested_at","last_code","enqueued_at","finished_at"`
	postgresqlQueueClaim   = `("status" IN ('pending','leased') AND "available_at"<=clock_timestamp())`
)

type queueStore struct {
	database  *sqlx.DB
	namespace physical.PhysicalName
}

type postgresqlSelectedClaim struct {
	candidateID string
	token       string
	resource    any
	cost        any
	capacity    any
}

// QueueStore binds the durable job state machine to a live PostgreSQL database
// in the default system namespace.
func (*Provider) QueueStore(database *sqlx.DB) (queueprovider.Store, error) {
	return newQueueStore(database, "_golem")
}

// QueueStoreAt exists for generated schemas whose reviewed system namespace
// differs from the default. The namespace must already be a closed physical
// identifier; no application value can become SQL.
func (*Provider) QueueStoreAt(database *sqlx.DB, namespace physical.PhysicalName) (queueprovider.Store, error) {
	return newQueueStore(database, namespace)
}

func newQueueStore(database *sqlx.DB, namespace physical.PhysicalName) (queueprovider.Store, error) {
	if database == nil {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: database is nil")
	}
	if !eventNamespacePattern.MatchString(string(namespace)) {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: system namespace is invalid")
	}
	return &queueStore{database: database, namespace: namespace}, nil
}

func (store *queueStore) table() string { return qualified(store.namespace, "golem_queue") }

func (store *queueStore) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + store.table() + ` ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BYTEA NOT NULL,"status" TEXT NOT NULL,"attempt_count" BIGINT NOT NULL DEFAULT 0,"max_attempts" BIGINT NOT NULL,"available_at" TIMESTAMPTZ NOT NULL,"lease_token" TEXT,"lease_until" TIMESTAMPTZ,"resource_name" TEXT,"resource_cost" BIGINT,"resource_capacity" BIGINT,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" TIMESTAMPTZ,"last_code" TEXT,"enqueued_at" TIMESTAMPTZ NOT NULL,"finished_at" TIMESTAMPTZ,"updated_at" TIMESTAMPTZ NOT NULL)`,
		`ALTER TABLE ` + store.table() + ` ADD COLUMN IF NOT EXISTS "resource_name" TEXT`,
		`ALTER TABLE ` + store.table() + ` ADD COLUMN IF NOT EXISTS "resource_cost" BIGINT`,
		`ALTER TABLE ` + store.table() + ` ADD COLUMN IF NOT EXISTS "resource_capacity" BIGINT`,
		`CREATE INDEX IF NOT EXISTS "golem_queue_claim" ON ` + store.table() + ` ("status","available_at","type")`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "golem_queue_dedupe" ON ` + store.table() + ` ("dedupe_key") WHERE "status" IN ('pending','leased')`,
		`CREATE INDEX IF NOT EXISTS "golem_queue_exclusive" ON ` + store.table() + ` ("exclusive_key") WHERE "status"='leased'`,
	}
	for _, statement := range statements {
		if _, err := store.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("QUEUE_POSTGRESQL_STORE: create durable job storage: %w", err)
		}
	}
	if _, err := store.database.ExecContext(ctx, `UPDATE `+store.table()+` SET "available_at"="lease_until" WHERE "status"='leased' AND "lease_until" IS NOT NULL AND "available_at"<>"lease_until"`); err != nil {
		return fmt.Errorf("QUEUE_POSTGRESQL_STORE: align existing lease eligibility: %w", err)
	}
	return nil
}

func (store *queueStore) Enqueue(ctx context.Context, executor queueprovider.Executor, request queueprovider.EnqueueRequest) (queueprovider.EnqueueResult, error) {
	if err := queueprovider.ValidateEnqueue(request); err != nil {
		return queueprovider.EnqueueResult{}, err
	}
	if executor == nil {
		executor = store.database
	}
	var now time.Time
	if err := executor.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return queueprovider.EnqueueResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: read database time: %w", err)
	}
	if request.DedupeKey == "" {
		result, err := executor.ExecContext(ctx, `INSERT INTO `+store.table()+` ("id","type","payload","status","attempt_count","max_attempts","available_at","dedupe_key","exclusive_key","enqueued_at","updated_at") VALUES ($1,$2,$3,'pending',0,$4,$5,$6,$7,$8,$8) ON CONFLICT DO NOTHING`,
			request.ID, request.Type, request.Payload, request.MaxAttempts, now.Add(request.Delay), optionalText(request.DedupeKey), optionalText(request.ExclusiveKey), now)
		if err != nil {
			return queueprovider.EnqueueResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: insert job: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return queueprovider.EnqueueResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: insert result: %w", err)
		}
		if inserted != 1 {
			return queueprovider.EnqueueResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: job identity is already durable")
		}
		return queueprovider.EnqueueResult{ID: request.ID, State: queueprovider.StatePending, Inserted: true}, nil
	}
	var stored queueprovider.EnqueueResult
	err := executor.QueryRowContext(ctx, `INSERT INTO `+store.table()+` ("id","type","payload","status","attempt_count","max_attempts","available_at","dedupe_key","exclusive_key","enqueued_at","updated_at") VALUES ($1,$2,$3,'pending',0,$4,$5,$6,$7,$8,$8) ON CONFLICT ("dedupe_key") WHERE "status" IN ('pending','leased') DO UPDATE SET "dedupe_key"=EXCLUDED."dedupe_key" RETURNING "id","status"`,
		request.ID, request.Type, request.Payload, request.MaxAttempts, now.Add(request.Delay), optionalText(request.DedupeKey), optionalText(request.ExclusiveKey), now).Scan(&stored.ID, &stored.State)
	if err != nil {
		return queueprovider.EnqueueResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: insert or coalesce job: %w", err)
	}
	stored.Inserted = stored.ID == request.ID
	return stored, nil
}

func (store *queueStore) Claim(ctx context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
	if err := queueprovider.ValidateClaim(options); err != nil {
		return nil, err
	}
	if len(options.Types) == 0 {
		return nil, nil
	}
	transaction, err := store.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: begin claim: %w", err)
	}
	defer transaction.Rollback()
	resourceUsed := int64(0)
	resourceCapacity := int64(0)
	claimTypes := options.Types
	if options.Resource != nil {
		var acquired bool
		if err := transaction.GetContext(ctx, &acquired, `SELECT pg_try_advisory_xact_lock($1)`, queueAdvisoryKey("queue-resource\x00"+string(store.namespace)+"\x00"+options.Resource.Name)); err != nil {
			return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: hold resource budget: %w", err)
		}
		if !acquired {
			return nil, nil
		}
		resourceUsed, resourceCapacity, err = store.resourceUsage(ctx, transaction, *options.Resource)
		if err != nil {
			return nil, err
		}
		claimTypes = resourceEligibleTypes(options.Types, options.Resource.Costs, resourceCapacity-resourceUsed)
	}
	arguments := make([]any, 0, len(claimTypes)+len(options.Types)+1)
	typePredicate := "FALSE"
	if len(claimTypes) != 0 {
		names := make([]string, 0, len(claimTypes))
		for _, name := range claimTypes {
			arguments = append(arguments, name)
			names = append(names, "$"+strconv.Itoa(len(arguments)))
		}
		typePredicate = `job."type" IN (` + strings.Join(names, ",") + `)`
	}
	if options.Resource != nil {
		names := make([]string, 0, len(options.Types))
		for _, name := range options.Types {
			arguments = append(arguments, name)
			names = append(names, "$"+strconv.Itoa(len(arguments)))
		}
		typePredicate = `(` + typePredicate + ` OR (job."type" IN (` + strings.Join(names, ",") + `) AND job."status"='leased' AND job."lease_until"<=clock_timestamp() AND (job."cancel_requested_at" IS NOT NULL OR job."attempt_count">=job."max_attempts")))`
	}
	discoveryLimit := options.Limit
	if options.Resource != nil {
		discoveryLimit = queueprovider.MaximumClaimJobs
	}
	arguments = append(arguments, discoveryLimit)
	discovery := `SELECT "id","type","status","attempt_count","max_attempts","cancel_requested_at","exclusive_key" FROM ` + store.table() + ` AS job WHERE ` + postgresqlQueueClaim +
		` AND ` + typePredicate +
		` AND ("exclusive_key" IS NULL OR NOT EXISTS (SELECT 1 FROM ` + store.table() + ` AS holder WHERE holder."exclusive_key"=job."exclusive_key" AND holder."id"<>job."id" AND holder."status"='leased' AND holder."lease_until">clock_timestamp()))` +
		` ORDER BY "available_at","id" LIMIT $` + strconv.Itoa(len(arguments)) + ` FOR UPDATE OF job SKIP LOCKED`
	var candidates []struct {
		ID              string         `db:"id"`
		Type            string         `db:"type"`
		Status          string         `db:"status"`
		Attempts        int64          `db:"attempt_count"`
		MaxAttempts     int64          `db:"max_attempts"`
		CancelRequested sql.NullTime   `db:"cancel_requested_at"`
		ExclusiveKey    sql.NullString `db:"exclusive_key"`
	}
	if err := transaction.SelectContext(ctx, &candidates, discovery, arguments...); err != nil {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: discover claimable jobs: %w", err)
	}
	held := make(map[string]struct{}, len(candidates))
	selected := make([]postgresqlSelectedClaim, 0, len(candidates))
	canceled := make([]string, 0)
	exhausted := make([]string, 0)
	for _, candidate := range candidates {
		if len(selected) >= options.Limit {
			break
		}
		if candidate.Status == string(queueprovider.StateLeased) && candidate.CancelRequested.Valid {
			canceled = append(canceled, candidate.ID)
			continue
		}
		if candidate.Status == string(queueprovider.StateLeased) && candidate.Attempts >= candidate.MaxAttempts {
			exhausted = append(exhausted, candidate.ID)
			continue
		}
		candidateCost := int64(0)
		if options.Resource != nil {
			candidateCost = options.Resource.Costs[candidate.Type]
			if resourceUsed >= resourceCapacity || candidateCost > resourceCapacity-resourceUsed {
				continue
			}
		}
		if candidate.ExclusiveKey.Valid {
			if _, taken := held[candidate.ExclusiveKey.String]; taken {
				continue
			}
			exclusive, lockErr := store.holdExclusiveKey(ctx, transaction, candidate.ExclusiveKey.String, candidate.ID)
			if lockErr != nil {
				return nil, lockErr
			}
			if !exclusive {
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
		if candidate.ExclusiveKey.Valid {
			held[candidate.ExclusiveKey.String] = struct{}{}
		}
		resourceUsed += candidateCost
		selected = append(selected, postgresqlSelectedClaim{candidateID: candidate.ID, token: token, resource: resourceName, cost: resourceCost, capacity: snapshotCapacity})
	}
	if err := store.finishExpiredClaims(ctx, transaction, canceled, exhausted); err != nil {
		return nil, err
	}
	records, err := store.leaseSelected(ctx, transaction, selected, options.LeaseDuration)
	if err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: commit claim: %w", err)
	}
	return queueprovider.CloneRecords(records), nil
}

func (store *queueStore) finishExpiredClaims(ctx context.Context, transaction *sqlx.Tx, canceled, exhausted []string) error {
	for _, terminal := range []struct {
		ids       []string
		state     string
		code      string
		predicate string
	}{
		{canceled, "canceled", queueprovider.CodeCanceled, `"cancel_requested_at" IS NOT NULL`},
		{exhausted, "failed", queueprovider.CodeAttemptsExhausted, `"attempt_count">="max_attempts"`},
	} {
		if len(terminal.ids) == 0 {
			continue
		}
		marks, arguments := postgresqlArguments(terminal.ids)
		arguments = append(arguments, terminal.code)
		result, err := transaction.ExecContext(ctx, `UPDATE `+store.table()+` SET "status"='`+terminal.state+`',"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"=$`+strconv.Itoa(len(arguments))+`,"finished_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "id" IN (`+strings.Join(marks, ",")+`) AND "status"='leased' AND "lease_until"<=clock_timestamp() AND `+terminal.predicate, arguments...)
		if err != nil {
			return fmt.Errorf("QUEUE_POSTGRESQL_STORE: finalize expired leases: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || int(changed) != len(terminal.ids) {
			return fmt.Errorf("QUEUE_POSTGRESQL_STORE: expired lease ownership changed")
		}
	}
	return nil
}

func (store *queueStore) leaseSelected(ctx context.Context, transaction *sqlx.Tx, selected []postgresqlSelectedClaim, duration time.Duration) ([]queueprovider.Record, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	values := make([]string, len(selected))
	arguments := make([]any, 0, len(selected)*5+1)
	for index, claim := range selected {
		position := len(arguments) + 1
		values[index] = "($" + strconv.Itoa(position) + "::text,$" + strconv.Itoa(position+1) + "::text,$" + strconv.Itoa(position+2) + "::text,$" + strconv.Itoa(position+3) + "::bigint,$" + strconv.Itoa(position+4) + "::bigint)"
		arguments = append(arguments, claim.candidateID, claim.token, claim.resource, claim.cost, claim.capacity)
	}
	arguments = append(arguments, duration.Microseconds())
	returned := `job.` + strings.ReplaceAll(postgresqlQueueColumns, `,`, `,job.`)
	deadline := `clock_timestamp()+$` + strconv.Itoa(len(arguments)) + `*interval '1 microsecond'`
	statement := `WITH deadline AS MATERIALIZED (SELECT ` + deadline + ` AS expires_at) UPDATE ` + store.table() + ` AS job SET "status"='leased',"attempt_count"=CASE WHEN job."attempt_count"<9223372036854775807 THEN job."attempt_count"+1 ELSE job."attempt_count" END,"lease_token"=claim.token,"available_at"=deadline.expires_at,"lease_until"=deadline.expires_at,"resource_name"=claim.resource_name,"resource_cost"=claim.resource_cost,"resource_capacity"=claim.resource_capacity,"updated_at"=clock_timestamp() FROM (VALUES ` + strings.Join(values, ",") + `) AS claim(id,token,resource_name,resource_cost,resource_capacity) CROSS JOIN deadline WHERE job."id"=claim.id AND ` + postgresqlQueueClaim + ` RETURNING ` + returned
	var rows []postgresqlQueueRow
	if err := transaction.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: lease jobs: %w", err)
	}
	byID := make(map[string]queueprovider.Record, len(rows))
	for _, row := range rows {
		byID[row.ID] = row.record()
	}
	records := make([]queueprovider.Record, 0, len(selected))
	for _, claim := range selected {
		record, ok := byID[claim.candidateID]
		if !ok {
			return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: selected job lost claim eligibility")
		}
		records = append(records, record)
	}
	return records, nil
}

func (store *queueStore) resourceUsage(ctx context.Context, transaction *sqlx.Tx, resource queueprovider.ClaimResource) (int64, int64, error) {
	var rows []struct {
		Cost     sql.NullInt64 `db:"resource_cost"`
		Capacity sql.NullInt64 `db:"resource_capacity"`
		Count    int64         `db:"count"`
	}
	query := `SELECT "resource_cost","resource_capacity",COUNT(*) AS "count" FROM ` + store.table() + ` WHERE "status"='leased' AND "lease_until">clock_timestamp() AND "resource_name"=$1 GROUP BY "resource_cost","resource_capacity"`
	if err := transaction.SelectContext(ctx, &rows, query, resource.Name); err != nil {
		return 0, 0, fmt.Errorf("QUEUE_POSTGRESQL_STORE: read resource usage: %w", err)
	}
	used := int64(0)
	capacity := resource.Concurrency
	for _, row := range rows {
		if !row.Cost.Valid || row.Cost.Int64 <= 0 || !row.Capacity.Valid || row.Capacity.Int64 <= 0 || row.Count <= 0 {
			return 0, 0, fmt.Errorf("QUEUE_POSTGRESQL_STORE: resource lease snapshot is invalid")
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

func (store *queueStore) holdExclusiveKey(ctx context.Context, transaction *sqlx.Tx, key, id string) (bool, error) {
	var free bool
	if err := transaction.GetContext(ctx, &free, `SELECT CASE WHEN pg_try_advisory_xact_lock($1) THEN NOT EXISTS (SELECT 1 FROM `+store.table()+` WHERE "exclusive_key"=$2 AND "id"<>$3 AND "status"='leased' AND "lease_until">clock_timestamp()) ELSE FALSE END`, queueAdvisoryKey(key), key, id); err != nil {
		return false, fmt.Errorf("QUEUE_POSTGRESQL_STORE: hold and verify exclusivity key: %w", err)
	}
	return free, nil
}

func (store *queueStore) Renew(ctx context.Context, id, token string, duration time.Duration) (queueprovider.Renewal, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return queueprovider.Renewal{}, err
	}
	if err := queueprovider.ValidateLease(duration); err != nil {
		return queueprovider.Renewal{}, err
	}
	var requested sql.NullTime
	err := store.database.QueryRowxContext(ctx, `WITH deadline AS MATERIALIZED (SELECT clock_timestamp()+$1*interval '1 microsecond' AS expires_at) UPDATE `+store.table()+` SET "available_at"=deadline.expires_at,"lease_until"=deadline.expires_at,"updated_at"=clock_timestamp() FROM deadline WHERE "id"=$2 AND "lease_token"=$3 AND "status"='leased' AND "lease_until">clock_timestamp() RETURNING "cancel_requested_at"`, duration.Microseconds(), id, token).Scan(&requested)
	if errors.Is(err, sql.ErrNoRows) {
		return queueprovider.Renewal{}, nil
	}
	if err != nil {
		return queueprovider.Renewal{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: renew lease: %w", err)
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
	return store.fenced(ctx, `UPDATE `+store.table()+` SET "status"=$1,"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"=$2,"finished_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "id"=$3 AND "lease_token"=$4 AND "status"='leased' AND "lease_until">clock_timestamp()`, state, optionalText(code), id, token)
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
	return store.fenced(ctx, `UPDATE `+store.table()+` SET "status"='pending',"attempt_count"=CASE WHEN $1 AND "attempt_count">0 THEN "attempt_count"-1 ELSE "attempt_count" END,"available_at"=clock_timestamp()+$2*interval '1 microsecond',"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"=$3,"updated_at"=clock_timestamp() WHERE "id"=$4 AND "lease_token"=$5 AND "status"='leased' AND "lease_until">clock_timestamp()`, uncounted, delay.Microseconds(), optionalText(code), id, token)
}

func (store *queueStore) Release(ctx context.Context, id, token string) (bool, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+store.table()+` SET "status"='pending',"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"updated_at"=clock_timestamp() WHERE "id"=$1 AND "lease_token"=$2 AND "status"='leased' AND "lease_until">clock_timestamp()`, id, token)
}

func (store *queueStore) Cancel(ctx context.Context, id string) (queueprovider.CancelResult, error) {
	if err := queueprovider.ValidateJobIdentity(id); err != nil {
		return queueprovider.CancelResult{}, err
	}
	var typeName string
	var attempt int64
	var state string
	err := store.database.QueryRowxContext(ctx, `WITH target AS MATERIALIZED (SELECT "id" FROM `+store.table()+` WHERE "id"=$1 FOR UPDATE), moment AS MATERIALIZED (SELECT clock_timestamp() AS "now" FROM target) UPDATE `+store.table()+` AS job SET "status"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN 'canceled' ELSE job."status" END,"lease_token"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."lease_token" END,"lease_until"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."lease_until" END,"resource_name"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."resource_name" END,"resource_cost"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."resource_cost" END,"resource_capacity"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."resource_capacity" END,"cancel_requested_at"=CASE WHEN job."status"='leased' AND job."lease_until">moment."now" THEN moment."now" ELSE job."cancel_requested_at" END,"last_code"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN 'canceled' ELSE job."last_code" END,"finished_at"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN moment."now" ELSE job."finished_at" END,"updated_at"=moment."now" FROM target,moment WHERE job."id"=target."id" AND (job."status"='pending' OR (job."status"='leased' AND (job."lease_until"<=moment."now" OR job."cancel_requested_at" IS NULL))) RETURNING job."type",job."attempt_count",job."status"`, id).Scan(&typeName, &attempt, &state)
	if err == nil {
		return queueprovider.CancelResult{Changed: true, Terminal: state == string(queueprovider.StateCanceled), Type: typeName, AttemptCount: attempt}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return queueprovider.CancelResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: cancel job: %w", err)
	}
	return queueprovider.CancelResult{}, nil
}

func (store *queueStore) CancelMany(ctx context.Context, ids []string) (queueprovider.CancelBatch, error) {
	if err := queueprovider.ValidateOperatorIDs(ids); err != nil {
		return queueprovider.CancelBatch{}, err
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return queueprovider.CancelBatch{}, nil
	}
	parameters := make([]string, len(ordered))
	arguments := make([]any, len(ordered))
	for index, id := range ordered {
		parameters[index] = "$" + strconv.Itoa(index+1)
		arguments[index] = id
	}
	var rows []struct {
		ID      string `db:"id"`
		Type    string `db:"type"`
		Attempt int64  `db:"attempt_count"`
		State   string `db:"status"`
	}
	statement := `WITH target AS MATERIALIZED (SELECT "id" FROM ` + store.table() + ` WHERE "id" IN (` + strings.Join(parameters, ",") + `) FOR UPDATE), moment AS MATERIALIZED (SELECT clock_timestamp() AS "now" FROM target LIMIT 1) UPDATE ` + store.table() + ` AS job SET "status"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN 'canceled' ELSE job."status" END,"lease_token"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."lease_token" END,"lease_until"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."lease_until" END,"resource_name"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."resource_name" END,"resource_cost"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."resource_cost" END,"resource_capacity"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN NULL ELSE job."resource_capacity" END,"cancel_requested_at"=CASE WHEN job."status"='leased' AND job."lease_until">moment."now" THEN moment."now" ELSE job."cancel_requested_at" END,"last_code"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN 'canceled' ELSE job."last_code" END,"finished_at"=CASE WHEN job."status"='pending' OR job."lease_until"<=moment."now" THEN moment."now" ELSE job."finished_at" END,"updated_at"=moment."now" FROM target,moment WHERE job."id"=target."id" AND (job."status"='pending' OR (job."status"='leased' AND (job."lease_until"<=moment."now" OR job."cancel_requested_at" IS NULL))) RETURNING job."id",job."type",job."attempt_count",job."status"`
	if err := store.database.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return queueprovider.CancelBatch{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: cancel jobs: %w", err)
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].ID < rows[right].ID })
	batch := queueprovider.CancelBatch{Changed: len(rows)}
	for _, row := range rows {
		if row.State == string(queueprovider.StateCanceled) {
			batch.Terminal = append(batch.Terminal, queueprovider.CancelResult{Changed: true, Terminal: true, Type: row.Type, AttemptCount: row.Attempt})
		}
	}
	return batch, nil
}

func (store *queueStore) Requeue(ctx context.Context, id string) (bool, error) {
	if err := queueprovider.ValidateJobIdentity(id); err != nil {
		return false, err
	}
	changed, err := store.fenced(ctx, `UPDATE `+store.table()+` AS job SET "status"='pending',"attempt_count"=0,"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"cancel_requested_at"=NULL,"last_code"=NULL,"finished_at"=NULL,"updated_at"=clock_timestamp() WHERE "id"=$1 AND "status" IN ('failed','canceled') AND ("dedupe_key" IS NULL OR NOT EXISTS (SELECT 1 FROM `+store.table()+` AS active WHERE active."id"<>job."id" AND active."dedupe_key"=job."dedupe_key" AND active."status" IN ('pending','leased')))`, id)
	if postgresqlDedupeConflict(err) {
		return false, nil
	}
	return changed, err
}

func (store *queueStore) RunRetention(ctx context.Context, policy queueprovider.RetentionPolicy) (int, error) {
	if err := queueprovider.ValidateRetention(policy); err != nil {
		return 0, err
	}
	states := queueprovider.RetentionStates(policy)
	arguments := []any{policy.OlderThan.UTC().Truncate(time.Microsecond), policy.MaxRows}
	parameters := make([]string, len(states))
	for index, state := range states {
		arguments = append(arguments, string(state))
		parameters[index] = "$" + strconv.Itoa(len(arguments))
	}
	stateSet := strings.Join(parameters, ",")
	result, err := store.database.ExecContext(ctx, `DELETE FROM `+store.table()+` WHERE "status" IN (`+stateSet+`) AND "finished_at"<=$1 AND "id" IN (SELECT "id" FROM `+store.table()+` WHERE "status" IN (`+stateSet+`) AND "finished_at"<=$1 ORDER BY "finished_at","id" LIMIT $2)`, arguments...)
	if err != nil {
		return 0, fmt.Errorf("QUEUE_POSTGRESQL_STORE: retire terminal jobs: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("QUEUE_POSTGRESQL_STORE: retention result: %w", err)
	}
	return int(deleted), nil
}

func (store *queueStore) Inspect(ctx context.Context, id string) (queueprovider.Record, error) {
	record, err := store.readJob(ctx, store.database, id)
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
		parameters := make([]string, len(query.Types))
		for index, name := range query.Types {
			arguments = append(arguments, name)
			parameters[index] = "$" + strconv.Itoa(len(arguments))
		}
		where = append(where, `"type" IN (`+strings.Join(parameters, ",")+`)`)
	}
	if len(query.States) != 0 {
		parameters := make([]string, len(query.States))
		for index, state := range query.States {
			arguments = append(arguments, string(state))
			parameters[index] = "$" + strconv.Itoa(len(arguments))
		}
		where = append(where, `"status" IN (`+strings.Join(parameters, ",")+`)`)
	}
	if query.Before != nil {
		enqueued := query.Before.EnqueuedAt.UTC().Truncate(time.Microsecond)
		arguments = append(arguments, enqueued, enqueued, query.Before.ID)
		first := len(arguments) - 2
		where = append(where, `("enqueued_at"<$`+strconv.Itoa(first)+` OR ("enqueued_at"=$`+strconv.Itoa(first+1)+` AND "id"<$`+strconv.Itoa(first+2)+`))`)
	}
	arguments = append(arguments, query.Limit+1)
	statement := `SELECT ` + postgresqlQueueSummary + ` FROM ` + store.table() + ` WHERE ` + strings.Join(where, ` AND `) + ` ORDER BY "enqueued_at" DESC,"id" DESC LIMIT $` + strconv.Itoa(len(arguments))
	var rows []postgresqlQueueSummaryRow
	if err := store.database.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return queueprovider.JobPage{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: list jobs: %w", err)
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
		parameters := make([]string, len(query.Types))
		for index, name := range query.Types {
			arguments = append(arguments, name)
			parameters[index] = "$" + strconv.Itoa(len(arguments))
		}
		where = append(where, `"type" IN (`+strings.Join(parameters, ",")+`)`)
	}
	if query.Before != nil {
		finished := query.Before.FinishedAt.UTC().Truncate(time.Microsecond)
		arguments = append(arguments, finished, finished, query.Before.ID)
		first := len(arguments) - 2
		where = append(where, `("finished_at"<$`+strconv.Itoa(first)+` OR ("finished_at"=$`+strconv.Itoa(first+1)+` AND "id"<$`+strconv.Itoa(first+2)+`))`)
	}
	arguments = append(arguments, query.Limit+1)
	statement := `SELECT ` + postgresqlQueueSummary + ` FROM ` + store.table() + ` WHERE ` + strings.Join(where, ` AND `) + ` ORDER BY "finished_at" DESC,"id" DESC LIMIT $` + strconv.Itoa(len(arguments))
	var rows []postgresqlQueueSummaryRow
	if err := store.database.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return queueprovider.FailedPage{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: list failed jobs: %w", err)
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
		parameters := make([]string, len(query.Types))
		for index, name := range query.Types {
			arguments = append(arguments, name)
			parameters[index] = "$" + strconv.Itoa(len(arguments))
		}
		where = append(where, `"type" IN (`+strings.Join(parameters, ",")+`)`)
	}
	var rows []struct {
		State string `db:"status"`
		Count int64  `db:"count"`
	}
	statement := `SELECT "status",COUNT(*) AS "count" FROM ` + store.table() + ` WHERE ` + strings.Join(where, ` AND `) + ` GROUP BY "status"`
	if err := store.database.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return queueprovider.StateCounts{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: count jobs by state: %w", err)
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
			return queueprovider.StateCounts{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: stored job state is invalid")
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
		requeued, err := store.fenced(ctx, `UPDATE `+store.table()+` AS job SET "status"='pending',"attempt_count"=0,"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"cancel_requested_at"=NULL,"last_code"=NULL,"finished_at"=NULL,"updated_at"=clock_timestamp() WHERE "id"=$1 AND "status"='failed' AND ("dedupe_key" IS NULL OR NOT EXISTS (SELECT 1 FROM `+store.table()+` AS active WHERE active."id"<>job."id" AND active."dedupe_key"=job."dedupe_key" AND active."status" IN ('pending','leased')))`, id)
		if postgresqlDedupeConflict(err) {
			continue
		}
		if err != nil {
			return changed, err
		}
		if requeued {
			changed++
		}
	}
	return changed, nil
}

func postgresqlDedupeConflict(err error) bool {
	var failure *pgconn.PgError
	return errors.As(err, &failure) && failure.Code == "23505" && failure.ConstraintName == "golem_queue_dedupe"
}

func (store *queueStore) fenced(ctx context.Context, statement string, arguments ...any) (bool, error) {
	result, err := store.database.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return false, fmt.Errorf("QUEUE_POSTGRESQL_STORE: fenced transition: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("QUEUE_POSTGRESQL_STORE: transition result: %w", err)
	}
	return changed == 1, nil
}

type postgresqlQueueRow struct {
	ID                string         `db:"id"`
	Type              string         `db:"type"`
	Payload           []byte         `db:"payload"`
	Status            string         `db:"status"`
	AttemptCount      int64          `db:"attempt_count"`
	MaxAttempts       int64          `db:"max_attempts"`
	AvailableAt       time.Time      `db:"available_at"`
	LeaseToken        sql.NullString `db:"lease_token"`
	LeaseUntil        sql.NullTime   `db:"lease_until"`
	DedupeKey         sql.NullString `db:"dedupe_key"`
	ExclusiveKey      sql.NullString `db:"exclusive_key"`
	CancelRequestedAt sql.NullTime   `db:"cancel_requested_at"`
	LastCode          sql.NullString `db:"last_code"`
	EnqueuedAt        time.Time      `db:"enqueued_at"`
	FinishedAt        sql.NullTime   `db:"finished_at"`
	UpdatedAt         time.Time      `db:"updated_at"`
}

type postgresqlQueueSummaryRow struct {
	ID                string         `db:"id"`
	Type              string         `db:"type"`
	Status            string         `db:"status"`
	AttemptCount      int64          `db:"attempt_count"`
	MaxAttempts       int64          `db:"max_attempts"`
	AvailableAt       time.Time      `db:"available_at"`
	CancelRequestedAt sql.NullTime   `db:"cancel_requested_at"`
	LastCode          sql.NullString `db:"last_code"`
	EnqueuedAt        time.Time      `db:"enqueued_at"`
	FinishedAt        sql.NullTime   `db:"finished_at"`
}

func (row postgresqlQueueRow) record() queueprovider.Record {
	return queueprovider.Record{
		ID: row.ID, Type: row.Type, Payload: append([]byte(nil), row.Payload...), State: queueprovider.State(row.Status),
		AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts,
		AvailableAt: row.AvailableAt.UTC().Truncate(time.Microsecond), LeaseToken: row.LeaseToken.String,
		LeaseUntil: postgresqlOptionalTime(row.LeaseUntil), DedupeKey: row.DedupeKey.String,
		ExclusiveKey: row.ExclusiveKey.String, CancelRequested: row.CancelRequestedAt.Valid,
		LastCode: row.LastCode.String, EnqueuedAt: row.EnqueuedAt.UTC().Truncate(time.Microsecond),
		FinishedAt: postgresqlOptionalTime(row.FinishedAt), UpdatedAt: row.UpdatedAt.UTC().Truncate(time.Microsecond),
	}
}

func (row postgresqlQueueSummaryRow) summary() queueprovider.Summary {
	return queueprovider.Summary{
		ID: row.ID, Type: row.Type, State: queueprovider.State(row.Status),
		AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts,
		AvailableAt: row.AvailableAt.UTC().Truncate(time.Microsecond), CancelRequested: row.CancelRequestedAt.Valid,
		LastCode: row.LastCode.String, EnqueuedAt: row.EnqueuedAt.UTC().Truncate(time.Microsecond),
		FinishedAt: postgresqlOptionalTime(row.FinishedAt),
	}
}

func (store *queueStore) readJob(ctx context.Context, queryer sqlx.QueryerContext, id string) (queueprovider.Record, error) {
	var row postgresqlQueueRow
	if err := sqlx.GetContext(ctx, queryer, &row, `SELECT `+postgresqlQueueColumns+` FROM `+store.table()+` WHERE "id"=$1`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return queueprovider.Record{}, err
		}
		return queueprovider.Record{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: read job: %w", err)
	}
	return row.record(), nil
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func queueAdvisoryKey(value string) int64 {
	digest := fnv.New64a()
	_, _ = digest.Write([]byte(value))
	return int64(digest.Sum64())
}
