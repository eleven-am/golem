package sqlite

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
	result, err := executor.ExecContext(ctx, `UPDATE `+sqliteQueueTable+` SET "priority"=?,"available_at"=CASE WHEN ? THEN MIN("available_at",`+sqliteDatabaseMicros+`) ELSE "available_at" END,"updated_at"=`+sqliteDatabaseMicros+` WHERE "id"=? AND "status"='pending' AND "priority"<=? AND ("priority"<? OR (? AND "available_at">`+sqliteDatabaseMicros+`))`, priority, readyNow, id, priority, priority, readyNow)
	if err != nil {
		return false, fmt.Errorf("QUEUE_SQLITE_STORE: promote pending job: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed != 0, err
}
