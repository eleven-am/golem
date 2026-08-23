package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

// executionBinding is the single SQL execution authority carried by a prepared
// operation. A binding is tied to the App database that created it, so a
// transaction-bound operation can never silently fall back to another App or
// to the App's connection pool.
type executionBinding struct {
	database     *sqlx.DB
	executor     sqlx.QueryerContext
	transaction  *sqlx.Tx
	active       atomic.Bool
	scoped       bool
	nextScope    atomic.Uint64
	invalidation atomic.Uint64
	stateMu      sync.Mutex
	state        *mutationState
	stateErr     error
	mutation     executionMutationConfig
	observation  *observeexec.Span
	observer     observe.Observer
	queueWake    atomic.Pointer[func()]
}

func (binding *executionBinding) queueEnqueued(wake func()) {
	if binding != nil && wake != nil {
		binding.queueWake.Store(&wake)
	}
}

func (binding *executionBinding) notifyQueue() {
	if binding == nil {
		return
	}
	if wake := binding.queueWake.Load(); wake != nil {
		(*wake)()
	}
}

type executionMutationConfig struct {
	enabled          bool
	provider         policyir.Provider
	limits           normalizedMutationLimits
	afterCommitError func(context.Context, golem.AfterCommitFailure)
	invalidate       func()
	outboxNamespace  string
	// flushSemantic writes the transaction's semantic marks and schedules their
	// drain on the same executor the outbox rows use. It is nil only for an
	// execution that owns no application.
	flushSemantic func(context.Context, sqlx.ExecerContext, []semanticMark) error
}

func databaseExecution(database *sqlx.DB) *executionBinding {
	binding := &executionBinding{database: database, executor: database}
	binding.active.Store(true)
	return binding
}

func transactionExecution(database *sqlx.DB, transaction *sqlx.Tx) *executionBinding {
	binding := &executionBinding{database: database, executor: transaction, transaction: transaction, scoped: true}
	binding.active.Store(true)
	return binding
}

func scopedExecution(database *sqlx.DB, executor sqlx.QueryerContext) *executionBinding {
	binding := &executionBinding{database: database, executor: executor, scoped: true}
	binding.active.Store(true)
	return binding
}

func (binding *executionBinding) queryerFor(database *sqlx.DB) (sqlx.QueryerContext, error) {
	if binding == nil || database == nil || binding.database != database || binding.executor == nil {
		return nil, fmt.Errorf("P4_RUNTIME_EXECUTOR: execution binding does not belong to this application")
	}
	if !binding.active.Load() {
		return nil, fmt.Errorf("P4_RUNTIME_EXECUTOR: transaction execution has ended")
	}
	return observingQueryer{inner: binding.executor, transaction: binding.observation}, nil
}

type observingQueryer struct {
	inner       sqlx.QueryerContext
	transaction *observeexec.Span
}

func (queryer observingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	observeexec.RecordStatement(ctx, queryer.transaction)
	return queryer.inner.QueryContext(ctx, query, args...)
}

func (queryer observingQueryer) QueryxContext(ctx context.Context, query string, args ...any) (*sqlx.Rows, error) {
	observeexec.RecordStatement(ctx, queryer.transaction)
	return queryer.inner.QueryxContext(ctx, query, args...)
}

func (queryer observingQueryer) QueryRowxContext(ctx context.Context, query string, args ...any) *sqlx.Row {
	observeexec.RecordStatement(ctx, queryer.transaction)
	return queryer.inner.QueryRowxContext(ctx, query, args...)
}

func (queryer observingQueryer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	observeexec.RecordStatement(ctx, queryer.transaction)
	execer, ok := queryer.inner.(sqlx.ExecerContext)
	if !ok {
		return nil, fmt.Errorf("P8_OBSERVE_EXECUTOR: query executor cannot execute statements")
	}
	return execer.ExecContext(ctx, query, args...)
}

func recordQueryerStatement(ctx context.Context, queryer sqlx.QueryerContext, additional ...*observeexec.Span) {
	if _, alreadyObserved := queryer.(observingQueryer); !alreadyObserved {
		observeexec.RecordStatement(ctx, additional...)
	}
}

// transactionFor is the future mutation-kernel seam. Runtime mutation entry
// points must request this transaction rather than accepting an arbitrary SQL
// executor or reaching back to App.database.
func (binding *executionBinding) transactionFor(database *sqlx.DB) (*sqlx.Tx, error) {
	if _, err := binding.queryerFor(database); err != nil {
		return nil, err
	}
	if binding.transaction == nil {
		return nil, fmt.Errorf("P4_RUNTIME_EXECUTOR: operation requires a transaction-bound executor")
	}
	return binding.transaction, nil
}

func (binding *executionBinding) close() {
	if binding != nil && binding.scoped {
		binding.active.Store(false)
	}
}

func (binding *executionBinding) nextSavepoint() (uint64, error) {
	if binding == nil || binding.transaction == nil || !binding.active.Load() {
		return 0, fmt.Errorf("P4_RUNTIME_EXECUTOR: active transaction is required for savepoint")
	}
	value := binding.nextScope.Add(1)
	if value == 0 {
		return 0, fmt.Errorf("P4_RUNTIME_EXECUTOR: savepoint identity exhausted")
	}
	return value, nil
}

// invalidationEpoch is the execution-wide cache generation. P3 currently
// retains no cross-operation loader or decision cache; future caches must
// snapshot this value and clear or rebuild when it changes.
func (binding *executionBinding) invalidationEpoch() uint64 {
	if binding == nil {
		return 0
	}
	return binding.invalidation.Load()
}

func (binding *executionBinding) invalidateExecution() {
	if binding != nil {
		binding.invalidation.Add(1)
	}
}

func (binding *executionBinding) enableMutation(config executionMutationConfig) error {
	if binding == nil || !config.enabled {
		return fmt.Errorf("P4_MUTATION_STATE: execution mutation configuration is unavailable")
	}
	binding.stateMu.Lock()
	defer binding.stateMu.Unlock()
	if binding.state != nil || binding.stateErr != nil {
		return binding.stateErr
	}
	binding.mutation = config
	binding.state, binding.stateErr = newMutationState(config.limits, [16]byte{})
	if binding.stateErr == nil && config.invalidate != nil {
		binding.stateErr = binding.state.setInvalidation(config.invalidate)
	}
	return binding.stateErr
}

func (binding *executionBinding) mutationState() (*mutationState, error) {
	if binding == nil {
		return nil, fmt.Errorf("P4_MUTATION_STATE: execution binding is unavailable")
	}
	binding.stateMu.Lock()
	defer binding.stateMu.Unlock()
	if binding.stateErr != nil {
		return nil, binding.stateErr
	}
	if binding.state == nil {
		return nil, fmt.Errorf("P4_MUTATION_STATE: execution has no outer mutation state")
	}
	return binding.state, nil
}

func (binding *executionBinding) discardMutation() {
	if binding == nil {
		return
	}
	binding.stateMu.Lock()
	state := binding.state
	binding.stateMu.Unlock()
	state.discarded()
}

func mutationConfig[P, A any](app *App[P, A], execution *executionBinding) executionMutationConfig {
	if app == nil {
		return executionMutationConfig{}
	}
	publicProvider := golem.SQLite
	if app.provider == policyir.ProviderPostgreSQL {
		publicProvider = golem.PostgreSQL
	}
	namespace, _ := app.registry.PhysicalSystemNamespace(publicProvider)
	return executionMutationConfig{
		enabled: true, provider: app.provider, limits: app.mutationLimits,
		afterCommitError: app.afterCommitError, invalidate: execution.invalidateExecution,
		outboxNamespace: string(namespace), flushSemantic: app.flushSemanticMarks,
	}
}

// CallerTx is the opaque caller authorization and transaction capability used
// by generated transaction clients. Its fields deliberately expose neither
// the App database nor the underlying sqlx transaction.
type CallerTx[P, A any] struct {
	caller *Caller[P, A]
}

// SystemTx is the opaque unrestricted transaction capability used by
// generated system transaction clients.
type SystemTx[P, A any] struct {
	system    System[P, A]
	execution uint64
}

// undoOnPanic is the single owner of the runtime's panic contract for
// transaction-owning execution. A recovered panic performs exactly the site's
// own undo work and then re-raises the original value; a programmer panic is
// never converted into a public error. It must be deferred directly so that
// recover observes the panicking frame.
func undoOnPanic(undo func()) {
	if recovered := recover(); recovered != nil {
		undo()
		panic(recovered)
	}
}

// finishObservationOrPanic is the single owner of outcome finalization for the
// two public transaction entry points. A recovered panic closes the span as a
// panic failure and re-raises; an ordinary return closes it against err.
func finishObservationOrPanic(observation *observeexec.Span, observer *observeexec.DeferredObserver, err *error) {
	observer.Flush()
	if recovered := recover(); recovered != nil {
		observeexec.Finish(observation, observe.OutcomeFailure, observe.ReasonPanic)
		panic(recovered)
	}
	finishObservation(observation, *err)
}

// CallerTransaction owns the outer transaction around one generated caller
// callback. The callback is invoked exactly once; this function never retries
// application code.
func CallerTransaction[P, A any](ctx context.Context, caller *Caller[P, A], callback func(*CallerTx[P, A]) error) (err error) {
	if caller == nil || caller.app == nil || caller.policies == nil || caller.execution == 0 {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: caller execution is unavailable")
	}
	if ctx == nil || callback == nil {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: context and callback are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, observation := beginObservation(ctx, caller.app, golem.ModelID{}, observe.KindTransaction, observe.OperationCallerTransaction)
	deferredObserver := observeexec.NewDeferredObserver(caller.app.observer)
	defer finishObservationOrPanic(observation, deferredObserver, &err)
	transaction, err := caller.app.database.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: begin caller transaction: %w", err)
	}
	binding := transactionExecution(caller.app.database, transaction)
	binding.observation = observation
	binding.observer = deferredObserver
	if err := binding.enableMutation(mutationConfig(caller.app, caller.executor)); err != nil {
		binding.close()
		return rollbackTransaction(transaction, err)
	}
	transactionCaller := &Caller[P, A]{
		app:       caller.app,
		policies:  caller.policies,
		actor:     caller.actor,
		execution: caller.execution,
		executor:  binding,
		auditID:   caller.auditID,
	}
	capability := &CallerTx[P, A]{caller: transactionCaller}
	return finishTransaction(ctx, transaction, binding, func() error { return callback(capability) })
}

// SystemTransaction owns the outer transaction around one generated system
// callback. It does not construct caller policy or make caller hooks available.
func SystemTransaction[P, A any](ctx context.Context, system System[P, A], callback func(*SystemTx[P, A]) error) (err error) {
	if system.app == nil {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: system capability is unavailable")
	}
	if ctx == nil || callback == nil {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: context and callback are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, observation := beginObservation(ctx, system.app, golem.ModelID{}, observe.KindTransaction, observe.OperationSystemTransaction)
	deferredObserver := observeexec.NewDeferredObserver(system.app.observer)
	defer finishObservationOrPanic(observation, deferredObserver, &err)
	transaction, err := system.app.database.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: begin system transaction: %w", err)
	}
	binding := transactionExecution(system.app.database, transaction)
	binding.observation = observation
	binding.observer = deferredObserver
	if err := binding.enableMutation(mutationConfig(system.app, system.executor)); err != nil {
		binding.close()
		return rollbackTransaction(transaction, err)
	}
	execution := system.app.nextExecution.Add(1)
	if execution == 0 {
		binding.close()
		return rollbackTransaction(transaction, fmt.Errorf("P6_RUNTIME_EXECUTION: execution identity exhausted"))
	}
	capability := &SystemTx[P, A]{system: System[P, A]{app: system.app, executor: binding}, execution: execution}
	return finishTransaction(ctx, transaction, binding, func() error { return callback(capability) })
}

func finishTransaction(ctx context.Context, transaction *sqlx.Tx, binding *executionBinding, callback func() error) (err error) {
	defer binding.close()
	defer undoOnPanic(func() {
		binding.discardMutation()
		_ = transaction.Rollback()
	})

	if callbackErr := callback(); callbackErr != nil {
		binding.discardMutation()
		return rollbackTransaction(transaction, callbackErr)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		binding.discardMutation()
		return rollbackTransaction(transaction, contextErr)
	}
	if err := flushMutationBinding(ctx, transaction, binding); err != nil {
		binding.discardMutation()
		return rollbackTransaction(transaction, err)
	}
	if commitErr := transaction.Commit(); commitErr != nil {
		binding.discardMutation()
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: commit: %w", commitErr)
	}
	commitMutationBinding(ctx, binding)
	binding.notifyQueue()
	return nil
}

func flushMutationBinding(ctx context.Context, executor sqlx.ExecerContext, binding *executionBinding) error {
	state, err := binding.mutationState()
	if err != nil {
		return err
	}
	return state.flush(ctx, executor, binding.mutation)
}

func commitMutationBinding(ctx context.Context, binding *executionBinding) {
	state, err := binding.mutationState()
	if err != nil {
		return
	}
	state.committed(ctx, binding.mutation.afterCommitError)
}

func rollbackTransaction(transaction *sqlx.Tx, cause error) error {
	if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		return errors.Join(cause, fmt.Errorf("P4_RUNTIME_TRANSACTION: rollback: %w", rollbackErr))
	}
	return cause
}

// sqliteImmediateConnection is the small ownership surface shared by the
// standalone SQLite mutation boundaries. Once COMMIT succeeds, a later driver
// Close error cannot truthfully be reported as a failed mutation: the database
// change is already durable and retrying it could duplicate the write.
type sqliteImmediateConnection interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Close() error
}

func commitSQLiteImmediate(ctx context.Context, connection sqliteImmediateConnection, committed func()) error {
	if connection == nil {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: SQLite connection is unavailable")
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	if committed != nil {
		committed()
	}
	// COMMIT is the operation's durable success boundary. Close is cleanup and
	// must not turn that success into an ordinary retryable operation error.
	_ = connection.Close()
	return nil
}

// CallerTxFindMany is the transaction-bound equivalent of CallerFindMany.
// Generated Tx model clients call this seam; it preserves caller hooks and
// policy isolation while forcing every statement through the bound sqlx.Tx.
func CallerTxFindMany[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) ([]golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
	}
	return CallerFindMany(ctx, transaction.caller, descriptor, options...)
}

func CallerTxFindFirst[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (golem.Row[M], bool, error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, false, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
	}
	return CallerFindFirst(ctx, transaction.caller, descriptor, options...)
}

func CallerTxFindUnique[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], selector golem.UniqueSelectorValue[M], options ...golem.ReadOption[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
	}
	return CallerFindUnique(ctx, transaction.caller, descriptor, selector, options...)
}

func CallerTxCount[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (int64, error) {
	if transaction == nil || transaction.caller == nil {
		return 0, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
	}
	return CallerCount(ctx, transaction.caller, descriptor, options...)
}

func SystemTxFindMany[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) ([]golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
	}
	return SystemFindMany(ctx, transaction.system, descriptor, options...)
}

func SystemTxFindFirst[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (golem.Row[M], bool, error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, false, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
	}
	return SystemFindFirst(ctx, transaction.system, descriptor, options...)
}

func SystemTxFindUnique[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], selector golem.UniqueSelectorValue[M], options ...golem.ReadOption[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
	}
	return SystemFindUnique(ctx, transaction.system, descriptor, selector, options...)
}

func SystemTxCount[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (int64, error) {
	if transaction == nil || transaction.system.app == nil {
		return 0, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
	}
	return SystemCount(ctx, transaction.system, descriptor, options...)
}
