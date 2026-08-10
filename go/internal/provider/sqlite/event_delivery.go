package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/jmoiron/sqlx"
)

const sqliteDatabaseMicros = `CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)`

type eventCoordinator struct {
	database *sqlx.DB
}

// EventCoordinator binds the closed P7 delivery state machine to a live
// SQLite database. It accepts no physical names or caller-authored SQL.
func (*Provider) EventCoordinator(database *sqlx.DB) (eventprovider.Coordinator, error) {
	if database == nil {
		return nil, fmt.Errorf("P7_SQLITE_DELIVERY: database is nil")
	}
	return &eventCoordinator{database: database}, nil
}

func (coordinator *eventCoordinator) Claim(ctx context.Context, options eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	snapshot, err := coordinator.ClaimWithDepth(ctx, options)
	return snapshot.Leases, err
}

func (coordinator *eventCoordinator) ClaimWithDepth(ctx context.Context, options eventprovider.ClaimOptions) (eventprovider.ClaimSnapshot, error) {
	if err := eventprovider.ValidateClaim(options); err != nil {
		return eventprovider.ClaimSnapshot{}, err
	}
	tokens := make([]string, options.Groups)
	for index := range tokens {
		token, err := eventprovider.NewLeaseToken()
		if err != nil {
			return eventprovider.ClaimSnapshot{}, err
		}
		tokens[index] = token
	}
	connection, err := coordinator.database.Connx(ctx)
	if err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_SQLITE_DELIVERY: reserve connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_SQLITE_DELIVERY: begin immediate claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := sqliteMaterializeMissing(ctx, connection, options.Groups); err != nil {
		return eventprovider.ClaimSnapshot{}, err
	}
	var now int64
	if err := connection.GetContext(ctx, &now, "SELECT "+sqliteDatabaseMicros); err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_SQLITE_DELIVERY: read database time: %w", err)
	}
	var causations []string
	if err := connection.SelectContext(ctx, &causations, `SELECT "causation_id" FROM "main"."_golem_outbox_delivery" WHERE ("status"='pending' AND "available_at"<=?) OR ("status"='leased' AND "lease_until"<=?) ORDER BY "first_recorded_at","causation_id" LIMIT ?`, now, now, options.Groups); err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_SQLITE_DELIVERY: discover claimable groups: %w", err)
	}
	leases := make([]eventprovider.Lease, 0, len(causations))
	leaseUntil := now + options.LeaseDuration.Microseconds()
	for index, causation := range causations {
		result, updateErr := connection.ExecContext(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='leased',"attempt_count"=CASE WHEN "attempt_count"<9223372036854775807 THEN "attempt_count"+1 ELSE "attempt_count" END,"lease_token"=?,"lease_until"=?,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=? WHERE "causation_id"=? AND (("status"='pending' AND "available_at"<=?) OR ("status"='leased' AND "lease_until"<=?))`, tokens[index], leaseUntil, now, causation, now, now)
		if updateErr != nil {
			return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_SQLITE_DELIVERY: lease group: %w", updateErr)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_SQLITE_DELIVERY: immediate claim lost exclusive group ownership")
		}
		delivery, readErr := sqliteReadDelivery(ctx, connection, causation)
		if readErr != nil {
			return eventprovider.ClaimSnapshot{}, readErr
		}
		facts, readErr := sqliteReadFacts(ctx, connection, causation)
		if readErr != nil {
			return eventprovider.ClaimSnapshot{}, readErr
		}
		delivery.ImmutableFactRows = len(facts)
		leases = append(leases, eventprovider.Lease{Delivery: delivery, Facts: facts})
	}
	depth, err := sqliteDeliveryDepth(ctx, connection)
	if err != nil {
		return eventprovider.ClaimSnapshot{}, err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_SQLITE_DELIVERY: commit claim: %w", err)
	}
	committed = true
	return eventprovider.ClaimSnapshot{Leases: cloneLeases(leases), Depth: depth}, nil
}

func sqliteDeliveryDepth(ctx context.Context, queryer sqlx.QueryerContext) (eventprovider.DepthSnapshot, error) {
	var row struct {
		Pending int64 `db:"pending"`
		Blocked int64 `db:"blocked"`
		Retired int64 `db:"retired"`
	}
	if err := sqlx.GetContext(ctx, queryer, &row, `SELECT COALESCE(SUM(CASE WHEN "status"='pending' THEN 1 ELSE 0 END),0) AS "pending",COALESCE(SUM(CASE WHEN "status"='blocked' THEN 1 ELSE 0 END),0) AS "blocked",COALESCE(SUM(CASE WHEN "status"='retired' THEN 1 ELSE 0 END),0) AS "retired" FROM "main"."_golem_outbox_delivery"`); err != nil {
		return eventprovider.DepthSnapshot{}, fmt.Errorf("P8_SQLITE_OBSERVE: snapshot delivery depths: %w", err)
	}
	return eventprovider.DepthSnapshot{Pending: row.Pending, Blocked: row.Blocked, Retired: row.Retired}, nil
}

func (coordinator *eventCoordinator) Renew(ctx context.Context, causation, token string, duration time.Duration) (bool, error) {
	if err := eventprovider.ValidateClaim(eventprovider.ClaimOptions{Groups: 1, LeaseDuration: duration}); err != nil {
		return false, err
	}
	return coordinator.fenced(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "lease_until"=`+sqliteDatabaseMicros+`+?,"updated_at"=`+sqliteDatabaseMicros+` WHERE "causation_id"=? AND "lease_token"=? AND "status"='leased'`, duration.Microseconds(), causation, token)
}

func (coordinator *eventCoordinator) Acknowledge(ctx context.Context, causation, token string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='delivered',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=`+sqliteDatabaseMicros+`,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "causation_id"=? AND "lease_token"=? AND "status"='leased'`, causation, token)
}

func (coordinator *eventCoordinator) Retry(ctx context.Context, causation, token string, delay time.Duration, code string) (bool, error) {
	if err := eventprovider.ValidateDelay(delay); err != nil {
		return false, err
	}
	if err := eventprovider.ValidateFailureCode(code, false); err != nil {
		return false, err
	}
	var failure any
	if code != "" {
		failure = code
	}
	return coordinator.fenced(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='pending',"available_at"=`+sqliteDatabaseMicros+`+?,"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"last_failure_code"=?,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "causation_id"=? AND "lease_token"=? AND "status"='leased'`, delay.Microseconds(), failure, causation, token)
}

func (coordinator *eventCoordinator) Block(ctx context.Context, causation, token, code string) (bool, error) {
	if err := eventprovider.ValidateFailureCode(code, true); err != nil {
		return false, err
	}
	return coordinator.fenced(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='blocked',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"last_failure_code"=?,"blocked_at"=`+sqliteDatabaseMicros+`,"retired_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "causation_id"=? AND "lease_token"=? AND "status"='leased'`, code, causation, token)
}

func (coordinator *eventCoordinator) Release(ctx context.Context, causation, token string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='pending',"available_at"=`+sqliteDatabaseMicros+`,"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "causation_id"=? AND "lease_token"=? AND "status"='leased'`, causation, token)
}

func (coordinator *eventCoordinator) fenced(ctx context.Context, statement string, arguments ...any) (bool, error) {
	result, err := coordinator.database.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return false, fmt.Errorf("P7_SQLITE_DELIVERY: fenced transition: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("P7_SQLITE_DELIVERY: transition result: %w", err)
	}
	return changed == 1, nil
}

func (coordinator *eventCoordinator) Inspect(ctx context.Context, causation string) (eventprovider.Delivery, error) {
	delivery, err := sqliteReadDelivery(ctx, coordinator.database, causation)
	if err == nil {
		return delivery, nil
	}
	if err != sql.ErrNoRows {
		return eventprovider.Delivery{}, err
	}
	var row struct {
		First int64 `db:"first_recorded_at"`
		Count int   `db:"fact_rows"`
	}
	if queryErr := coordinator.database.GetContext(ctx, &row, `SELECT MIN("recorded_at") AS "first_recorded_at",COUNT(*) AS "fact_rows" FROM "main"."_golem_outbox" WHERE "causation_id"=? HAVING COUNT(*)>0`, causation); queryErr != nil {
		if queryErr == sql.ErrNoRows {
			return eventprovider.Delivery{}, fmt.Errorf("P7_DELIVERY_NOT_FOUND: causation is absent")
		}
		return eventprovider.Delivery{}, fmt.Errorf("P7_SQLITE_DELIVERY: inspect missing state: %w", queryErr)
	}
	first := time.UnixMicro(row.First).UTC()
	return eventprovider.Delivery{CausationID: causation, Status: eventprovider.StatusPending, FirstRecordedAt: first, AvailableAt: first, UpdatedAt: first, ImmutableFactRows: row.Count}, nil
}

func (coordinator *eventCoordinator) Resume(ctx context.Context, causation string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='pending',"available_at"=`+sqliteDatabaseMicros+`,"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=`+sqliteDatabaseMicros+` WHERE "causation_id"=? AND "status"='blocked'`, causation)
}

func (coordinator *eventCoordinator) Retire(ctx context.Context, causation string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='retired',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=`+sqliteDatabaseMicros+`,"updated_at"=`+sqliteDatabaseMicros+` WHERE "causation_id"=? AND "status"='blocked'`, causation)
}

func (coordinator *eventCoordinator) RunRetention(ctx context.Context, policy eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	if err := eventprovider.ValidateRetention(policy); err != nil {
		return eventprovider.RetentionResult{}, err
	}
	connection, err := coordinator.database.Connx(ctx)
	if err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: reserve connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var candidates []sqliteRetentionCandidate
	if err := connection.SelectContext(ctx, &candidates, `SELECT d."causation_id",COUNT(o."event_id") AS "fact_rows" FROM "main"."_golem_outbox_delivery" d JOIN "main"."_golem_outbox" o ON o."causation_id"=d."causation_id" WHERE d."status"='delivered' AND d."delivered_at"<=? GROUP BY d."causation_id",d."delivered_at",d."first_recorded_at" HAVING MAX(o."recorded_at")<=? ORDER BY d."delivered_at",d."first_recorded_at",d."causation_id" LIMIT ?`, policy.OlderThan.UTC().UnixMicro(), policy.OlderThan.UTC().UnixMicro(), policy.MaxRows); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: select groups: %w", err)
	}
	selected := retentionPrefix(candidates, policy.MaxRows)
	result := eventprovider.RetentionResult{}
	for _, candidate := range selected {
		deleted, deleteErr := connection.ExecContext(ctx, `DELETE FROM "main"."_golem_outbox" WHERE "causation_id"=?`, candidate.Causation)
		if deleteErr != nil {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: delete facts: %w", deleteErr)
		}
		facts, _ := deleted.RowsAffected()
		if int(facts) != candidate.Facts {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: causal fact count changed under immediate ownership")
		}
		state, deleteErr := connection.ExecContext(ctx, `DELETE FROM "main"."_golem_outbox_delivery" WHERE "causation_id"=? AND "status"='delivered'`, candidate.Causation)
		if deleteErr != nil {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: delete delivery: %w", deleteErr)
		}
		states, _ := state.RowsAffected()
		if states != 1 {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: delivery ownership changed")
		}
		result.Causations++
		result.Facts += int(facts)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_SQLITE_RETENTION: commit: %w", err)
	}
	committed = true
	return result, nil
}

func sqliteMaterializeMissing(ctx context.Context, queryer sqlx.ExecerContext, maximum int) error {
	if maximum <= 0 || maximum > eventprovider.MaximumClaimGroups {
		return fmt.Errorf("P7_SQLITE_DELIVERY: missing-state materialization limit is invalid")
	}
	_, err := queryer.ExecContext(ctx, `INSERT INTO "main"."_golem_outbox_delivery" ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") SELECT o."causation_id",'pending',MIN(o."recorded_at"),0,MIN(o."recorded_at"),MIN(o."recorded_at") FROM "main"."_golem_outbox" o LEFT JOIN "main"."_golem_outbox_delivery" d ON d."causation_id"=o."causation_id" WHERE d."causation_id" IS NULL GROUP BY o."causation_id" ORDER BY MIN(o."recorded_at"),o."causation_id" LIMIT ? ON CONFLICT ("causation_id") DO NOTHING`, maximum)
	if err != nil {
		return fmt.Errorf("P7_SQLITE_DELIVERY: materialize missing groups: %w", err)
	}
	return nil
}

type sqliteDeliveryRow struct {
	CausationID     string         `db:"causation_id"`
	Status          string         `db:"status"`
	FirstRecordedAt int64          `db:"first_recorded_at"`
	AttemptCount    int64          `db:"attempt_count"`
	AvailableAt     int64          `db:"available_at"`
	LeaseToken      sql.NullString `db:"lease_token"`
	LeaseUntil      sql.NullInt64  `db:"lease_until"`
	DeliveredAt     sql.NullInt64  `db:"delivered_at"`
	FailureCode     sql.NullString `db:"last_failure_code"`
	BlockedAt       sql.NullInt64  `db:"blocked_at"`
	RetiredAt       sql.NullInt64  `db:"retired_at"`
	UpdatedAt       int64          `db:"updated_at"`
	FactRows        int            `db:"fact_rows"`
}

func sqliteReadDelivery(ctx context.Context, queryer sqlx.QueryerContext, causation string) (eventprovider.Delivery, error) {
	var row sqliteDeliveryRow
	err := sqlx.GetContext(ctx, queryer, &row, `SELECT d.*,COUNT(o."event_id") AS "fact_rows" FROM "main"."_golem_outbox_delivery" d LEFT JOIN "main"."_golem_outbox" o ON o."causation_id"=d."causation_id" WHERE d."causation_id"=? GROUP BY d."causation_id"`, causation)
	if err != nil {
		return eventprovider.Delivery{}, err
	}
	return sqliteDeliveryValue(row), nil
}

func sqliteReadFacts(ctx context.Context, queryer sqlx.QueryerContext, causation string) ([]eventprovider.FactRow, error) {
	type factRow struct {
		EventID               string `db:"event_id"`
		FactVersion           int64  `db:"fact_version"`
		CodecIdentity         string `db:"codec_identity"`
		GenerationFingerprint string `db:"generation_fingerprint"`
		ModelID               string `db:"model_id"`
		Action                string `db:"action"`
		BeforeIdentity        []byte `db:"before_identity"`
		AfterIdentity         []byte `db:"after_identity"`
		CausationID           string `db:"causation_id"`
		TransactionOrdinal    int64  `db:"transaction_ordinal"`
		Metadata              []byte `db:"metadata"`
		DeleteSnapshot        []byte `db:"delete_snapshot"`
		RecordedMicros        int64  `db:"recorded_at"`
	}
	var rows []factRow
	err := sqlx.SelectContext(ctx, queryer, &rows, `SELECT "event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at" FROM "main"."_golem_outbox" WHERE "causation_id"=? ORDER BY "transaction_ordinal"`, causation)
	if err != nil {
		return nil, fmt.Errorf("P7_SQLITE_DELIVERY: read immutable facts: %w", err)
	}
	result := make([]eventprovider.FactRow, len(rows))
	for index, row := range rows {
		result[index] = eventprovider.FactRow{
			EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity,
			GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action,
			BeforeIdentity: row.BeforeIdentity, AfterIdentity: row.AfterIdentity, CausationID: row.CausationID,
			TransactionOrdinal: row.TransactionOrdinal, Metadata: row.Metadata, DeleteSnapshot: row.DeleteSnapshot,
			RecordedAt: time.UnixMicro(row.RecordedMicros).UTC(),
		}
	}
	return eventprovider.CloneFacts(result), nil
}

func sqliteDeliveryValue(row sqliteDeliveryRow) eventprovider.Delivery {
	value := eventprovider.Delivery{
		CausationID: row.CausationID, Status: eventprovider.Status(row.Status), FirstRecordedAt: time.UnixMicro(row.FirstRecordedAt).UTC(),
		AttemptCount: row.AttemptCount, AvailableAt: time.UnixMicro(row.AvailableAt).UTC(), LeaseToken: row.LeaseToken.String,
		LastFailureCode: row.FailureCode.String, UpdatedAt: time.UnixMicro(row.UpdatedAt).UTC(), ImmutableFactRows: row.FactRows,
	}
	value.LeaseUntil = sqliteOptionalTime(row.LeaseUntil)
	value.DeliveredAt = sqliteOptionalTime(row.DeliveredAt)
	value.BlockedAt = sqliteOptionalTime(row.BlockedAt)
	value.RetiredAt = sqliteOptionalTime(row.RetiredAt)
	return value
}

func sqliteOptionalTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMicro(value.Int64).UTC()
	return &result
}

func cloneLeases(leases []eventprovider.Lease) []eventprovider.Lease {
	result := make([]eventprovider.Lease, len(leases))
	copy(result, leases)
	for index := range result {
		result[index].Facts = eventprovider.CloneFacts(result[index].Facts)
	}
	return result
}

type sqliteRetentionCandidate struct {
	Causation string `db:"causation_id"`
	Facts     int    `db:"fact_rows"`
}

func retentionPrefix(candidates []sqliteRetentionCandidate, maximum int) []sqliteRetentionCandidate {
	count := 0
	for index, candidate := range candidates {
		// Whole-causation deletion never spends beyond the row budget. An
		// oversized oldest group is deliberately skipped for this bounded run;
		// an operator must raise MaxRows to retire it.
		if count+candidate.Facts > maximum {
			return candidates[:index]
		}
		count += candidate.Facts
		if count >= maximum {
			return candidates[:index+1]
		}
	}
	return candidates
}
