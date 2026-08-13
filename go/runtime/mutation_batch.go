package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

// CallerUpdateMany executes one bounded, exact-set authorized update. Planning,
// policy classification, provider capability checks, and SQL rendering all
// complete before transaction acquisition.
func CallerUpdateMany[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M], input golem.UpdateManyInput[M]) (count int64, resultErr error) {
	if caller == nil || caller.app == nil {
		return 0, golem.RuntimeOperationError(golem.CodeUnauthenticated, "updateMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if err := refuseLegacyVersionedMutation(caller.app.registry, descriptor.Metadata().ModelID(), "updateMany"); err != nil {
		return 0, err
	}
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), observe.KindMutation, observe.OperationMutationUpdateMany)
	defer func() {
		if observation != nil {
			observation.SetAggregateCount(count)
		}
		finishDeferredObservation(observation, deferredObservation, resultErr)
	}()
	predicate, err := where.Freeze(descriptor)
	if err != nil {
		return 0, publicBatchPreparationError(mutationir.UpdateMany, descriptor.Metadata().ModelID(), err)
	}
	frozenInput, err := golem.RuntimeFreezeUpdateManyInput(input)
	if err != nil {
		return 0, publicBatchPreparationError(mutationir.UpdateMany, descriptor.Metadata().ModelID(), err)
	}
	program, err := prepareCallerBatchHooks(ctx, caller, descriptor, golem.RuntimeUpdateManyMutationHookRequest(descriptor.Metadata().ModelID(), predicate, frozenInput))
	if err != nil {
		return 0, publicBatchPreparationError(mutationir.UpdateMany, descriptor.Metadata().ModelID(), err)
	}
	hooks := callerMutationHookExecution[A]{
		bindings: caller.app.bindings,
		actor:    caller.actor,
		executor: func(binding *executionBinding) golem.HookExecutor {
			return newCallerHookExecutor(caller, binding)
		},
	}
	return executePublicBatch(ctx, caller.app, caller.executor, program, &hooks)
}

func CallerDeleteMany[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M]) (count int64, resultErr error) {
	if caller == nil || caller.app == nil {
		return 0, golem.RuntimeOperationError(golem.CodeUnauthenticated, "deleteMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if err := refuseLegacyVersionedMutation(caller.app.registry, descriptor.Metadata().ModelID(), "deleteMany"); err != nil {
		return 0, err
	}
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), observe.KindMutation, observe.OperationMutationDeleteMany)
	defer func() {
		if observation != nil {
			observation.SetAggregateCount(count)
		}
		finishDeferredObservation(observation, deferredObservation, resultErr)
	}()
	predicate, err := where.Freeze(descriptor)
	if err != nil {
		return 0, publicBatchPreparationError(mutationir.DeleteMany, descriptor.Metadata().ModelID(), err)
	}
	program, err := prepareCallerBatchHooks(ctx, caller, descriptor, golem.RuntimeDeleteManyMutationHookRequest(descriptor.Metadata().ModelID(), predicate))
	if err != nil {
		return 0, publicBatchPreparationError(mutationir.DeleteMany, descriptor.Metadata().ModelID(), err)
	}
	hooks := callerMutationHookExecution[A]{
		bindings: caller.app.bindings,
		actor:    caller.actor,
		executor: func(binding *executionBinding) golem.HookExecutor {
			return newCallerHookExecutor(caller, binding)
		},
	}
	return executePublicBatch(ctx, caller.app, caller.executor, program, &hooks)
}

func SystemUpdateMany[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M], input golem.UpdateManyInput[M]) (count int64, resultErr error) {
	if system.app == nil {
		return 0, golem.RuntimeOperationError(golem.CodeBadUserInput, "updateMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "system execution is unavailable", nil)
	}
	if err := refuseLegacyVersionedMutation(system.app.registry, descriptor.Metadata().ModelID(), "updateMany"); err != nil {
		return 0, err
	}
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), observe.KindMutation, observe.OperationMutationUpdateMany)
	defer func() {
		if observation != nil {
			observation.SetAggregateCount(count)
		}
		finishDeferredObservation(observation, deferredObservation, resultErr)
	}()
	program, err := prepareBatchProgram(system.app, nil, mutationir.System, mutationir.UpdateMany, descriptor, where, &input)
	if err != nil {
		return 0, publicBatchPreparationError(mutationir.UpdateMany, descriptor.Metadata().ModelID(), err)
	}
	return executePublicBatch[P, A](ctx, system.app, system.executor, program, nil)
}

func SystemDeleteMany[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M]) (count int64, resultErr error) {
	if system.app == nil {
		return 0, golem.RuntimeOperationError(golem.CodeBadUserInput, "deleteMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "system execution is unavailable", nil)
	}
	if err := refuseLegacyVersionedMutation(system.app.registry, descriptor.Metadata().ModelID(), "deleteMany"); err != nil {
		return 0, err
	}
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), observe.KindMutation, observe.OperationMutationDeleteMany)
	defer func() {
		if observation != nil {
			observation.SetAggregateCount(count)
		}
		finishDeferredObservation(observation, deferredObservation, resultErr)
	}()
	program, err := prepareBatchProgram[P, A, M](system.app, nil, mutationir.System, mutationir.DeleteMany, descriptor, where, nil)
	if err != nil {
		return 0, publicBatchPreparationError(mutationir.DeleteMany, descriptor.Metadata().ModelID(), err)
	}
	return executePublicBatch[P, A](ctx, system.app, system.executor, program, nil)
}

func CallerTxUpdateMany[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M], input golem.UpdateManyInput[M]) (int64, error) {
	if transaction == nil || transaction.caller == nil {
		return 0, golem.RuntimeOperationError(golem.CodeBadUserInput, "updateMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerUpdateMany(ctx, transaction.caller, descriptor, where, input)
}

func CallerTxDeleteMany[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M]) (int64, error) {
	if transaction == nil || transaction.caller == nil {
		return 0, golem.RuntimeOperationError(golem.CodeBadUserInput, "deleteMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerDeleteMany(ctx, transaction.caller, descriptor, where)
}

func SystemTxUpdateMany[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M], input golem.UpdateManyInput[M]) (int64, error) {
	if transaction == nil || transaction.system.app == nil {
		return 0, golem.RuntimeOperationError(golem.CodeBadUserInput, "updateMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemUpdateMany(ctx, transaction.system, descriptor, where, input)
}

func SystemTxDeleteMany[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], where golem.Predicate[M]) (int64, error) {
	if transaction == nil || transaction.system.app == nil {
		return 0, golem.RuntimeOperationError(golem.CodeBadUserInput, "deleteMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemDeleteMany(ctx, transaction.system, descriptor, where)
}

func prepareBatchProgram[P, A, M any](app *App[P, A], policies mutationplan.PolicySet, stance mutationir.Stance, operation mutationir.Operation, descriptor golem.ModelDescriptor[M], where golem.Predicate[M], input *golem.UpdateManyInput[M]) (mutationbatch.Program, error) {
	if app == nil {
		return mutationbatch.Program{}, fmt.Errorf("batch application is unavailable")
	}
	frozenPredicate, err := where.Freeze(descriptor)
	if err != nil {
		return mutationbatch.Program{}, err
	}
	var frozenInput *golem.FrozenMutationInput
	if input != nil {
		value, freezeErr := golem.RuntimeFreezeUpdateManyInput(*input)
		if freezeErr != nil {
			return mutationbatch.Program{}, freezeErr
		}
		frozenInput = &value
	}
	return prepareFrozenBatchProgram(app, policies, stance, operation, descriptor.Metadata().ModelID(), frozenPredicate, frozenInput, newMutationRuntimeValues())
}

func prepareCallerBatchHooks[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.RuntimeMutationHookRequest) (mutationbatch.Program, error) {
	return prepareCallerFrozenBatchHooks(ctx, caller, descriptor.Metadata().ModelID(), request)
}

func prepareCallerFrozenBatchHooks[P, A any](ctx context.Context, caller *Caller[P, A], model golem.ModelID, request golem.RuntimeMutationHookRequest) (mutationbatch.Program, error) {
	var program mutationbatch.Program
	runtimeValues := newMutationRuntimeValues()
	validate := func(transformed golem.RuntimeMutationHookRequest) error {
		predicate, input, err := batchRequestParts(transformed)
		if err != nil {
			return err
		}
		prepared, err := prepareFrozenBatchProgram(caller.app, caller.policies, mutationir.Caller, hookMutationOperation(transformed.Operation()), model, predicate, input, runtimeValues)
		if err == nil {
			program = prepared
		}
		return err
	}
	hookContext := golem.RuntimeContextWithActor(ctx, caller.actor)
	hookContext, hookObservation := observeexec.BeginChild(hookContext, model, observe.KindHook, hookObservationOperation(request.Operation()), observe.PhaseBefore)
	transformed, err := golem.RuntimeInvokeMutationBeforeHooks(hookContext, caller.app.bindings, request, validate)
	finishObservation(hookObservation, err)
	if err != nil {
		return mutationbatch.Program{}, err
	}
	if err := validate(transformed); err != nil {
		return mutationbatch.Program{}, err
	}
	return program, nil
}

func batchRequestParts(request golem.RuntimeMutationHookRequest) (golem.FrozenPredicate, *golem.FrozenMutationInput, error) {
	predicate, ok := request.Predicate()
	if !ok {
		return golem.FrozenPredicate{}, nil, fmt.Errorf("batch hook request predicate is absent")
	}
	switch request.Operation() {
	case golem.HookUpdateMany:
		input, ok := request.Input()
		if !ok {
			return golem.FrozenPredicate{}, nil, fmt.Errorf("update-many hook request input is absent")
		}
		return predicate, &input, nil
	case golem.HookDeleteMany:
		if _, ok := request.Input(); ok {
			return golem.FrozenPredicate{}, nil, fmt.Errorf("delete-many hook request has an input")
		}
		return predicate, nil, nil
	default:
		return golem.FrozenPredicate{}, nil, fmt.Errorf("hook request is not a batch operation")
	}
}

func hookMutationOperation(operation golem.HookOperation) mutationir.Operation {
	if operation == golem.HookUpdateMany {
		return mutationir.UpdateMany
	}
	if operation == golem.HookDeleteMany {
		return mutationir.DeleteMany
	}
	return 0
}

func prepareFrozenBatchProgram[P, A any](app *App[P, A], policies mutationplan.PolicySet, stance mutationir.Stance, operation mutationir.Operation, model golem.ModelID, frozenPredicate golem.FrozenPredicate, input *golem.FrozenMutationInput, runtimeValues *mutationRuntimeValues) (mutationbatch.Program, error) {
	predicate, err := mutationbind.BatchPredicate(frozenPredicate, model, app.registry)
	if err != nil {
		return mutationbatch.Program{}, err
	}
	bounds, err := mutationir.NewStatementBounds(uint32(app.mutationLimits.statementParameters), uint32(app.mutationLimits.touchedRows))
	if err != nil {
		return mutationbatch.Program{}, err
	}
	request := mutationplan.RootRequest{
		Stance: stance, Operation: operation, Model: policyir.ModelID(model),
		Registry: app.registry, Policies: policies, Predicate: &predicate,
		Retry: mutationir.NoRetry, Bounds: bounds,
	}
	if stance == mutationir.Caller {
		request.Hooks = mutationHookInventory(app.bindings, model)
	}
	modelFact, _ := app.registry.Model(model)
	if modelFact.SubscriptionsEnabled() {
		factCodec, eventSchema, snapshot, err := mutationEventSchema(app.registry, policyir.ModelID(model))
		if err != nil {
			return mutationbatch.Program{}, err
		}
		request.CaptureFacts, request.FactCodec, request.EventSchema = true, &factCodec, eventSchema
		if operation == mutationir.DeleteMany {
			request.PrivateDeleteSnapshot = snapshot
		}
	}
	if operation == mutationir.UpdateMany {
		if input == nil {
			return mutationbatch.Program{}, fmt.Errorf("update-many input is absent")
		}
		bound, bindErr := mutationbind.UpdateManyInput(*input, app.registry)
		if bindErr != nil {
			return mutationbatch.Program{}, bindErr
		}
		if runtimeValues == nil {
			runtimeValues = newMutationRuntimeValues()
		}
		bound, bindErr = runtimeValues.apply(bound, app.registry)
		if bindErr != nil {
			return mutationbatch.Program{}, bindErr
		}
		request.Update = &bound
	} else if operation != mutationir.DeleteMany || input != nil {
		return mutationbatch.Program{}, fmt.Errorf("batch operation shape is invalid")
	}
	plan, err := mutationplan.BuildRoot(request)
	if err != nil {
		return mutationbatch.Program{}, err
	}
	return mutationbatch.Render(plan, app.registry, app.provider, app.capabilities)
}

type batchExecutionScope struct {
	queryer sqlx.QueryerContext
	execer  sqlx.ExecerContext
	binding *executionBinding
	finish  func(context.Context) error
	abort   func() error
}

// batchAfterCaptureObserver is a deliberately private execution seam used by
// the provider acceptance suite to coordinate a genuinely independent writer
// after the exact target set has been captured. Production callers cannot set
// it through the public API.
type batchAfterCaptureObserver func(context.Context) error

type batchAfterCaptureObserverContextKey struct{}

func contextWithBatchAfterCaptureObserver(ctx context.Context, observer batchAfterCaptureObserver) context.Context {
	return context.WithValue(ctx, batchAfterCaptureObserverContextKey{}, observer)
}

func invokeBatchAfterCaptureObserver(ctx context.Context) error {
	observer, _ := ctx.Value(batchAfterCaptureObserverContextKey{}).(batchAfterCaptureObserver)
	if observer == nil {
		return nil
	}
	return observer(ctx)
}

func executePublicBatch[P, A any](ctx context.Context, app *App[P, A], binding *executionBinding, program mutationbatch.Program, hooks *callerMutationHookExecution[A]) (count int64, err error) {
	if ctx == nil || app == nil || binding == nil {
		return 0, golem.RuntimeOperationError(golem.CodeBadUserInput, batchOperationName(program.Operation()), programModel(program), golem.FieldID{}, "batch execution is unavailable", nil)
	}
	scope, err := beginBatchExecution(ctx, app.database, app.provider, binding, program.TransactionRequirement())
	if err != nil {
		return 0, publicBatchExecutionError(program, err)
	}
	state, stateScope, outerState, err := beginBatchMutationState(app, binding)
	if err != nil {
		_ = scope.abort()
		return 0, publicBatchExecutionError(program, err)
	}
	activeBinding := scope.binding
	if activeBinding == nil {
		activeBinding = binding
	}
	if activeBinding != binding {
		activeBinding.stateMu.Lock()
		activeBinding.state = state
		activeBinding.mutation = mutationConfig(app, binding)
		activeBinding.stateMu.Unlock()
	}
	done := false
	defer func() {
		if recovered := recover(); recovered != nil {
			abortErr := scope.abort()
			rollbackErr := stateScope.rollback()
			if abortErr != nil || rollbackErr != nil {
				state.poison(errors.Join(abortErr, rollbackErr))
			}
			panic(recovered)
		}
		if !done {
			abortErr := scope.abort()
			rollbackErr := stateScope.rollback()
			if abortErr != nil || rollbackErr != nil {
				state.poison(errors.Join(err, abortErr, rollbackErr))
			}
		}
	}()

	captured, _, err := executeMutationBatchStatement(ctx, scope.queryer, app.registry, app.provider, program.ModelID(), program.CaptureStatement(), program.SentinelRows())
	if err != nil {
		return 0, publicBatchExecutionError(program, err)
	}
	prepared, err := program.PrepareCaptured(captured)
	if err != nil {
		return 0, publicBatchExecutionError(program, err)
	}
	if err := invokeBatchAfterCaptureObserver(ctx); err != nil {
		return 0, publicBatchExecutionError(program, err)
	}
	var authorized []mutationbatch.AuthorizedRow
	var applied, after []mutationdecode.Row
	for _, statement := range prepared.Statements() {
		rows, grants, executeErr := executeMutationBatchStatement(ctx, scope.queryer, app.registry, app.provider, program.ModelID(), statement, statement.ExpectedRows())
		if executeErr != nil {
			return 0, publicBatchExecutionError(program, executeErr)
		}
		switch statement.Role() {
		case mutationbatch.AuthorizePreImage:
			for index, row := range rows {
				authorizedRow, authorizedErr := mutationbatch.NewAuthorizedRow(row, grants[index]...)
				if authorizedErr != nil {
					return 0, publicBatchExecutionError(program, authorizedErr)
				}
				authorized = append(authorized, authorizedRow)
			}
		case mutationbatch.ApplyUpdate, mutationbatch.ApplyDelete:
			applied = append(applied, rows...)
		case mutationbatch.RehydrateAfterImage:
			after = append(after, rows...)
		default:
			return 0, publicBatchExecutionError(program, fmt.Errorf("unknown prepared batch statement role %d", statement.Role()))
		}
	}
	verification, err := prepared.VerifyAuthorized(authorized, applied, after, 0)
	if err != nil {
		return 0, publicBatchExecutionError(program, err)
	}
	if hooks != nil {
		operation, ok := mutationHookOperation(program.Operation())
		if !ok {
			return 0, publicBatchExecutionError(program, fmt.Errorf("batch operation has no hook family"))
		}
		model := golem.ModelID(program.ModelID())
		result := golem.RuntimeDeleteManyMutationHookResult(model, verification.Count())
		if operation == golem.HookUpdateMany {
			result = golem.RuntimeUpdateManyMutationHookResult(model, verification.Count())
		}
		if hooks.executor != nil {
			result = golem.RuntimeMutationHookResultWithExecutor(result, hooks.executor(activeBinding))
		}
		if hasMutationHook(hooks.bindings, model, operation, golem.HookAfter) {
			if err := invokeMutationResultHooks(ctx, hooks.bindings, hooks.actor, result, golem.HookAfter); err != nil {
				return 0, publicBatchExecutionError(program, err)
			}
		}
		if hasMutationHook(hooks.bindings, model, operation, golem.HookAfterCommit) {
			afterCommitResult := golem.RuntimeMutationHookResultWithoutExecutor(result)
			if err := state.addAfterCommit(operation, model, func(commitContext context.Context) error {
				return invokeMutationResultHooks(commitContext, hooks.bindings, hooks.actor, afterCommitResult, golem.HookAfterCommit)
			}); err != nil {
				return 0, publicBatchExecutionError(program, err)
			}
		}
	}
	if err := state.touch(int(verification.Count())); err != nil {
		return 0, publicBatchExecutionError(program, err)
	}
	requirement := prepared.FactRequirement()
	recordedAt := time.Now()
	for _, fact := range verification.Facts() {
		before := fact.Before()
		var after *mutationdecode.Row
		if value, present := fact.After(); present {
			after = &value
		}
		if _, err := state.buildFact(app.registry, requirement, &before, after, recordedAt); err != nil {
			return 0, publicBatchExecutionError(program, err)
		}
	}
	if !outerState {
		publicProvider := golem.SQLite
		if app.provider == policyir.ProviderPostgreSQL {
			publicProvider = golem.PostgreSQL
		}
		namespace, _ := app.registry.PhysicalSystemNamespace(publicProvider)
		if err := state.flush(ctx, scope.execer, app.provider, string(namespace)); err != nil {
			return 0, publicBatchExecutionError(program, err)
		}
	}
	if err := scope.finish(ctx); err != nil {
		return 0, publicBatchExecutionError(program, err)
	}
	if err := stateScope.release(); err != nil {
		state.poison(err)
		return 0, publicBatchExecutionError(program, err)
	}
	if !outerState {
		state.committed(ctx, app.afterCommitError)
	}
	done = true
	return verification.Count(), nil
}

func beginBatchMutationState[P, A any](app *App[P, A], binding *executionBinding) (*mutationState, *mutationScope, bool, error) {
	if app == nil || binding == nil {
		return nil, nil, false, fmt.Errorf("batch mutation state is unavailable")
	}
	if binding.transaction != nil {
		state, err := binding.mutationState()
		if err != nil {
			return nil, nil, true, err
		}
		scope, err := state.beginScope()
		return state, scope, true, err
	}
	state, err := newMutationState(app.mutationLimits, [16]byte{})
	if err != nil {
		return nil, nil, false, err
	}
	if err := state.setInvalidation(binding.invalidateExecution); err != nil {
		return nil, nil, false, err
	}
	scope, err := state.beginScope()
	return state, scope, false, err
}

func executeMutationBatchStatement(ctx context.Context, queryer sqlx.QueryerContext, registry *schema.Registry, provider policyir.Provider, model policyir.ModelID, statement mutationbatch.Statement, limit uint32) ([]mutationdecode.Row, [][]mutationbatch.FieldGrant, error) {
	columns := statement.Columns()
	fieldOrder := make([]policyir.FieldID, len(columns))
	for index, column := range columns {
		fieldOrder[index] = column.FieldID()
	}
	decoder, err := readdecode.NewFields(model, registry, provider, fieldOrder)
	if err != nil {
		return nil, nil, err
	}
	bindings := statement.Bindings()
	arguments := make([]any, len(bindings))
	for index, binding := range bindings {
		arguments[index] = binding.Value()
	}
	recordQueryerStatement(ctx, queryer)
	rows, err := queryer.QueryxContext(ctx, statement.SQL(), arguments...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	result := make([]mutationdecode.Row, 0)
	grantRows := make([][]mutationbatch.FieldGrant, 0)
	authorizationColumns := statement.AuthorizationColumns()
	for rows.Next() {
		if limit != 0 && uint64(len(result)) >= uint64(limit) {
			return nil, nil, fmt.Errorf("batch statement returned more than its cardinality bound")
		}
		scan := decoder.NewScan()
		destinations := scan.Destinations()
		rawGrants := make([]any, len(authorizationColumns))
		for index := range rawGrants {
			destinations = append(destinations, &rawGrants[index])
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, nil, err
		}
		decoded, err := scan.Decode()
		if err != nil {
			return nil, nil, err
		}
		cells := make([]mutationdecode.Cell, len(decoded))
		for index, cell := range decoded {
			cells[index], err = exactMutationCell(cell)
			if err != nil {
				return nil, nil, err
			}
		}
		row, err := mutationdecode.NewCompleteRow(registry, model, cells)
		if err != nil {
			return nil, nil, err
		}
		grants := make([]mutationbatch.FieldGrant, len(authorizationColumns))
		for index, column := range authorizationColumns {
			granted, err := decodeMutationAuthorizationGrant(rawGrants[index])
			if err != nil {
				return nil, nil, err
			}
			grants[index], err = mutationbatch.NewFieldGrant(column.FieldID(), granted)
			if err != nil {
				return nil, nil, err
			}
		}
		result = append(result, row)
		grantRows = append(grantRows, grants)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if statement.Cardinality() == mutationbatch.ExactlyCapturedRows && uint32(len(result)) != statement.ExpectedRows() {
		return nil, nil, &batchCardinalityError{role: statement.Role(), got: uint32(len(result)), want: statement.ExpectedRows()}
	}
	return result, grantRows, nil
}

type batchCardinalityError struct {
	role      mutationbatch.Role
	got, want uint32
}

func (failure *batchCardinalityError) Error() string {
	return fmt.Sprintf("batch role %d returned %d rows; expected %d", failure.role, failure.got, failure.want)
}

func beginBatchExecution(ctx context.Context, database *sqlx.DB, provider policyir.Provider, binding *executionBinding, requirement mutationbatch.TransactionRequirement) (batchExecutionScope, error) {
	if database == nil || binding == nil {
		return batchExecutionScope{}, fmt.Errorf("batch database and execution binding are required")
	}
	if binding.scoped && binding.transaction == nil {
		queryer, err := binding.queryerFor(database)
		if err != nil {
			return batchExecutionScope{}, err
		}
		execer, ok := queryer.(sqlx.ExecerContext)
		if !ok {
			return batchExecutionScope{}, fmt.Errorf("scoped batch executor cannot execute statements")
		}
		return batchExecutionScope{queryer: queryer, execer: execer, binding: binding, finish: func(context.Context) error { return nil }, abort: func() error { return nil }}, nil
	}
	if binding.transaction != nil {
		if provider == policyir.ProviderPostgreSQL && requirement != mutationbatch.PostgreSQLTransaction || provider == policyir.ProviderSQLite && requirement != mutationbatch.SQLiteImmediateTransaction {
			return batchExecutionScope{}, fmt.Errorf("transaction-bound batch has the wrong provider requirement")
		}
		sequence, err := binding.nextSavepoint()
		if err != nil {
			return batchExecutionScope{}, err
		}
		name := fmt.Sprintf(`"golem_batch_%d"`, sequence)
		if _, err := binding.transaction.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
			return batchExecutionScope{}, err
		}
		queryer, err := binding.queryerFor(database)
		if err != nil {
			_, _ = binding.transaction.ExecContext(context.Background(), "ROLLBACK TO SAVEPOINT "+name)
			_, _ = binding.transaction.ExecContext(context.Background(), "RELEASE SAVEPOINT "+name)
			return batchExecutionScope{}, err
		}
		return batchExecutionScope{
			queryer: queryer,
			execer:  binding.transaction,
			binding: binding,
			finish: func(ctx context.Context) error {
				_, err := binding.transaction.ExecContext(ctx, "RELEASE SAVEPOINT "+name)
				return err
			},
			abort: func() error {
				_, rollbackErr := binding.transaction.ExecContext(context.Background(), "ROLLBACK TO SAVEPOINT "+name)
				_, releaseErr := binding.transaction.ExecContext(context.Background(), "RELEASE SAVEPOINT "+name)
				return errors.Join(rollbackErr, releaseErr)
			},
		}, nil
	}
	switch provider {
	case policyir.ProviderPostgreSQL:
		if requirement != mutationbatch.PostgreSQLTransaction {
			return batchExecutionScope{}, fmt.Errorf("PostgreSQL batch has the wrong transaction requirement")
		}
		transaction, err := database.BeginTxx(ctx, nil)
		if err != nil {
			return batchExecutionScope{}, err
		}
		active := transactionExecution(database, transaction)
		queryer, err := active.queryerFor(database)
		if err != nil {
			active.close()
			_ = transaction.Rollback()
			return batchExecutionScope{}, err
		}
		return batchExecutionScope{queryer: queryer, execer: transaction, binding: active, finish: func(context.Context) error {
			defer active.close()
			return transaction.Commit()
		}, abort: func() error {
			defer active.close()
			return ignoreTransactionDone(transaction.Rollback())
		}}, nil
	case policyir.ProviderSQLite:
		if requirement != mutationbatch.SQLiteImmediateTransaction {
			return batchExecutionScope{}, fmt.Errorf("SQLite batch has the wrong transaction requirement")
		}
		connection, err := database.Connx(ctx)
		if err != nil {
			return batchExecutionScope{}, err
		}
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			_ = connection.Close()
			return batchExecutionScope{}, err
		}
		active := scopedExecution(database, connection)
		queryer, err := active.queryerFor(database)
		if err != nil {
			active.close()
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			_ = connection.Close()
			return batchExecutionScope{}, err
		}
		return batchExecutionScope{
			queryer: queryer,
			execer:  connection,
			binding: active,
			finish: func(ctx context.Context) error {
				return commitSQLiteImmediate(ctx, connection, active.close)
			},
			abort: func() error {
				defer active.close()
				_, rollbackErr := connection.ExecContext(context.Background(), "ROLLBACK")
				closeErr := connection.Close()
				return errors.Join(rollbackErr, closeErr)
			},
		}, nil
	default:
		return batchExecutionScope{}, fmt.Errorf("batch provider is unsupported")
	}
}

func publicBatchPreparationError(operation mutationir.Operation, model golem.ModelID, err error) error {
	var planned *mutationplan.Error
	if errors.As(err, &planned) && planned.Code == mutationplan.CodePolicy {
		return golem.RuntimeOperationError(golem.CodeForbidden, batchOperationName(operation), model, golem.FieldID(planned.Field), "batch mutation is not authorized", err)
	}
	var public *golem.Error
	if errors.As(err, &public) {
		return err
	}
	return golem.RuntimeOperationError(golem.CodeBadUserInput, batchOperationName(operation), model, golem.FieldID{}, "batch mutation request is invalid", err)
}

func publicBatchExecutionError(program mutationbatch.Program, err error) error {
	model := programModel(program)
	var hook *mutationHookFailure
	if errors.As(err, &hook) {
		return golem.RuntimeOperationError(golem.CodeBadUserInput, batchOperationName(program.Operation()), model, golem.FieldID{}, "mutation hook rejected the operation", err)
	}
	var batchFailure *mutationbatch.Error
	if errors.As(err, &batchFailure) {
		switch batchFailure.Code {
		case mutationbatch.CodeForbidden:
			return golem.RuntimeOperationError(golem.CodeForbidden, batchOperationName(program.Operation()), model, golem.FieldID(batchFailure.Field), "batch mutation is not authorized", err)
		case mutationbatch.CodeLimit:
			return golem.RuntimeOperationError(golem.CodeBadUserInput, batchOperationName(program.Operation()), model, golem.FieldID{}, "batch mutation exceeds the configured row limit", err)
		case mutationbatch.CodeSet, mutationbatch.CodeIdentity:
			return golem.RuntimeOperationError(golem.CodeConflict, batchOperationName(program.Operation()), model, golem.FieldID(batchFailure.Field), "batch mutation conflicted", err)
		}
	}
	var cardinality *batchCardinalityError
	if errors.As(err, &cardinality) {
		code := golem.CodeConflict
		if cardinality.role == mutationbatch.AuthorizePreImage || cardinality.role == mutationbatch.RehydrateAfterImage {
			code = golem.CodeForbidden
		}
		return golem.RuntimeOperationError(code, batchOperationName(program.Operation()), model, golem.FieldID{}, "batch mutation cardinality verification failed", err)
	}
	if scalarMutationProviderFailureKind(err) == scalarMutationConflict {
		return golem.RuntimeOperationError(golem.CodeConflict, batchOperationName(program.Operation()), model, golem.FieldID{}, "batch mutation conflicted", err)
	}
	return golem.RuntimeOperationError(golem.CodeBadUserInput, batchOperationName(program.Operation()), model, golem.FieldID{}, "batch mutation could not be completed", err)
}

func batchOperationName(operation mutationir.Operation) string {
	if operation == mutationir.UpdateMany {
		return "updateMany"
	}
	if operation == mutationir.DeleteMany {
		return "deleteMany"
	}
	return "batchMutation"
}

func programModel(program mutationbatch.Program) golem.ModelID {
	return golem.ModelID(program.ModelID())
}
