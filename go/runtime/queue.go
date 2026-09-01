package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/golem"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	queueworker "github.com/eleven-am/golem/go/internal/queue/worker"
	"github.com/eleven-am/golem/go/queue"
)

// QueueConfig enables the durable job queue for one application. The registry
// is the complete set of job types this process is willing to execute.
type QueueConfig struct {
	Registry                  *queue.Registry
	Limits                    queue.Limits
	Resources                 []queue.Resource
	SemanticReconcileInterval time.Duration
}

func (app *App[P, A]) initializeQueueRuntime(ctx context.Context, config *QueueConfig) error {
	if config == nil {
		return nil
	}
	if config.Registry == nil {
		return queue.Fail(queue.CodeConfigInvalid, "queue registry is required")
	}
	reconcileInterval := config.SemanticReconcileInterval
	if reconcileInterval != 0 && (reconcileInterval < time.Minute || reconcileInterval > 24*time.Hour || reconcileInterval%time.Microsecond != 0) {
		return queue.Fail(queue.CodeConfigInvalid, "semantic reconcile interval must be microsecond-exact and within 1m..24h")
	}
	store, err := app.queueStoreFor()
	if err != nil {
		return queue.Fail(queue.CodeConfigInvalid, "durable job storage is unavailable: %v", err)
	}
	registry := config.Registry.Clone()
	if len(app.semantic.IndexRefs()) > 0 {
		if err := app.registerSemanticJobs(registry); err != nil {
			return err
		}
	}
	worker, err := queueworker.New(store, registry, config.Limits, app.eventProvider, app.observer, config.Resources...)
	if err != nil {
		return err
	}
	operator, err := queueworker.NewOperator(store, app.eventProvider, app.observer)
	if err != nil {
		return err
	}
	if err := store.EnsureSchema(ctx); err != nil {
		return queue.Fail(queue.CodeStoreFailure, "create durable job storage: %v", err)
	}
	app.queueStore = store
	app.queueWorker = worker
	app.queueOperator = operator
	app.queueLimits = config.Limits.Resolved()
	app.semanticReconcileInterval = reconcileInterval
	return nil
}

func (app *App[P, A]) queueStoreFor() (queueprovider.Store, error) {
	namespace, ok := app.registry.PhysicalSystemNamespace(app.eventProvider)
	if !ok || namespace == "" {
		return nil, fmt.Errorf("queue system namespace is absent")
	}
	switch app.eventProvider {
	case golem.SQLite:
		if namespace != "main" {
			return nil, fmt.Errorf("SQLite queue system namespace is not main")
		}
		return sqliteprovider.New().QueueStore(app.database)
	case golem.PostgreSQL:
		return postgresprovider.New().QueueStoreAt(app.database, namespace)
	default:
		return nil, fmt.Errorf("queue provider is unsupported")
	}
}

// RunQueueWorker owns the durable job worker for the lifetime of ctx. Open
// performs no background work, and a second concurrent call is refused with
// CodeWorkerRunning. On cancellation, claimed-but-unstarted jobs are released
// immediately and running handlers are given the configured shutdown grace;
// because Go cannot kill a goroutine, a handler that ignores its context may
// still be running when this returns.
func (app *App[P, A]) RunQueueWorker(ctx context.Context) error {
	if app == nil || ctx == nil || app.queueWorker == nil {
		return queue.Fail(queue.CodeConfigInvalid, "queue worker is not configured")
	}
	if !app.queueRunning.CompareAndSwap(false, true) {
		return queue.Fail(queue.CodeWorkerRunning, "queue worker is already running")
	}
	defer app.queueRunning.Store(false)
	return app.queueWorker.Run(ctx)
}

// QueueOperator returns the durable job control surface, or nil when the queue
// is not configured.
func (app *App[P, A]) QueueOperator() queue.Operator {
	if app == nil {
		return nil
	}
	return app.queueOperator
}

// Enqueue durably records one job on the application's connection pool and
// nudges an idle in-process worker.
func (app *App[P, A]) Enqueue(ctx context.Context, pending queue.Pending) (queue.JobID, error) {
	stored, err := app.enqueueOn(ctx, nil, pending)
	if err != nil {
		return "", err
	}
	app.queueWorker.Wake()
	return queue.JobID(stored.ID), nil
}

// CallerTxEnqueue records one job on the caller transaction's executor, so the
// job row commits and rolls back with the domain write. The in-process worker
// is nudged when the transaction commits, never when the row is written.
func CallerTxEnqueue[P, A any](ctx context.Context, transaction *CallerTx[P, A], pending queue.Pending) (queue.JobID, error) {
	if transaction == nil || transaction.caller == nil {
		return "", queue.Fail(queue.CodeConfigInvalid, "caller transaction is unavailable")
	}
	return txEnqueue(ctx, transaction.caller.app, transaction.caller.executor, pending)
}

// SystemTxEnqueue is the unrestricted equivalent of CallerTxEnqueue.
func SystemTxEnqueue[P, A any](ctx context.Context, transaction *SystemTx[P, A], pending queue.Pending) (queue.JobID, error) {
	if transaction == nil || transaction.system.app == nil {
		return "", queue.Fail(queue.CodeConfigInvalid, "system transaction is unavailable")
	}
	return txEnqueue(ctx, transaction.system.app, transaction.system.executor, pending)
}

func txEnqueue[P, A any](ctx context.Context, app *App[P, A], binding *executionBinding, pending queue.Pending) (queue.JobID, error) {
	if app == nil || app.queueStore == nil {
		return "", queue.Fail(queue.CodeConfigInvalid, "queue is not configured")
	}
	executor, err := binding.transactionFor(app.database)
	if err != nil {
		return "", queue.Fail(queue.CodeConfigInvalid, "transactional enqueue requires a transaction-bound executor")
	}
	stored, err := app.enqueueOn(ctx, executor, pending)
	if err != nil {
		return "", err
	}
	binding.queueEnqueued(app.queueWorker.Wake)
	return queue.JobID(stored.ID), nil
}

func (app *App[P, A]) enqueueOn(ctx context.Context, executor queueprovider.Executor, pending queue.Pending) (queueprovider.EnqueueResult, error) {
	if app == nil || ctx == nil || app.queueStore == nil {
		return queueprovider.EnqueueResult{}, queue.Fail(queue.CodeConfigInvalid, "queue is not configured")
	}
	if pending.TypeName() == "" {
		return queueprovider.EnqueueResult{}, queue.Fail(queue.CodePayloadInvalid, "pending job was not constructed by a registered type")
	}
	payload := pending.Payload()
	if len(payload) > app.queueLimits.MaxPayloadBytes {
		return queueprovider.EnqueueResult{}, queue.Fail(queue.CodePayloadInvalid, "job type %q payload is %d bytes, above %d", pending.TypeName(), len(payload), app.queueLimits.MaxPayloadBytes)
	}
	identity, err := queueprovider.NewIdentifier()
	if err != nil {
		return queueprovider.EnqueueResult{}, queue.Fail(queue.CodeStoreFailure, "job identity: %v", err)
	}
	stored, err := app.queueStore.Enqueue(ctx, executor, queueprovider.EnqueueRequest{
		ID: identity, Type: pending.TypeName(), Payload: payload,
		MaxAttempts: pending.MaxAttempts(), Delay: pending.Delay().Truncate(time.Microsecond),
		DedupeKey: pending.DedupeKey(), ExclusiveKey: pending.ExclusiveKey(),
	})
	if err != nil {
		return queueprovider.EnqueueResult{}, queue.Fail(queue.CodeStoreFailure, "enqueue job type %q: %v", pending.TypeName(), err)
	}
	return stored, nil
}
