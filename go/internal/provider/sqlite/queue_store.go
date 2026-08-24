package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

const (
	sqliteQueueTable   = `"main"."golem_queue"`
	sqliteQueueColumns = `"id","type","payload","status","attempt_count","max_attempts","available_at","lease_token","lease_until","dedupe_key","exclusive_key","cancel_requested_at","last_code","enqueued_at","finished_at","updated_at"`
	sqliteQueueClaim   = `(("status"='pending' AND "available_at"<=?) OR ("status"='leased' AND "lease_until"<=?))`
	sqliteQueueFence   = ` WHERE "id"=? AND "lease_token"=? AND "status"='leased'`
)

var sqliteQueueSchema = []string{
	`CREATE TABLE IF NOT EXISTS ` + sqliteQueueTable + ` ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BLOB NOT NULL,"status" TEXT NOT NULL,"attempt_count" INTEGER NOT NULL DEFAULT 0,"max_attempts" INTEGER NOT NULL,"available_at" INTEGER NOT NULL,"lease_token" TEXT,"lease_until" INTEGER,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" INTEGER,"last_code" TEXT,"enqueued_at" INTEGER NOT NULL,"finished_at" INTEGER,"updated_at" INTEGER NOT NULL)`,
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
	arguments := []any{now, now}
	for _, name := range options.Types {
		arguments = append(arguments, name)
	}
	arguments = append(arguments, now, options.Limit)
	discovery := `SELECT "id","exclusive_key" FROM ` + sqliteQueueTable + ` AS job WHERE ` + sqliteQueueClaim +
		` AND "type" IN (` + placeholders(len(options.Types)) + `)` +
		` AND ("exclusive_key" IS NULL OR NOT EXISTS (SELECT 1 FROM ` + sqliteQueueTable + ` AS holder WHERE holder."exclusive_key"=job."exclusive_key" AND holder."id"<>job."id" AND holder."status"='leased' AND holder."lease_until">?))` +
		` ORDER BY "available_at","id" LIMIT ?`
	var candidates []struct {
		ID           string         `db:"id"`
		ExclusiveKey sql.NullString `db:"exclusive_key"`
	}
	if err := connection.SelectContext(ctx, &candidates, discovery, arguments...); err != nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: discover claimable jobs: %w", err)
	}
	leaseUntil := now + options.LeaseDuration.Microseconds()
	held := make(map[string]struct{}, len(candidates))
	records := make([]queueprovider.Record, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ExclusiveKey.Valid {
			if _, taken := held[candidate.ExclusiveKey.String]; taken {
				continue
			}
		}
		token, tokenErr := queueprovider.NewIdentifier()
		if tokenErr != nil {
			return nil, tokenErr
		}
		result, updateErr := connection.ExecContext(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='leased',"attempt_count"=CASE WHEN "attempt_count"<9223372036854775807 THEN "attempt_count"+1 ELSE "attempt_count" END,"lease_token"=?,"lease_until"=?,"updated_at"=? WHERE "id"=? AND `+sqliteQueueClaim,
			token, leaseUntil, now, candidate.ID, now, now)
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
		records = append(records, record)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("QUEUE_SQLITE_STORE: commit claim: %w", err)
	}
	committed = true
	return queueprovider.CloneRecords(records), nil
}

func (store *queueStore) Renew(ctx context.Context, id, token string, duration time.Duration) (queueprovider.Renewal, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return queueprovider.Renewal{}, err
	}
	if err := queueprovider.ValidateLease(duration); err != nil {
		return queueprovider.Renewal{}, err
	}
	changed, err := store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "lease_until"=`+sqliteDatabaseMicros+`+?,"updated_at"=`+sqliteDatabaseMicros+sqliteQueueFence, duration.Microseconds(), id, token)
	if err != nil || !changed {
		return queueprovider.Renewal{}, err
	}
	var requested sql.NullInt64
	if err := store.database.GetContext(ctx, &requested, `SELECT "cancel_requested_at" FROM `+sqliteQueueTable+` WHERE "id"=? AND "lease_token"=?`, id, token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return queueprovider.Renewal{}, nil
		}
		return queueprovider.Renewal{}, fmt.Errorf("QUEUE_SQLITE_STORE: read cancellation flag: %w", err)
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
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"=?,"lease_token"=NULL,"lease_until"=NULL,"last_code"=?,"finished_at"=`+sqliteDatabaseMicros+`,"updated_at"=`+sqliteDatabaseMicros+sqliteQueueFence, state, optionalText(code), id, token)
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
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='pending',"available_at"=`+sqliteDatabaseMicros+`+?,"lease_token"=NULL,"lease_until"=NULL,"last_code"=?,"updated_at"=`+sqliteDatabaseMicros+sqliteQueueFence, delay.Microseconds(), optionalText(code), id, token)
}

func (store *queueStore) Release(ctx context.Context, id, token string) (bool, error) {
	if err := queueprovider.ValidateIdentity(id, token); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='pending',"available_at"=`+sqliteDatabaseMicros+`,"lease_token"=NULL,"lease_until"=NULL,"updated_at"=`+sqliteDatabaseMicros+sqliteQueueFence, id, token)
}

func (store *queueStore) Cancel(ctx context.Context, id string) (bool, error) {
	if err := queueprovider.ValidateJobIdentity(id); err != nil {
		return false, err
	}
	immediate, err := store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='canceled',"lease_token"=NULL,"lease_until"=NULL,"finished_at"=`+sqliteDatabaseMicros+`,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND "status"='pending'`, id)
	if err != nil || immediate {
		return immediate, err
	}
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "cancel_requested_at"=`+sqliteDatabaseMicros+`,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND "status"='leased'`, id)
}

func (store *queueStore) Requeue(ctx context.Context, id string) (bool, error) {
	if err := queueprovider.ValidateJobIdentity(id); err != nil {
		return false, err
	}
	return store.fenced(ctx, `UPDATE `+sqliteQueueTable+` SET "status"='pending',"attempt_count"=0,"available_at"=`+sqliteDatabaseMicros+`,"lease_token"=NULL,"lease_until"=NULL,"cancel_requested_at"=NULL,"finished_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND "status" IN ('failed','canceled')`, id)
}

func (store *queueStore) RunRetention(ctx context.Context, policy queueprovider.RetentionPolicy) (int, error) {
	if err := queueprovider.ValidateRetention(policy); err != nil {
		return 0, err
	}
	result, err := store.database.ExecContext(ctx, `DELETE FROM `+sqliteQueueTable+` WHERE "id" IN (SELECT "id" FROM `+sqliteQueueTable+` WHERE "status" IN ('succeeded','failed','canceled') AND "finished_at"<=? ORDER BY "finished_at","id" LIMIT ?)`, policy.OlderThan.UTC().UnixMicro(), policy.MaxRows)
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
