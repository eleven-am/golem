package postgresql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

var eventNamespacePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

type eventCoordinator struct {
	database  *sqlx.DB
	namespace physical.PhysicalName
}

func (*Provider) EventCoordinator(database *sqlx.DB) (eventprovider.Coordinator, error) {
	return newEventCoordinator(database, "_golem")
}

// EventCoordinatorAt exists for generated schemas whose reviewed system
// namespace differs from the default. The namespace must already be a closed
// physical identifier; no application value can become SQL.
func (*Provider) EventCoordinatorAt(database *sqlx.DB, namespace physical.PhysicalName) (eventprovider.Coordinator, error) {
	return newEventCoordinator(database, namespace)
}

func newEventCoordinator(database *sqlx.DB, namespace physical.PhysicalName) (eventprovider.Coordinator, error) {
	if database == nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: database is nil")
	}
	if !eventNamespacePattern.MatchString(string(namespace)) {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: system namespace is invalid")
	}
	return &eventCoordinator{database: database, namespace: namespace}, nil
}

func (coordinator *eventCoordinator) Claim(ctx context.Context, options eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	if err := eventprovider.ValidateClaim(options); err != nil {
		return nil, err
	}
	tokens := make([]string, options.Groups)
	for index := range tokens {
		token, err := eventprovider.NewLeaseToken()
		if err != nil {
			return nil, err
		}
		tokens[index] = token
	}
	transaction, err := coordinator.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: begin claim: %w", err)
	}
	defer transaction.Rollback()
	if err := coordinator.materializeMissing(ctx, transaction, options.Groups); err != nil {
		return nil, err
	}
	var causations []string
	delivery := coordinator.deliveryTable()
	query := `SELECT "causation_id" FROM ` + delivery + ` WHERE ("status"='pending' AND "available_at"<=clock_timestamp()) OR ("status"='leased' AND "lease_until"<=clock_timestamp()) ORDER BY "first_recorded_at","causation_id" FOR UPDATE SKIP LOCKED LIMIT $1`
	if err := transaction.SelectContext(ctx, &causations, query, options.Groups); err != nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: discover claimable groups: %w", err)
	}
	leases := make([]eventprovider.Lease, 0, len(causations))
	for index, causation := range causations {
		update := `UPDATE ` + delivery + ` SET "status"='leased',"attempt_count"=CASE WHEN "attempt_count"<9223372036854775807 THEN "attempt_count"+1 ELSE "attempt_count" END,"lease_token"=$1,"lease_until"=clock_timestamp()+$2*interval '1 microsecond',"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id"=$3 AND (("status"='pending' AND "available_at"<=clock_timestamp()) OR ("status"='leased' AND "lease_until"<=clock_timestamp()))`
		result, updateErr := transaction.ExecContext(ctx, update, tokens[index], options.LeaseDuration.Microseconds(), causation)
		if updateErr != nil {
			return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: lease group: %w", updateErr)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: locked group lost claim eligibility")
		}
		state, readErr := coordinator.readDelivery(ctx, transaction, causation)
		if readErr != nil {
			return nil, readErr
		}
		facts, readErr := coordinator.readFacts(ctx, transaction, causation)
		if readErr != nil {
			return nil, readErr
		}
		state.ImmutableFactRows = len(facts)
		leases = append(leases, eventprovider.Lease{Delivery: state, Facts: facts})
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: commit claim: %w", err)
	}
	return clonePostgreSQLLeases(leases), nil
}

func (coordinator *eventCoordinator) Renew(ctx context.Context, causation, token string, duration time.Duration) (bool, error) {
	if err := eventprovider.ValidateClaim(eventprovider.ClaimOptions{Groups: 1, LeaseDuration: duration}); err != nil {
		return false, err
	}
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "lease_until"=clock_timestamp()+$1*interval '1 microsecond',"updated_at"=clock_timestamp() WHERE "causation_id"=$2 AND "lease_token"=$3 AND "status"='leased'`, duration.Microseconds(), causation, token)
}

func (coordinator *eventCoordinator) Acknowledge(ctx context.Context, causation, token string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='delivered',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=clock_timestamp(),"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id"=$1 AND "lease_token"=$2 AND "status"='leased'`, causation, token)
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
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='pending',"available_at"=clock_timestamp()+$1*interval '1 microsecond',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"last_failure_code"=$2,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id"=$3 AND "lease_token"=$4 AND "status"='leased'`, delay.Microseconds(), failure, causation, token)
}

func (coordinator *eventCoordinator) Block(ctx context.Context, causation, token, code string) (bool, error) {
	if err := eventprovider.ValidateFailureCode(code, true); err != nil {
		return false, err
	}
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='blocked',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"last_failure_code"=$1,"blocked_at"=clock_timestamp(),"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id"=$2 AND "lease_token"=$3 AND "status"='leased'`, code, causation, token)
}

func (coordinator *eventCoordinator) Release(ctx context.Context, causation, token string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='pending',"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id"=$1 AND "lease_token"=$2 AND "status"='leased'`, causation, token)
}

func (coordinator *eventCoordinator) fenced(ctx context.Context, statement string, arguments ...any) (bool, error) {
	result, err := coordinator.database.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return false, fmt.Errorf("P7_POSTGRESQL_DELIVERY: fenced transition: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("P7_POSTGRESQL_DELIVERY: transition result: %w", err)
	}
	return changed == 1, nil
}

func (coordinator *eventCoordinator) Inspect(ctx context.Context, causation string) (eventprovider.Delivery, error) {
	state, err := coordinator.readDelivery(ctx, coordinator.database, causation)
	if err == nil {
		return state, nil
	}
	if err != sql.ErrNoRows {
		return eventprovider.Delivery{}, err
	}
	var row struct {
		First time.Time `db:"first_recorded_at"`
		Count int       `db:"fact_rows"`
	}
	query := `SELECT MIN("recorded_at") AS "first_recorded_at",COUNT(*) AS "fact_rows" FROM ` + coordinator.outboxTable() + ` WHERE "causation_id"=$1 HAVING COUNT(*)>0`
	if queryErr := coordinator.database.GetContext(ctx, &row, query, causation); queryErr != nil {
		if queryErr == sql.ErrNoRows {
			return eventprovider.Delivery{}, fmt.Errorf("P7_DELIVERY_NOT_FOUND: causation is absent")
		}
		return eventprovider.Delivery{}, fmt.Errorf("P7_POSTGRESQL_DELIVERY: inspect missing state: %w", queryErr)
	}
	first := row.First.UTC().Truncate(time.Microsecond)
	return eventprovider.Delivery{CausationID: causation, Status: eventprovider.StatusPending, FirstRecordedAt: first, AvailableAt: first, UpdatedAt: first, ImmutableFactRows: row.Count}, nil
}

func (coordinator *eventCoordinator) Resume(ctx context.Context, causation string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='pending',"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id"=$1 AND "status"='blocked'`, causation)
}

func (coordinator *eventCoordinator) Retire(ctx context.Context, causation string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='retired',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=clock_timestamp(),"updated_at"=clock_timestamp() WHERE "causation_id"=$1 AND "status"='blocked'`, causation)
}

func (coordinator *eventCoordinator) RunRetention(ctx context.Context, policy eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	if err := eventprovider.ValidateRetention(policy); err != nil {
		return eventprovider.RetentionResult{}, err
	}
	transaction, err := coordinator.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: begin: %w", err)
	}
	defer transaction.Rollback()
	query := `SELECT d."causation_id" FROM ` + coordinator.deliveryTable() + ` d WHERE d."status"='delivered' AND d."delivered_at"<=$1 AND NOT EXISTS (SELECT 1 FROM ` + coordinator.outboxTable() + ` o WHERE o."causation_id"=d."causation_id" AND o."recorded_at">$1) ORDER BY d."delivered_at",d."first_recorded_at",d."causation_id" FOR UPDATE OF d SKIP LOCKED LIMIT $2`
	var causations []string
	if err := transaction.SelectContext(ctx, &causations, query, policy.OlderThan.UTC().Truncate(time.Microsecond), policy.MaxRows); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: select groups: %w", err)
	}
	candidates := make([]postgresqlRetentionCandidate, 0, len(causations))
	for _, causation := range causations {
		var count int
		if err := transaction.GetContext(ctx, &count, `SELECT COUNT(*) FROM `+coordinator.outboxTable()+` WHERE "causation_id"=$1`, causation); err != nil {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: count facts: %w", err)
		}
		candidates = append(candidates, postgresqlRetentionCandidate{Causation: causation, Facts: count})
	}
	selected := postgresqlRetentionPrefix(candidates, policy.MaxRows)
	result := eventprovider.RetentionResult{}
	for _, candidate := range selected {
		deleted, deleteErr := transaction.ExecContext(ctx, `DELETE FROM `+coordinator.outboxTable()+` WHERE "causation_id"=$1`, candidate.Causation)
		if deleteErr != nil {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: delete facts: %w", deleteErr)
		}
		facts, _ := deleted.RowsAffected()
		if int(facts) != candidate.Facts {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: causal fact count changed under row lock")
		}
		state, deleteErr := transaction.ExecContext(ctx, `DELETE FROM `+coordinator.deliveryTable()+` WHERE "causation_id"=$1 AND "status"='delivered'`, candidate.Causation)
		if deleteErr != nil {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: delete delivery: %w", deleteErr)
		}
		states, _ := state.RowsAffected()
		if states != 1 {
			return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: delivery ownership changed")
		}
		result.Causations++
		result.Facts += int(facts)
	}
	if err := transaction.Commit(); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: commit: %w", err)
	}
	return result, nil
}

func (coordinator *eventCoordinator) materializeMissing(ctx context.Context, queryer sqlx.ExecerContext, maximum int) error {
	if maximum <= 0 || maximum > eventprovider.MaximumClaimGroups {
		return fmt.Errorf("P7_POSTGRESQL_DELIVERY: missing-state materialization limit is invalid")
	}
	statement := `INSERT INTO ` + coordinator.deliveryTable() + ` ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") SELECT o."causation_id",'pending',MIN(o."recorded_at"),0,MIN(o."recorded_at"),MIN(o."recorded_at") FROM ` + coordinator.outboxTable() + ` o LEFT JOIN ` + coordinator.deliveryTable() + ` d ON d."causation_id"=o."causation_id" WHERE d."causation_id" IS NULL GROUP BY o."causation_id" ORDER BY MIN(o."recorded_at"),o."causation_id" LIMIT $1 ON CONFLICT ("causation_id") DO NOTHING`
	if _, err := queryer.ExecContext(ctx, statement, maximum); err != nil {
		return fmt.Errorf("P7_POSTGRESQL_DELIVERY: materialize missing groups: %w", err)
	}
	return nil
}

type postgresqlDeliveryRow struct {
	CausationID     string         `db:"causation_id"`
	Status          string         `db:"status"`
	FirstRecordedAt time.Time      `db:"first_recorded_at"`
	AttemptCount    int64          `db:"attempt_count"`
	AvailableAt     time.Time      `db:"available_at"`
	LeaseToken      sql.NullString `db:"lease_token"`
	LeaseUntil      sql.NullTime   `db:"lease_until"`
	DeliveredAt     sql.NullTime   `db:"delivered_at"`
	FailureCode     sql.NullString `db:"last_failure_code"`
	BlockedAt       sql.NullTime   `db:"blocked_at"`
	RetiredAt       sql.NullTime   `db:"retired_at"`
	UpdatedAt       time.Time      `db:"updated_at"`
	FactRows        int            `db:"fact_rows"`
}

func (coordinator *eventCoordinator) readDelivery(ctx context.Context, queryer sqlx.QueryerContext, causation string) (eventprovider.Delivery, error) {
	var row postgresqlDeliveryRow
	query := `SELECT d.*,(SELECT COUNT(*) FROM ` + coordinator.outboxTable() + ` o WHERE o."causation_id"=d."causation_id") AS "fact_rows" FROM ` + coordinator.deliveryTable() + ` d WHERE d."causation_id"=$1`
	if err := sqlx.GetContext(ctx, queryer, &row, query, causation); err != nil {
		return eventprovider.Delivery{}, err
	}
	return postgresqlDeliveryValue(row), nil
}

func (coordinator *eventCoordinator) readFacts(ctx context.Context, queryer sqlx.QueryerContext, causation string) ([]eventprovider.FactRow, error) {
	type factRow struct {
		EventID               string    `db:"event_id"`
		FactVersion           int64     `db:"fact_version"`
		CodecIdentity         string    `db:"codec_identity"`
		GenerationFingerprint string    `db:"generation_fingerprint"`
		ModelID               string    `db:"model_id"`
		Action                string    `db:"action"`
		BeforeIdentity        []byte    `db:"before_identity"`
		AfterIdentity         []byte    `db:"after_identity"`
		CausationID           string    `db:"causation_id"`
		TransactionOrdinal    int64     `db:"transaction_ordinal"`
		Metadata              []byte    `db:"metadata"`
		DeleteSnapshot        []byte    `db:"delete_snapshot"`
		RecordedAt            time.Time `db:"recorded_at"`
	}
	var rows []factRow
	query := `SELECT "event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at" FROM ` + coordinator.outboxTable() + ` WHERE "causation_id"=$1 ORDER BY "transaction_ordinal"`
	if err := sqlx.SelectContext(ctx, queryer, &rows, query, causation); err != nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: read immutable facts: %w", err)
	}
	result := make([]eventprovider.FactRow, len(rows))
	for index, row := range rows {
		result[index] = eventprovider.FactRow{
			EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity,
			GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action,
			BeforeIdentity: row.BeforeIdentity, AfterIdentity: row.AfterIdentity, CausationID: row.CausationID,
			TransactionOrdinal: row.TransactionOrdinal, Metadata: row.Metadata, DeleteSnapshot: row.DeleteSnapshot,
			RecordedAt: row.RecordedAt.UTC().Truncate(time.Microsecond),
		}
	}
	return eventprovider.CloneFacts(result), nil
}

func (coordinator *eventCoordinator) deliveryTable() string {
	return qualified(coordinator.namespace, "_golem_outbox_delivery")
}

func (coordinator *eventCoordinator) outboxTable() string {
	return qualified(coordinator.namespace, "_golem_outbox")
}

func postgresqlDeliveryValue(row postgresqlDeliveryRow) eventprovider.Delivery {
	value := eventprovider.Delivery{
		CausationID: row.CausationID, Status: eventprovider.Status(row.Status), FirstRecordedAt: row.FirstRecordedAt.UTC().Truncate(time.Microsecond),
		AttemptCount: row.AttemptCount, AvailableAt: row.AvailableAt.UTC().Truncate(time.Microsecond), LeaseToken: row.LeaseToken.String,
		LastFailureCode: row.FailureCode.String, UpdatedAt: row.UpdatedAt.UTC().Truncate(time.Microsecond), ImmutableFactRows: row.FactRows,
	}
	value.LeaseUntil = postgresqlOptionalTime(row.LeaseUntil)
	value.DeliveredAt = postgresqlOptionalTime(row.DeliveredAt)
	value.BlockedAt = postgresqlOptionalTime(row.BlockedAt)
	value.RetiredAt = postgresqlOptionalTime(row.RetiredAt)
	return value
}

func postgresqlOptionalTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC().Truncate(time.Microsecond)
	return &result
}

func clonePostgreSQLLeases(leases []eventprovider.Lease) []eventprovider.Lease {
	result := make([]eventprovider.Lease, len(leases))
	copy(result, leases)
	for index := range result {
		result[index].Facts = eventprovider.CloneFacts(result[index].Facts)
	}
	return result
}

type postgresqlRetentionCandidate struct {
	Causation string
	Facts     int
}

func postgresqlRetentionPrefix(candidates []postgresqlRetentionCandidate, maximum int) []postgresqlRetentionCandidate {
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
