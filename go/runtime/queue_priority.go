package runtime

import (
	"context"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

// CallerTxPromoteJob promotes a pending job in the caller's transaction. The
// application must authorize its domain row and derive the job ID from that
// row in this transaction; knowing a job ID is not an authorization check.
// Successful changes wake the local worker only after commit.
func CallerTxPromoteJob[P, A any](ctx context.Context, transaction *CallerTx[P, A], id queue.JobID, priority queue.Priority, options ...queue.PromotionOption) (bool, error) {
	return transactionOperation(ctx, transaction.binding(), func(ctx context.Context) (bool, error) {
		if transaction == nil || transaction.caller == nil {
			return false, queue.Fail(queue.CodeConfigInvalid, "caller transaction is unavailable")
		}
		return txPromoteJob(ctx, transaction.caller.app, transaction.caller.executor, id, priority, options...)
	})
}

// SystemTxPromoteJob is the unrestricted transactional promotion entry point.
func SystemTxPromoteJob[P, A any](ctx context.Context, transaction *SystemTx[P, A], id queue.JobID, priority queue.Priority, options ...queue.PromotionOption) (bool, error) {
	return transactionOperation(ctx, transaction.binding(), func(ctx context.Context) (bool, error) {
		if transaction == nil || transaction.system.app == nil {
			return false, queue.Fail(queue.CodeConfigInvalid, "system transaction is unavailable")
		}
		return txPromoteJob(ctx, transaction.system.app, transaction.system.executor, id, priority, options...)
	})
}

func txPromoteJob[P, A any](ctx context.Context, app *App[P, A], binding *executionBinding, id queue.JobID, priority queue.Priority, options ...queue.PromotionOption) (bool, error) {
	if app == nil || app.queueStore == nil {
		return false, queue.Fail(queue.CodeConfigInvalid, "queue is not configured")
	}
	if err := binding.requireAdmitted(ctx); err != nil {
		return false, err
	}
	executor, err := binding.transactionFor(app.database)
	if err != nil {
		return false, queue.Fail(queue.CodeConfigInvalid, "transactional promotion requires a transaction-bound executor")
	}
	promotion, err := queue.ResolvePromotion(priority, options...)
	if err != nil {
		return false, err
	}
	if err := queueprovider.ValidateJobIdentity(string(id)); err != nil {
		return false, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	changed, err := app.queueStore.Promote(ctx, executor, string(id), int(promotion.Priority()), promotion.ReadyNow())
	if err != nil {
		if providerInterference(err) {
			return false, transactionConflict("promote job conflicted with another transaction", err)
		}
		return false, queue.Fail(queue.CodeStoreFailure, "promote job %s: %v", id, err)
	}
	if changed {
		binding.afterCommit(app.queueWorker.Wake)
	}
	return changed, nil
}
