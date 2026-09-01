package postgresql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

var eventNamespacePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

type eventCoordinator struct {
	database            *sqlx.DB
	namespace           physical.PhysicalName
	legacyProbeComplete atomic.Bool
	leaseClockAligned   atomic.Bool
}

type postgresqlEventQueryExecer interface {
	sqlx.QueryerContext
	sqlx.ExecerContext
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
	snapshot, err := coordinator.claim(ctx, options, false)
	return snapshot.Leases, err
}

func (coordinator *eventCoordinator) ClaimWithDepth(ctx context.Context, options eventprovider.ClaimOptions) (eventprovider.ClaimSnapshot, error) {
	return coordinator.claim(ctx, options, true)
}

func (coordinator *eventCoordinator) claim(ctx context.Context, options eventprovider.ClaimOptions, includeDepth bool) (eventprovider.ClaimSnapshot, error) {
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
	transaction, err := coordinator.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_POSTGRESQL_DELIVERY: begin claim: %w", err)
	}
	defer transaction.Rollback()
	legacyComplete := false
	if !coordinator.legacyProbeComplete.Load() {
		complete, err := coordinator.materializeMissing(ctx, transaction, options.Groups)
		if err != nil {
			return eventprovider.ClaimSnapshot{}, err
		}
		legacyComplete = complete
	}
	leaseAligned := false
	if !coordinator.leaseClockAligned.Load() {
		if _, err := transaction.ExecContext(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "available_at"=CASE "status" WHEN 'leased' THEN "lease_until" ELSE "delivered_at" END WHERE ("status"='leased' AND "lease_until" IS NOT NULL AND "available_at"<>"lease_until") OR ("status"='delivered' AND "delivered_at" IS NOT NULL AND "available_at"<>"delivered_at")`); err != nil {
			return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_POSTGRESQL_DELIVERY: align existing lease eligibility: %w", err)
		}
		leaseAligned = true
	}
	var causations []string
	delivery := coordinator.deliveryTable()
	query := `SELECT "causation_id" FROM ` + delivery + ` WHERE "status" IN ('pending','leased') AND "available_at"<=clock_timestamp() ORDER BY "first_recorded_at","causation_id" FOR UPDATE SKIP LOCKED LIMIT $1`
	if err := transaction.SelectContext(ctx, &causations, query, options.Groups); err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_POSTGRESQL_DELIVERY: discover claimable groups: %w", err)
	}
	causations, oversized, err := coordinator.boundedCausations(ctx, transaction, causations, eventprovider.ClaimByteLimit(options))
	if err != nil {
		return eventprovider.ClaimSnapshot{}, err
	}
	if err := coordinator.blockOversizedCausations(ctx, transaction, oversized); err != nil {
		return eventprovider.ClaimSnapshot{}, err
	}
	if len(causations) != 0 {
		values := make([]string, len(causations))
		arguments := make([]any, 0, len(causations)*3)
		for index, causation := range causations {
			position := index*3 + 1
			values[index] = "($" + strconv.Itoa(position) + "::text,$" + strconv.Itoa(position+1) + "::bigint,$" + strconv.Itoa(position+2) + "::text)"
			arguments = append(arguments, tokens[index], options.LeaseDuration.Microseconds(), causation)
		}
		var changed []string
		expiry := `clock_timestamp()+claim.lease_micros*interval '1 microsecond'`
		update := `UPDATE ` + delivery + ` AS target SET "status"='leased',"attempt_count"=CASE WHEN target."attempt_count"<9223372036854775807 THEN target."attempt_count"+1 ELSE target."attempt_count" END,"lease_token"=claim.token,"available_at"=` + expiry + `,"lease_until"=` + expiry + `,"delivered_at"=NULL,"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=clock_timestamp() FROM (VALUES ` + strings.Join(values, ",") + `) AS claim(token,lease_micros,causation_id) WHERE target."causation_id"=claim.causation_id AND target."status" IN ('pending','leased') AND target."available_at"<=clock_timestamp() RETURNING target."causation_id"`
		if err := transaction.SelectContext(ctx, &changed, update, arguments...); err != nil {
			return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_POSTGRESQL_DELIVERY: lease groups: %w", err)
		}
		if len(changed) != len(causations) {
			return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_POSTGRESQL_DELIVERY: locked groups lost claim eligibility")
		}
	}
	leases, err := coordinator.readLeases(ctx, transaction, causations)
	if err != nil {
		return eventprovider.ClaimSnapshot{}, err
	}
	depth := eventprovider.DepthSnapshot{}
	if includeDepth {
		depth, err = coordinator.postgresqlDeliveryDepth(ctx, transaction)
		if err != nil {
			return eventprovider.ClaimSnapshot{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return eventprovider.ClaimSnapshot{}, fmt.Errorf("P7_POSTGRESQL_DELIVERY: commit claim: %w", err)
	}
	if legacyComplete {
		coordinator.legacyProbeComplete.Store(true)
	}
	if leaseAligned {
		coordinator.leaseClockAligned.Store(true)
	}
	return eventprovider.ClaimSnapshot{Leases: clonePostgreSQLLeases(leases), Depth: depth}, nil
}

func (coordinator *eventCoordinator) boundedCausations(ctx context.Context, queryer sqlx.QueryerContext, causations []string, maximum int) ([]string, []string, error) {
	if len(causations) == 0 {
		return nil, nil, nil
	}
	marks, arguments := postgresqlArguments(causations)
	var rows []struct {
		Causation string `db:"causation_id"`
		Bytes     int64  `db:"fact_bytes"`
	}
	size := `octet_length("event_id")+octet_length("codec_identity")+octet_length("generation_fingerprint")+octet_length("model_id")+octet_length("action")+COALESCE(octet_length("before_identity"),0)+COALESCE(octet_length("after_identity"),0)+octet_length("causation_id")+octet_length("metadata")+COALESCE(octet_length("delete_snapshot"),0)+32`
	query := `SELECT "causation_id",SUM(` + size + `) AS "fact_bytes" FROM ` + coordinator.outboxTable() + ` WHERE "causation_id" IN (` + strings.Join(marks, ",") + `) GROUP BY "causation_id"`
	if err := sqlx.SelectContext(ctx, queryer, &rows, query, arguments...); err != nil {
		return nil, nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: measure claimed groups: %w", err)
	}
	measured := make(map[string]int64, len(rows))
	for _, row := range rows {
		measured[row.Causation] = row.Bytes
	}
	result := make([]string, 0, len(causations))
	oversized := make([]string, 0)
	total := int64(0)
	for _, causation := range causations {
		bytes, exists := measured[causation]
		if !exists || bytes <= 0 {
			return nil, nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: claimed group has no measurable facts")
		}
		if bytes > int64(maximum) {
			oversized = append(oversized, causation)
			continue
		}
		if bytes > int64(maximum)-total {
			break
		}
		result = append(result, causation)
		total += bytes
	}
	return result, oversized, nil
}

func (coordinator *eventCoordinator) blockOversizedCausations(ctx context.Context, queryer sqlx.ExecerContext, causations []string) error {
	if len(causations) == 0 {
		return nil
	}
	marks, arguments := postgresqlArguments(causations)
	result, err := queryer.ExecContext(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='blocked',"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=NULL,"last_failure_code"='batch-too-large',"blocked_at"=clock_timestamp(),"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id" IN (`+strings.Join(marks, ",")+`)`, arguments...)
	if err != nil {
		return fmt.Errorf("P7_POSTGRESQL_DELIVERY: block oversized groups: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || int(changed) != len(causations) {
		return fmt.Errorf("P7_POSTGRESQL_DELIVERY: oversized group ownership changed")
	}
	return nil
}

func (coordinator *eventCoordinator) postgresqlDeliveryDepth(ctx context.Context, queryer sqlx.QueryerContext) (eventprovider.DepthSnapshot, error) {
	var row struct {
		Pending int64 `db:"pending"`
		Blocked int64 `db:"blocked"`
		Retired int64 `db:"retired"`
	}
	query := `SELECT COUNT(*) FILTER (WHERE "status"='pending') AS "pending",COUNT(*) FILTER (WHERE "status"='blocked') AS "blocked",COUNT(*) FILTER (WHERE "status"='retired') AS "retired" FROM ` + coordinator.deliveryTable()
	if err := sqlx.GetContext(ctx, queryer, &row, query); err != nil {
		return eventprovider.DepthSnapshot{}, fmt.Errorf("P8_POSTGRESQL_OBSERVE: snapshot delivery depths: %w", err)
	}
	return eventprovider.DepthSnapshot{Pending: row.Pending, Blocked: row.Blocked, Retired: row.Retired}, nil
}

func (coordinator *eventCoordinator) Renew(ctx context.Context, causation, token string, duration time.Duration) (bool, error) {
	if err := eventprovider.ValidateClaim(eventprovider.ClaimOptions{Groups: 1, LeaseDuration: duration}); err != nil {
		return false, err
	}
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "available_at"=clock_timestamp()+$1*interval '1 microsecond',"lease_until"=clock_timestamp()+$1*interval '1 microsecond',"updated_at"=clock_timestamp() WHERE "causation_id"=$2 AND "lease_token"=$3 AND "status"='leased'`, duration.Microseconds(), causation, token)
}

func (coordinator *eventCoordinator) Acknowledge(ctx context.Context, causation, token string) (bool, error) {
	return coordinator.fenced(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "status"='delivered',"available_at"=clock_timestamp(),"lease_token"=NULL,"lease_until"=NULL,"delivered_at"=clock_timestamp(),"blocked_at"=NULL,"retired_at"=NULL,"updated_at"=clock_timestamp() WHERE "causation_id"=$1 AND "lease_token"=$2 AND "status"='leased'`, causation, token)
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
	if _, err := transaction.ExecContext(ctx, `UPDATE `+coordinator.deliveryTable()+` SET "available_at"="delivered_at" WHERE "status"='delivered' AND "delivered_at" IS NOT NULL AND "available_at"<>"delivered_at"`); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: align existing delivery time: %w", err)
	}
	query := `SELECT d."causation_id" FROM ` + coordinator.deliveryTable() + ` d WHERE d."status"='delivered' AND d."available_at"<=$1 AND NOT EXISTS (SELECT 1 FROM ` + coordinator.outboxTable() + ` o WHERE o."causation_id"=d."causation_id" AND o."recorded_at">$1) ORDER BY d."available_at",d."first_recorded_at",d."causation_id" FOR UPDATE OF d SKIP LOCKED LIMIT $2`
	var causations []string
	if err := transaction.SelectContext(ctx, &causations, query, policy.OlderThan.UTC().Truncate(time.Microsecond), policy.MaxRows); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: select groups: %w", err)
	}
	candidates, err := coordinator.retentionCandidates(ctx, transaction, causations)
	if err != nil {
		return eventprovider.RetentionResult{}, err
	}
	selected := postgresqlRetentionPrefix(candidates, policy.MaxRows)
	result, err := coordinator.deleteRetentionCandidates(ctx, transaction, selected)
	if err != nil {
		return eventprovider.RetentionResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: commit: %w", err)
	}
	return result, nil
}

func (coordinator *eventCoordinator) retentionCandidates(ctx context.Context, queryer sqlx.QueryerContext, causations []string) ([]postgresqlRetentionCandidate, error) {
	if len(causations) == 0 {
		return nil, nil
	}
	marks, arguments := postgresqlArguments(causations)
	var counts []postgresqlRetentionCandidate
	if err := sqlx.SelectContext(ctx, queryer, &counts, `SELECT "causation_id",COUNT(*) AS "fact_rows" FROM `+coordinator.outboxTable()+` WHERE "causation_id" IN (`+strings.Join(marks, ",")+`) GROUP BY "causation_id"`, arguments...); err != nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_RETENTION: count facts: %w", err)
	}
	byCausation := make(map[string]int, len(counts))
	for _, count := range counts {
		byCausation[count.Causation] = count.Facts
	}
	ordered := make([]postgresqlRetentionCandidate, 0, len(causations))
	for _, causation := range causations {
		ordered = append(ordered, postgresqlRetentionCandidate{Causation: causation, Facts: byCausation[causation]})
	}
	return ordered, nil
}

func (coordinator *eventCoordinator) deleteRetentionCandidates(ctx context.Context, transaction *sqlx.Tx, selected []postgresqlRetentionCandidate) (eventprovider.RetentionResult, error) {
	if len(selected) == 0 {
		return eventprovider.RetentionResult{}, nil
	}
	causations := make([]string, len(selected))
	expectedFacts := 0
	for index, candidate := range selected {
		causations[index] = candidate.Causation
		expectedFacts += candidate.Facts
	}
	marks, arguments := postgresqlArguments(causations)
	deleted, err := transaction.ExecContext(ctx, `DELETE FROM `+coordinator.outboxTable()+` WHERE "causation_id" IN (`+strings.Join(marks, ",")+`)`, arguments...)
	if err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: delete facts: %w", err)
	}
	facts, err := deleted.RowsAffected()
	if err != nil || int(facts) != expectedFacts {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: causal fact count changed under row lock")
	}
	state, err := transaction.ExecContext(ctx, `DELETE FROM `+coordinator.deliveryTable()+` WHERE "causation_id" IN (`+strings.Join(marks, ",")+`) AND "status"='delivered'`, arguments...)
	if err != nil {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: delete delivery: %w", err)
	}
	states, err := state.RowsAffected()
	if err != nil || int(states) != len(selected) {
		return eventprovider.RetentionResult{}, fmt.Errorf("P7_POSTGRESQL_RETENTION: delivery ownership changed")
	}
	return eventprovider.RetentionResult{Causations: len(selected), Facts: int(facts)}, nil
}

func (coordinator *eventCoordinator) materializeMissing(ctx context.Context, queryer postgresqlEventQueryExecer, maximum int) (bool, error) {
	if maximum <= 0 || maximum > eventprovider.MaximumClaimGroups {
		return false, fmt.Errorf("P7_POSTGRESQL_DELIVERY: missing-state materialization limit is invalid")
	}
	statement := `INSERT INTO ` + coordinator.deliveryTable() + ` ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") SELECT o."causation_id",'pending',MIN(o."recorded_at"),0,MIN(o."recorded_at"),MIN(o."recorded_at") FROM ` + coordinator.outboxTable() + ` o LEFT JOIN ` + coordinator.deliveryTable() + ` d ON d."causation_id"=o."causation_id" WHERE d."causation_id" IS NULL GROUP BY o."causation_id" ORDER BY MIN(o."recorded_at"),o."causation_id" LIMIT $1 ON CONFLICT ("causation_id") DO NOTHING`
	result, err := queryer.ExecContext(ctx, statement, maximum)
	if err != nil {
		return false, fmt.Errorf("P7_POSTGRESQL_DELIVERY: materialize missing groups: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("P7_POSTGRESQL_DELIVERY: materialize missing result: %w", err)
	}
	if inserted >= int64(maximum) {
		return false, nil
	}
	var complete bool
	query := `SELECT NOT EXISTS (SELECT 1 FROM ` + coordinator.outboxTable() + ` o LEFT JOIN ` + coordinator.deliveryTable() + ` d ON d."causation_id"=o."causation_id" WHERE d."causation_id" IS NULL LIMIT 1)`
	if err := sqlx.GetContext(ctx, queryer, &complete, query); err != nil {
		return false, fmt.Errorf("P7_POSTGRESQL_DELIVERY: verify missing groups: %w", err)
	}
	return complete, nil
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

type postgresqlFactRow struct {
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

func (coordinator *eventCoordinator) readLeases(ctx context.Context, queryer sqlx.QueryerContext, causations []string) ([]eventprovider.Lease, error) {
	if len(causations) == 0 {
		return nil, nil
	}
	marks, arguments := postgresqlArguments(causations)
	var states []postgresqlDeliveryRow
	if err := sqlx.SelectContext(ctx, queryer, &states, `SELECT d.* FROM `+coordinator.deliveryTable()+` d WHERE d."causation_id" IN (`+strings.Join(marks, ",")+`)`, arguments...); err != nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: read claimed groups: %w", err)
	}
	stateByCausation := make(map[string]eventprovider.Delivery, len(states))
	for _, state := range states {
		stateByCausation[state.CausationID] = postgresqlDeliveryValue(state)
	}
	var stored []postgresqlFactRow
	if err := sqlx.SelectContext(ctx, queryer, &stored, `SELECT "event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at" FROM `+coordinator.outboxTable()+` WHERE "causation_id" IN (`+strings.Join(marks, ",")+`) ORDER BY "causation_id","transaction_ordinal"`, arguments...); err != nil {
		return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: read claimed facts: %w", err)
	}
	facts := make(map[string][]eventprovider.FactRow, len(causations))
	for _, row := range stored {
		facts[row.CausationID] = append(facts[row.CausationID], postgresqlFactValue(row))
	}
	result := make([]eventprovider.Lease, 0, len(causations))
	for _, causation := range causations {
		state, exists := stateByCausation[causation]
		if !exists {
			return nil, fmt.Errorf("P7_POSTGRESQL_DELIVERY: claimed group disappeared")
		}
		state.ImmutableFactRows = len(facts[causation])
		result = append(result, eventprovider.Lease{Delivery: state, Facts: facts[causation]})
	}
	return clonePostgreSQLLeases(result), nil
}

func (coordinator *eventCoordinator) readDelivery(ctx context.Context, queryer sqlx.QueryerContext, causation string) (eventprovider.Delivery, error) {
	var row postgresqlDeliveryRow
	query := `SELECT d.*,(SELECT COUNT(*) FROM ` + coordinator.outboxTable() + ` o WHERE o."causation_id"=d."causation_id") AS "fact_rows" FROM ` + coordinator.deliveryTable() + ` d WHERE d."causation_id"=$1`
	if err := sqlx.GetContext(ctx, queryer, &row, query, causation); err != nil {
		return eventprovider.Delivery{}, err
	}
	return postgresqlDeliveryValue(row), nil
}

func postgresqlFactValue(row postgresqlFactRow) eventprovider.FactRow {
	return eventprovider.FactRow{
		EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity,
		GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action,
		BeforeIdentity: append([]byte(nil), row.BeforeIdentity...), AfterIdentity: append([]byte(nil), row.AfterIdentity...), CausationID: row.CausationID,
		TransactionOrdinal: row.TransactionOrdinal, Metadata: append([]byte(nil), row.Metadata...), DeleteSnapshot: append([]byte(nil), row.DeleteSnapshot...),
		RecordedAt: row.RecordedAt.UTC().Truncate(time.Microsecond),
	}
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
	Causation string `db:"causation_id"`
	Facts     int    `db:"fact_rows"`
}

func postgresqlArguments(values []string) ([]string, []any) {
	marks := make([]string, len(values))
	arguments := make([]any, len(values))
	for index, value := range values {
		marks[index] = "$" + strconv.Itoa(index+1)
		arguments[index] = value
	}
	return marks, arguments
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
