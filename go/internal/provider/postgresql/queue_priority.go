package postgresql

import (
	"context"
	"fmt"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

func (store *queueStore) Promote(ctx context.Context, executor queueprovider.Executor, id string, priority int, readyNow bool) (bool, error) {
	if err := queueprovider.ValidateJobIdentity(id); err != nil {
		return false, err
	}
	if err := queueprovider.ValidatePriority(priority); err != nil {
		return false, err
	}
	if executor == nil {
		executor = store.database
	}
	// Take database time after acquiring the row lock, as in Cancel. Promotion
	// and Claim therefore make one serial decision about pending ownership.
	result, err := executor.ExecContext(ctx, `WITH target AS MATERIALIZED (SELECT "id" FROM `+store.table()+` WHERE "id"=$1 FOR UPDATE), moment AS MATERIALIZED (SELECT clock_timestamp() AS "now" FROM target) UPDATE `+store.table()+` AS job SET "priority"=$2,"available_at"=CASE WHEN $3 THEN LEAST(job."available_at",moment."now") ELSE job."available_at" END,"updated_at"=moment."now" FROM target,moment WHERE job."id"=target."id" AND job."status"='pending' AND job."priority"<=$2 AND (job."priority"<$2 OR ($3 AND job."available_at">moment."now"))`, id, priority, readyNow)
	if err != nil {
		return false, fmt.Errorf("QUEUE_POSTGRESQL_STORE: promote pending job: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed != 0, err
}
