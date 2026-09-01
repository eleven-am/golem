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
	postgresqlQueueClaim   = `(("status"='pending' AND "available_at"<=clock_timestamp()) OR ("status"='leased' AND "lease_until"<=clock_timestamp()))`
)

type queueStore struct {
	database  *sqlx.DB
	namespace physical.PhysicalName
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
		`CREATE TABLE IF NOT EXISTS ` + store.table() + ` ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BYTEA NOT NULL,"status" TEXT NOT NULL,"attempt_count" BIGINT NOT NULL DEFAULT 0,"max_attempts" BIGINT NOT NULL,"available_at" TIMESTAMPTZ NOT NULL,"lease_token" TEXT,"lease_until" TIMESTAMPTZ,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" TIMESTAMPTZ,"last_code" TEXT,"enqueued_at" TIMESTAMPTZ NOT NULL,"finished_at" TIMESTAMPTZ,"updated_at" TIMESTAMPTZ NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS "golem_queue_claim" ON ` + store.table() + ` ("status","available_at","type")`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "golem_queue_dedupe" ON ` + store.table() + ` ("dedupe_key") WHERE "status" IN ('pending','leased')`,
		`CREATE INDEX IF NOT EXISTS "golem_queue_exclusive" ON ` + store.table() + ` ("exclusive_key") WHERE "status"='leased'`,
	}
	for _, statement := range statements {
		if _, err := store.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("QUEUE_POSTGRESQL_STORE: create durable job storage: %w", err)
		}
	}
	return nil
}

func (store *queueStore) Enqueue(ctx context.Context, executor queueprovider.Executor, request queueprovider.EnqueueRequest) (string, error) {
	if err := queueprovider.ValidateEnqueue(request); err != nil {
		return "", err
	}
	if executor == nil {
		executor = store.database
	}
	var now time.Time
	if err := executor.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return "", fmt.Errorf("QUEUE_POSTGRESQL_STORE: read database time: %w", err)
	}
	result, err := executor.ExecContext(ctx, `INSERT INTO `+store.table()+` ("id","type","payload","status","attempt_count","max_attempts","available_at","dedupe_key","exclusive_key","enqueued_at","updated_at") VALUES ($1,$2,$3,'pending',0,$4,$5,$6,$7,$8,$8) ON CONFLICT DO NOTHING`,
		request.ID, request.Type, request.Payload, request.MaxAttempts, now.Add(request.Delay), optionalText(request.DedupeKey), optionalText(request.ExclusiveKey), now)
	if err != nil {
		return "", fmt.Errorf("QUEUE_POSTGRESQL_STORE: insert job: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("QUEUE_POSTGRESQL_STORE: insert result: %w", err)
	}
	if inserted == 1 {
		return request.ID, nil
	}
	if request.DedupeKey == "" {
		return "", fmt.Errorf("QUEUE_POSTGRESQL_STORE: job identity is already durable")
	}
	var existing string
	if err := executor.QueryRowContext(ctx, `SELECT "id" FROM `+store.table()+` WHERE "dedupe_key"=$1 AND "status" IN ('pending','leased')`, request.DedupeKey).Scan(&existing); err != nil {
		return "", fmt.Errorf("QUEUE_POSTGRESQL_STORE: resolve coalesced job: %w", err)
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
	transaction, err := store.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: begin claim: %w", err)
	}
	defer transaction.Rollback()
	arguments := make([]any, 0, len(options.Types)+1)
	names := make([]string, 0, len(options.Types))
	for _, name := range options.Types {
		arguments = append(arguments, name)
		names = append(names, "$"+strconv.Itoa(len(arguments)))
	}
	arguments = append(arguments, options.Limit)
	discovery := `SELECT "id","status","attempt_count","max_attempts","cancel_requested_at","exclusive_key" FROM ` + store.table() + ` AS job WHERE ` + postgresqlQueueClaim +
		` AND "type" IN (` + strings.Join(names, ",") + `)` +
		` AND ("exclusive_key" IS NULL OR NOT EXISTS (SELECT 1 FROM ` + store.table() + ` AS holder WHERE holder."exclusive_key"=job."exclusive_key" AND holder."id"<>job."id" AND holder."status"='leased' AND holder."lease_until">clock_timestamp()))` +
		` ORDER BY "available_at","id" LIMIT $` + strconv.Itoa(len(arguments)) + ` FOR UPDATE OF job SKIP LOCKED`
	var candidates []struct {
		ID              string         `db:"id"`
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
	records := make([]queueprovider.Record, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Status == string(queueprovider.StateLeased) && candidate.CancelRequested.Valid {
			result, updateErr := transaction.ExecContext(ctx, `UPDATE `+store.table()+` SET "status"='canceled',"lease_token"=NULL,"lease_until"=NULL,"last_code"=$1,"finished_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "id"=$2 AND "status"='leased' AND "lease_until"<=clock_timestamp() AND "cancel_requested_at" IS NOT NULL`,
				queueprovider.CodeCanceled, candidate.ID)
			if updateErr != nil {
				return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: cancel expired lease: %w", updateErr)
			}
			changed, resultErr := result.RowsAffected()
			if resultErr != nil {
				return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: expired cancellation result: %w", resultErr)
			}
			if changed != 1 {
				return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: expired cancellation changed %d rows", changed)
			}
			continue
		}
		if candidate.Status == string(queueprovider.StateLeased) && candidate.Attempts >= candidate.MaxAttempts {
			result, updateErr := transaction.ExecContext(ctx, `UPDATE `+store.table()+` SET "status"='failed',"lease_token"=NULL,"lease_until"=NULL,"last_code"=$1,"finished_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "id"=$2 AND "status"='leased' AND "lease_until"<=clock_timestamp() AND "attempt_count">="max_attempts"`,
				queueprovider.CodeAttemptsExhausted, candidate.ID)
			if updateErr != nil {
				return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: fail exhausted lease: %w", updateErr)
			}
			changed, resultErr := result.RowsAffected()
			if resultErr != nil {
				return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: exhausted lease result: %w", resultErr)
			}
			if changed != 1 {
				return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: exhausted lease changed %d rows", changed)
			}
			continue
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
		result, updateErr := transaction.ExecContext(ctx, `UPDATE `+store.table()+` SET "status"='leased',"attempt_count"=CASE WHEN "attempt_count"<9223372036854775807 THEN "attempt_count"+1 ELSE "attempt_count" END,"lease_token"=$1,"lease_until"=clock_timestamp()+$2*interval '1 microsecond',"updated_at"=clock_timestamp() WHERE "id"=$3 AND `+postgresqlQueueClaim,
			token, options.LeaseDuration.Microseconds(), candidate.ID)
		if updateErr != nil {
			return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: lease job: %w", updateErr)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			continue
		}
		record, readErr := store.readJob(ctx, transaction, candidate.ID)
		if readErr != nil {
			return nil, readErr
		}
		if candidate.ExclusiveKey.Valid {
			held[candidate.ExclusiveKey.String] = struct{}{}
		}
		records = append(records, record)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("QUEUE_POSTGRESQL_STORE: commit claim: %w", err)
	}
	return queueprovider.CloneRecords(records), nil
}

func (store *queueStore) holdExclusiveKey(ctx context.Context, transaction *sqlx.Tx, key, id string) (bool, error) {
	var acquired bool
	if err := transaction.GetContext(ctx, &acquired, `SELECT pg_try_advisory_xact_lock($1)`, queueAdvisoryKey(key)); err != nil {
		return false, fmt.Errorf("QUEUE_POSTGRESQL_STORE: hold exclusivity key: %w", err)
	}
	if !acquired {
		return false, nil
	}
	var free bool
	if err := transaction.GetContext(ctx, &free, `SELECT NOT EXISTS (SELECT 1 FROM `+store.table()+` WHERE "exclusive_key"=$1 AND "id"<>$2 AND "status"='leased' AND "lease_until">clock_timestamp())`, key, id); err != nil {
		return false, fmt.Errorf("QUEUE_POSTGRESQL_STORE: recheck exclusivity holder: %w", err)
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
	err := store.database.QueryRowxContext(ctx, `UPDATE `+store.table()+` SET "lease_until"=clock_timestamp()+$1*interval '1 microsecond',"updated_at"=clock_timestamp() WHERE "id"=$2 AND "lease_token"=$3 AND "status"='leased' RETURNING "cancel_requested_at"`, duration.Microseconds(), id, token).Scan(&requested)
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
	return store.fenced(ctx, `UPDATE `+store.table()+` SET "status"=$1,"lease_token"=NULL,"lease_until"=NULL,"last_code"=$2,"finished_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "id"=$3 AND "lease_token"=$4 AND "status"='leased'`, state, optionalText(code), id, token)
}

func (store *queueStore) RetryAt(ctx context.Context, id, token string, delay time.Duration, code string) (bool, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return false, err
	}
	if err := queueprovider.ValidateDelay(delay); err != nil {
		return false, err
	}
	if err := queueprovider.ValidateCode(code); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+store.table()+` SET "status"='pending',"available_at"=clock_timestamp()+$1*interval '1 microsecond',"lease_token"=NULL,"lease_until"=NULL,"last_code"=$2,"updated_at"=clock_timestamp() WHERE "id"=$3 AND "lease_token"=$4 AND "status"='leased'`, delay.Microseconds(), optionalText(code), id, token)
}

func (store *queueStore) Release(ctx context.Context, id, token string) (bool, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+store.table()+` SET "status"='pending',"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"updated_at"=clock_timestamp() WHERE "id"=$1 AND "lease_token"=$2 AND "status"='leased'`, id, token)
}

func (store *queueStore) Cancel(ctx context.Context, id string) (queueprovider.CancelResult, error) {
	if id == "" || len(id) > 64 {
		return queueprovider.CancelResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: job identity is invalid")
	}
	var typeName string
	var attempt int64
	err := store.database.QueryRowxContext(ctx, `UPDATE `+store.table()+` SET "status"='canceled',"lease_token"=NULL,"lease_until"=NULL,"last_code"='canceled',"finished_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "id"=$1 AND "status"='pending' RETURNING "type","attempt_count"`, id).Scan(&typeName, &attempt)
	if err == nil {
		return queueprovider.CancelResult{Changed: true, Terminal: true, Type: typeName, AttemptCount: attempt}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return queueprovider.CancelResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: cancel pending job: %w", err)
	}
	err = store.database.QueryRowxContext(ctx, `UPDATE `+store.table()+` SET "cancel_requested_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "id"=$1 AND "status"='leased' AND "cancel_requested_at" IS NULL RETURNING "type","attempt_count"`, id).Scan(&typeName, &attempt)
	if err == nil {
		return queueprovider.CancelResult{Changed: true, Type: typeName, AttemptCount: attempt}, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return queueprovider.CancelResult{}, nil
	}
	return queueprovider.CancelResult{}, fmt.Errorf("QUEUE_POSTGRESQL_STORE: request leased cancellation: %w", err)
}

func (store *queueStore) Requeue(ctx context.Context, id string) (bool, error) {
	if id == "" || len(id) > 64 {
		return false, fmt.Errorf("QUEUE_POSTGRESQL_STORE: job identity is invalid")
	}
	changed, err := store.fenced(ctx, `UPDATE `+store.table()+` AS job SET "status"='pending',"attempt_count"=0,"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"cancel_requested_at"=NULL,"last_code"=NULL,"finished_at"=NULL,"updated_at"=clock_timestamp() WHERE "id"=$1 AND "status" IN ('failed','canceled') AND ("dedupe_key" IS NULL OR NOT EXISTS (SELECT 1 FROM `+store.table()+` AS active WHERE active."id"<>job."id" AND active."dedupe_key"=job."dedupe_key" AND active."status" IN ('pending','leased')))`, id)
	if postgresqlDedupeConflict(err) {
		return false, nil
	}
	return changed, err
}

func (store *queueStore) RunRetention(ctx context.Context, policy queueprovider.RetentionPolicy) (int, error) {
	if err := queueprovider.ValidateRetention(policy); err != nil {
		return 0, err
	}
	result, err := store.database.ExecContext(ctx, `DELETE FROM `+store.table()+` WHERE "id" IN (SELECT "id" FROM `+store.table()+` WHERE "status" IN ('succeeded','failed','canceled') AND "finished_at"<=$1 ORDER BY "finished_at","id" LIMIT $2)`, policy.OlderThan.UTC().Truncate(time.Microsecond), policy.MaxRows)
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

func (store *queueStore) RequeueFailed(ctx context.Context, ids []string) (int, error) {
	if err := queueprovider.ValidateOperatorIDs(ids); err != nil {
		return 0, err
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	changed := 0
	for _, id := range ordered {
		requeued, err := store.fenced(ctx, `UPDATE `+store.table()+` AS job SET "status"='pending',"attempt_count"=0,"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"cancel_requested_at"=NULL,"last_code"=NULL,"finished_at"=NULL,"updated_at"=clock_timestamp() WHERE "id"=$1 AND "status"='failed' AND ("dedupe_key" IS NULL OR NOT EXISTS (SELECT 1 FROM `+store.table()+` AS active WHERE active."id"<>job."id" AND active."dedupe_key"=job."dedupe_key" AND active."status" IN ('pending','leased')))`, id)
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
	return queueprovider.Record{
		ID: row.ID, Type: row.Type, Payload: row.Payload, State: queueprovider.State(row.Status),
		AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts,
		AvailableAt: row.AvailableAt.UTC().Truncate(time.Microsecond), LeaseToken: row.LeaseToken.String,
		LeaseUntil: postgresqlOptionalTime(row.LeaseUntil), DedupeKey: row.DedupeKey.String,
		ExclusiveKey: row.ExclusiveKey.String, CancelRequested: row.CancelRequestedAt.Valid,
		LastCode: row.LastCode.String, EnqueuedAt: row.EnqueuedAt.UTC().Truncate(time.Microsecond),
		FinishedAt: postgresqlOptionalTime(row.FinishedAt), UpdatedAt: row.UpdatedAt.UTC().Truncate(time.Microsecond),
	}, nil
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
