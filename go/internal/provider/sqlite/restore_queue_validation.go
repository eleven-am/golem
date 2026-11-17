package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

// validateRestoreQueueRows requires an already-inspected queue table and a stable
// offline snapshot (the restore transaction). SQL checks bound every variable
// width value before any job bytes are returned to Go. Typed job decoding belongs
// to the application; this checks the provider's durable envelope only.
func validateRestoreQueueRows(ctx context.Context, query sqlx.QueryerContext) error {
	var checks []string
	var arguments []any
	text := func(column string, maximum int, optional bool) {
		expression := `typeof("` + column + `")='text' AND length(CAST("` + column + `" AS BLOB))<=?`
		if optional {
			expression = `"` + column + `" IS NULL OR (` + expression + `)`
		}
		checks = append(checks, `(`+expression+`)`)
		arguments = append(arguments, maximum)
	}
	text("id", queueprovider.MaximumIdentityBytes, false)
	text("type", queueprovider.MaximumKeyBytes, false)
	text("status", queueprovider.MaximumKeyBytes, false)
	text("lease_token", queueprovider.MaximumIdentityBytes, true)
	for _, column := range []string{"dedupe_key", "exclusive_key", "last_code", "resource_name"} {
		text(column, queueprovider.MaximumKeyBytes, true)
	}
	checks = append(checks, `typeof("payload")='blob' AND length("payload") BETWEEN 1 AND ?`)
	arguments = append(arguments, queueprovider.MaximumPayloadBytes)
	for _, column := range []string{"priority", "attempt_count", "max_attempts", "available_at", "enqueued_at", "updated_at"} {
		checks = append(checks, `typeof("`+column+`")='integer'`)
	}
	for _, column := range []string{"lease_until", "cancel_requested_at", "finished_at", "resource_cost", "resource_capacity"} {
		checks = append(checks, `( "`+column+`" IS NULL OR typeof("`+column+`")='integer')`)
	}
	var invalid bool
	if err := sqlx.GetContext(ctx, query, &invalid, `SELECT EXISTS(SELECT 1 FROM `+sqliteQueueTable+` WHERE NOT (`+strings.Join(checks, ` AND `)+`))`, arguments...); err != nil {
		return fmt.Errorf("sqlite restore queue bounds: %w", err)
	}
	if invalid {
		return fmt.Errorf("sqlite restore queue contains invalid SQL types or oversized values")
	}
	rows, err := query.QueryxContext(ctx, `SELECT `+sqliteQueueColumns+`,"resource_name","resource_cost","resource_capacity" FROM `+sqliteQueueTable)
	if err != nil {
		return fmt.Errorf("sqlite restore queue rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row struct {
			sqliteQueueRow
			ResourceName     sql.NullString `db:"resource_name"`
			ResourceCost     sql.NullInt64  `db:"resource_cost"`
			ResourceCapacity sql.NullInt64  `db:"resource_capacity"`
		}
		if err := rows.StructScan(&row); err != nil {
			return fmt.Errorf("sqlite restore queue row: %w", err)
		}
		request := queueprovider.EnqueueRequest{ID: row.ID, Priority: row.Priority, Type: row.Type, Payload: row.Payload, MaxAttempts: int(row.MaxAttempts), DedupeKey: row.DedupeKey.String, ExclusiveKey: row.ExclusiveKey.String}
		if int64(request.MaxAttempts) != row.MaxAttempts {
			return fmt.Errorf("sqlite restore queue max attempts overflows producer representation")
		}
		if err := queueprovider.ValidateEnqueue(request); err != nil {
			return fmt.Errorf("sqlite restore queue envelope: %w", err)
		}
		if err := queueprovider.ValidateCode(row.LastCode.String); err != nil {
			return fmt.Errorf("sqlite restore queue code: %w", err)
		}
		if row.AttemptCount < 0 {
			return fmt.Errorf("sqlite restore queue has negative attempt count")
		}
		if row.LeaseToken.Valid {
			if err := queueprovider.ValidateIdentity(row.ID, row.LeaseToken.String); err != nil {
				return fmt.Errorf("sqlite restore queue lease: %w", err)
			}
		}
		terminal := false
		switch queueprovider.State(row.Status) {
		case queueprovider.StatePending:
		case queueprovider.StateLeased:
			// Release and offline reconciliation intentionally retain finalizer jobs
			// as leased with an expired deadline and no token.
			if !row.LeaseUntil.Valid {
				return fmt.Errorf("sqlite restore queue leased job has no deadline")
			}
		case queueprovider.StateSucceeded, queueprovider.StateFailed, queueprovider.StateCanceled:
			terminal = true
		default:
			return fmt.Errorf("sqlite restore queue has unknown state")
		}
		if terminal != row.FinishedAt.Valid {
			return fmt.Errorf("sqlite restore queue finished time disagrees with state")
		}
		if row.Status != string(queueprovider.StateLeased) && (row.LeaseToken.Valid || row.LeaseUntil.Valid || row.ResourceName.Valid || row.ResourceCost.Valid || row.ResourceCapacity.Valid) {
			return fmt.Errorf("sqlite restore queue unleased job retains lease or resource")
		}
		if row.ResourceName.Valid || row.ResourceCost.Valid || row.ResourceCapacity.Valid {
			if !row.ResourceName.Valid || !row.ResourceCost.Valid || !row.ResourceCapacity.Valid || !row.LeaseToken.Valid {
				return fmt.Errorf("sqlite restore queue incomplete resource lease")
			}
			if err := queueprovider.ValidateClaimResource(queueprovider.ClaimResource{Name: row.ResourceName.String, Concurrency: row.ResourceCapacity.Int64, Costs: map[string]int64{row.Type: row.ResourceCost.Int64}}); err != nil {
				return fmt.Errorf("sqlite restore queue resource: %w", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite restore queue scan: %w", err)
	}
	return nil
}
