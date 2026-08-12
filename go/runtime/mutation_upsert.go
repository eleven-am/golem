package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	mutationupsert "github.com/eleven-am/golem/go/internal/mutation/upsert"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

type preparedRuntimeUpsert struct {
	plan               mutationir.Plan
	kernel             mutationupsert.Program
	create             mutationsql.Program
	update             mutationsql.Program
	createNested       *mutationnested.Result
	updateNested       *mutationnested.Result
	createNestedPolicy error
	updateNestedPolicy error
	request            rootUpsertPrepareRequest
}

type rootUpsertPrepareRequest struct {
	model         policyir.ModelID
	target        golem.FrozenMutationTarget
	create        golem.FrozenMutationInput
	update        golem.FrozenMutationInput
	result        mutationir.ImageRequirements
	runtimeValues *mutationRuntimeValues
	// deferHookOwned permits only ContractIR GraphQLHookOwned create fields to
	// remain absent while the database-coordinated branch selector is built.
	// The selected create branch clears it after BeforeCreate and replans, so a
	// hook which omits a required field fails before executing SQL.
	deferHookOwned bool
	// concurrencyAbsent selects the expectation-aware create-only branch. The
	// ordinary update renderer is intentionally not available for a versioned
	// model; the authorized/private probes decide whether create may run.
	concurrencyAbsent bool
}

func missingRuntimeOwnedFields(input golem.FrozenMutationInput, candidates []golem.FieldID) []golem.FieldID {
	authored := make(map[golem.FieldID]bool, len(input.Fields()))
	for _, field := range input.Fields() {
		authored[field.FieldID()] = true
	}
	result := make([]golem.FieldID, 0, len(candidates))
	for _, field := range candidates {
		if !authored[field] {
			result = append(result, field)
		}
	}
	return result
}

func mergeRuntimeOwnedFields(left, right []golem.FieldID) []golem.FieldID {
	seen := make(map[golem.FieldID]bool, len(left)+len(right))
	result := make([]golem.FieldID, 0, len(left)+len(right))
	for _, values := range [][]golem.FieldID{left, right} {
		for _, field := range values {
			if !seen[field] {
				seen[field] = true
				result = append(result, field)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return string(result[i][:]) < string(result[j][:]) })
	return result
}

func prepareCallerRootUpsert[P, A any](caller *Caller[P, A], request rootUpsertPrepareRequest) (preparedRuntimeUpsert, error) {
	if caller == nil || caller.app == nil || caller.policies == nil || caller.executor == nil {
		return preparedRuntimeUpsert{}, fmt.Errorf("caller upsert preparation is unavailable")
	}
	return prepareRootUpsert(request, mutationir.Caller, caller.app, caller.policies, caller.executor)
}

func prepareSystemRootUpsert[P, A any](system System[P, A], request rootUpsertPrepareRequest) (preparedRuntimeUpsert, error) {
	if system.app == nil || system.executor == nil {
		return preparedRuntimeUpsert{}, fmt.Errorf("system upsert preparation is unavailable")
	}
	return prepareRootUpsert(request, mutationir.System, system.app, nil, system.executor)
}

func prepareRootUpsert[P, A any](request rootUpsertPrepareRequest, stance mutationir.Stance, app *App[P, A], policies mutationplan.PolicySet, binding *executionBinding) (preparedRuntimeUpsert, error) {
	if request.model == (policyir.ModelID{}) || request.result.ModelID() != request.model {
		return preparedRuntimeUpsert{}, fmt.Errorf("upsert model or result requirements are invalid")
	}
	boundTarget, err := mutationbind.Target(request.target, golem.ModelID(request.model), app.registry)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	ownedFields, err := nestedRootSourceOwnedFields(request.create, app.registry)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	if request.deferHookOwned {
		model, present := app.registry.Model(golem.ModelID(request.model))
		if !present {
			return preparedRuntimeUpsert{}, fmt.Errorf("hook-owned upsert model is absent")
		}
		missing := missingRuntimeOwnedFields(request.create, model.GraphQLHookOwnedCreateFields())
		if len(missing) != 0 {
			if stance != mutationir.Caller || !hasBeforeCreateHook(app.bindings, golem.ModelID(request.model)) {
				return preparedRuntimeUpsert{}, fmt.Errorf("hook-owned upsert preparation requires a caller BeforeCreate hook")
			}
			ownedFields = mergeRuntimeOwnedFields(ownedFields, missing)
		}
	}
	boundCreate, _, err := mutationbind.CreateInputWithRuntimeOwnedFields(request.create, app.registry, ownedFields)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	boundUpdate, err := mutationbind.UpdateInput(request.update, app.registry)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	if request.runtimeValues == nil {
		request.runtimeValues = newMutationRuntimeValues()
	}
	boundCreate, err = request.runtimeValues.apply(boundCreate, app.registry)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	boundUpdate, err = request.runtimeValues.apply(boundUpdate, app.registry)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	if request.concurrencyAbsent {
		if err := validateAbsentCreateMatchesTarget(boundTarget.Target(), boundCreate.Operations()); err != nil {
			return preparedRuntimeUpsert{}, err
		}
	}
	bounds, err := mutationir.NewStatementBounds(uint32(app.mutationLimits.statementParameters), uint32(app.mutationLimits.touchedRows))
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	retry := mutationir.EngineOwnedUpsertRetry
	if binding.scoped {
		retry = mutationir.CallerTransactionNoReplay
	}
	planning := mutationplan.RootRequest{
		Stance: stance, Operation: mutationir.Upsert, Model: request.model,
		Registry: app.registry, Policies: policies,
		Target: &boundTarget, Create: &boundCreate, Update: &boundUpdate,
		Result: request.result, Retry: retry, Bounds: bounds,
	}
	planning.AuthorizedRuntimeFields = make([]policyir.FieldID, len(ownedFields))
	for index, field := range ownedFields {
		planning.AuthorizedRuntimeFields[index] = policyir.FieldID(field)
	}
	if stance == mutationir.Caller {
		planning.Hooks = mutationHookInventory(app.bindings, golem.ModelID(request.model))
	}
	modelFact, _ := app.registry.Model(golem.ModelID(request.model))
	if modelFact.SubscriptionsEnabled() {
		factCodec, eventSchema, _, err := mutationEventSchema(app.registry, request.model)
		if err != nil {
			return preparedRuntimeUpsert{}, err
		}
		planning.CaptureFacts, planning.FactCodec, planning.EventSchema = true, &factCodec, eventSchema
	}
	plan, err := mutationplan.BuildRoot(planning)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	kernel, err := mutationupsert.Prepare(plan, app.registry, app.provider, app.capabilities)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	create, err := renderUpsertBranch(plan, kernel.CreateBranch(), app)
	if err != nil {
		return preparedRuntimeUpsert{}, err
	}
	var update mutationsql.Program
	if !request.concurrencyAbsent {
		update, err = renderUpsertBranch(plan, kernel.UpdateBranch(), app)
		if err != nil {
			return preparedRuntimeUpsert{}, err
		}
	}
	var createNested, updateNested *mutationnested.Result
	var createNestedPolicy, updateNestedPolicy error
	if len(request.create.Relations()) != 0 {
		compiled, compileErr := prepareNestedCompilation(app, policies, stance, mutationir.Create, &request.create, nil, request.result, request.runtimeValues)
		if compileErr != nil {
			var nestedFailure *mutationnested.Error
			if stance != mutationir.Caller || !errors.As(compileErr, &nestedFailure) || nestedFailure.Code != mutationnested.CodePolicy {
				return preparedRuntimeUpsert{}, compileErr
			}
			createNestedPolicy = compileErr
		} else {
			createNested = &compiled
		}
	}
	if len(request.update.Relations()) != 0 {
		compiled, compileErr := prepareNestedCompilation(app, policies, stance, mutationir.Update, &request.update, &request.target, request.result, request.runtimeValues)
		if compileErr != nil {
			var nestedFailure *mutationnested.Error
			if stance != mutationir.Caller || !errors.As(compileErr, &nestedFailure) || nestedFailure.Code != mutationnested.CodePolicy {
				return preparedRuntimeUpsert{}, compileErr
			}
			updateNestedPolicy = compileErr
		} else {
			updateNested = &compiled
		}
	}
	return preparedRuntimeUpsert{plan: plan, kernel: kernel, create: create, update: update, createNested: createNested, updateNested: updateNested, createNestedPolicy: createNestedPolicy, updateNestedPolicy: updateNestedPolicy, request: request}, nil
}

func validateAbsentCreateMatchesTarget(target mutationir.Target, operations []mutationir.ScalarOperation) error {
	created := make(map[policyir.FieldID]policyir.Value, len(operations))
	for _, operation := range operations {
		value, present := operation.Value()
		if operation.Kind() == mutationir.ScalarSet && present {
			created[operation.FieldID()] = value
		}
	}
	for _, selector := range target.Values() {
		value, present := created[selector.FieldID()]
		if !present || !equalMutationPhysicalValue(value, selector.Value()) {
			return fmt.Errorf("expect-absent create input does not match the guarded unique selector")
		}
	}
	return nil
}

func renderUpsertBranch[P, A any](parent mutationir.Plan, node mutationir.Node, app *App[P, A]) (mutationsql.Program, error) {
	input := mutationir.NodeInput{
		Operation: node.Operation(), Model: node.ModelID(),
		ScalarOperations: node.ScalarOperations(), InfluencingFields: node.InfluencingFields(),
		Before: node.BeforeRequirements(), After: node.AfterRequirements(),
		Hooks: node.Hooks(), Fact: node.Fact(), Identity: node.IdentityBehavior(),
	}
	if value, present := node.Target(); present {
		input.Target = &value
	}
	if value, present := node.Predicate(); present {
		input.Predicate = &value
	}
	if value, present := node.RelationPosition(); present {
		input.RelationPosition = &value
	}
	if value, present := node.SelectionRequirement(); present {
		input.Selection = &value
	}
	if value, present := node.RowPostcondition(); present {
		input.RowPostcondition = &value
	}
	input.FieldConditions = node.FieldAuthorizations()
	graph, err := mutationir.NewGraph(input)
	if err != nil {
		return mutationsql.Program{}, err
	}
	branch, err := mutationir.NewPlan(mutationir.PlanInput{
		Stance: parent.Stance(), Graph: graph, Result: parent.ResultRequirements(),
		Providers: parent.ProviderRequirements(), Retry: mutationir.NoRetry, Bounds: parent.Bounds(),
		FactCodec: func() *mutationir.FactCodecRequirement {
			codec, ok := parent.FactCodecRequirement()
			if !ok {
				return nil
			}
			return &codec
		}(),
	})
	if err != nil {
		return mutationsql.Program{}, err
	}
	return mutationsql.Render(branch, app.registry, app.provider, app.capabilities)
}

// sqlxUpsertBackend creates an owned transaction for ordinary calls and one
// savepoint attempt for a transaction-bound generated client.
type sqlxUpsertBackend struct {
	database *sqlx.DB
	provider policyir.Provider
	binding  *executionBinding
	mutation executionMutationConfig
}

// upsertAttemptFinishFault is an unexported acceptance-test seam at the
// provider boundary after a complete branch has executed but before its
// transaction is flushed or committed. Production callers never install it.
// It keeps retry evidence honest: hooks cannot impersonate provider
// interference, while tests can still deterministically exercise whole-attempt
// replay and exhaustion.
type upsertAttemptFinishFault func(ordinal uint32) error
type upsertAttemptFinishFaultContextKey struct{}

func contextWithUpsertAttemptFinishFault(ctx context.Context, fault upsertAttemptFinishFault) context.Context {
	if ctx == nil || fault == nil {
		return ctx
	}
	return context.WithValue(ctx, upsertAttemptFinishFaultContextKey{}, fault)
}

func applyUpsertAttemptFinishFault(ctx context.Context, ordinal uint32, attempt *sqlxUpsertAttempt) *sqlxUpsertAttempt {
	if attempt == nil || ctx == nil {
		return attempt
	}
	fault, _ := ctx.Value(upsertAttemptFinishFaultContextKey{}).(upsertAttemptFinishFault)
	if fault == nil {
		return attempt
	}
	finish := attempt.finish
	attempt.finish = func(finishContext context.Context) error {
		if err := fault(ordinal); err != nil {
			return err
		}
		return finish(finishContext)
	}
	return attempt
}

func (backend sqlxUpsertBackend) Begin(ctx context.Context, requirement mutationsql.TransactionRequirement, ordinal uint32) (mutationupsert.Attempt, error) {
	if backend.database == nil || backend.binding == nil {
		return nil, fmt.Errorf("upsert backend is unavailable")
	}
	if backend.binding.scoped && backend.binding.transaction == nil {
		if backend.provider == policyir.ProviderPostgreSQL && requirement != mutationsql.PostgreSQLTransaction || backend.provider == policyir.ProviderSQLite && requirement != mutationsql.SQLiteImmediateTransaction {
			return nil, fmt.Errorf("upsert scoped attempt has the wrong provider requirement")
		}
		queryer, err := backend.binding.queryerFor(backend.database)
		if err != nil {
			return nil, err
		}
		return applyUpsertAttemptFinishFault(ctx, ordinal, newSQLXUpsertAttempt(queryer, backend.binding, func(context.Context) error { return nil }, func() error { return nil })), nil
	}
	if backend.binding.transaction != nil {
		return backend.beginSavepoint(ctx, requirement, ordinal)
	}
	switch backend.provider {
	case policyir.ProviderPostgreSQL:
		if requirement != mutationsql.PostgreSQLTransaction {
			return nil, fmt.Errorf("PostgreSQL upsert has the wrong transaction requirement")
		}
		transaction, err := backend.database.BeginTxx(ctx, nil)
		if err != nil {
			return nil, err
		}
		binding := transactionExecution(backend.database, transaction)
		if err := binding.enableMutation(backend.mutation); err != nil {
			_ = transaction.Rollback()
			binding.close()
			return nil, err
		}
		queryer, err := binding.queryerFor(backend.database)
		if err != nil {
			binding.close()
			_ = transaction.Rollback()
			return nil, err
		}
		return applyUpsertAttemptFinishFault(ctx, ordinal, newSQLXUpsertAttempt(queryer, binding,
			func(ctx context.Context) error {
				if err := flushMutationBinding(ctx, transaction, binding); err != nil {
					return err
				}
				if err := transaction.Commit(); err != nil {
					binding.discardMutation()
					return err
				}
				commitMutationBinding(ctx, binding)
				binding.close()
				return nil
			},
			func() error {
				defer binding.close()
				binding.discardMutation()
				return ignoreTransactionDone(transaction.Rollback())
			},
		)), nil
	case policyir.ProviderSQLite:
		if requirement != mutationsql.SQLiteImmediateTransaction {
			return nil, fmt.Errorf("SQLite upsert has the wrong transaction requirement")
		}
		connection, err := backend.database.Connx(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			_ = connection.Close()
			return nil, err
		}
		binding := scopedExecution(backend.database, connection)
		if err := binding.enableMutation(backend.mutation); err != nil {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			binding.close()
			_ = connection.Close()
			return nil, err
		}
		queryer, err := binding.queryerFor(backend.database)
		if err != nil {
			binding.close()
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			_ = connection.Close()
			return nil, err
		}
		return applyUpsertAttemptFinishFault(ctx, ordinal, newSQLXUpsertAttempt(queryer, binding,
			func(ctx context.Context) error {
				if err := flushMutationBinding(ctx, connection, binding); err != nil {
					return err
				}
				if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
					binding.discardMutation()
					return err
				}
				commitMutationBinding(ctx, binding)
				binding.close()
				// The database commit is already durable. A connection-close error
				// must not turn committed success into a retry or an operation error.
				_ = connection.Close()
				return nil
			},
			func() error {
				_, rollbackErr := connection.ExecContext(context.Background(), "ROLLBACK")
				binding.discardMutation()
				binding.close()
				closeErr := connection.Close()
				return errors.Join(rollbackErr, closeErr)
			},
		)), nil
	default:
		return nil, fmt.Errorf("upsert provider is unsupported")
	}
}

func (backend sqlxUpsertBackend) beginSavepoint(ctx context.Context, requirement mutationsql.TransactionRequirement, ordinal uint32) (mutationupsert.Attempt, error) {
	if backend.provider == policyir.ProviderPostgreSQL && requirement != mutationsql.PostgreSQLTransaction || backend.provider == policyir.ProviderSQLite && requirement != mutationsql.SQLiteImmediateTransaction {
		return nil, fmt.Errorf("upsert savepoint has the wrong provider requirement")
	}
	sequence, err := backend.binding.nextSavepoint()
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("golem_upsert_%d", sequence)
	quoted := `"` + name + `"`
	transaction := backend.binding.transaction
	state, err := backend.binding.mutationState()
	if err != nil {
		return nil, err
	}
	scope, err := state.beginScope()
	if err != nil {
		return nil, err
	}
	if _, err := transaction.ExecContext(ctx, "SAVEPOINT "+quoted); err != nil {
		_ = scope.rollback()
		return nil, err
	}
	queryer, err := backend.binding.queryerFor(backend.database)
	if err != nil {
		_, _ = transaction.ExecContext(context.Background(), "ROLLBACK TO SAVEPOINT "+quoted)
		_, _ = transaction.ExecContext(context.Background(), "RELEASE SAVEPOINT "+quoted)
		_ = scope.rollback()
		return nil, err
	}
	return applyUpsertAttemptFinishFault(ctx, ordinal, newSQLXUpsertAttempt(queryer, backend.binding,
		func(ctx context.Context) error {
			if _, err := transaction.ExecContext(ctx, "RELEASE SAVEPOINT "+quoted); err != nil {
				return err
			}
			return scope.release()
		},
		func() error {
			_, rollbackErr := transaction.ExecContext(context.Background(), "ROLLBACK TO SAVEPOINT "+quoted)
			_, releaseErr := transaction.ExecContext(context.Background(), "RELEASE SAVEPOINT "+quoted)
			scopeErr := scope.rollback()
			return errors.Join(rollbackErr, releaseErr, scopeErr)
		},
	)), nil
}

type sqlxUpsertAttempt struct {
	queryer sqlx.QueryerContext
	binding *executionBinding
	finish  func(context.Context) error
	abort   func() error
	mu      sync.Mutex
	done    bool
}

func newSQLXUpsertAttempt(queryer sqlx.QueryerContext, binding *executionBinding, finish func(context.Context) error, abort func() error) *sqlxUpsertAttempt {
	return &sqlxUpsertAttempt{queryer: queryer, binding: binding, finish: finish, abort: abort}
}

func (attempt *sqlxUpsertAttempt) Query(ctx context.Context, statement mutationupsert.Statement) (uint32, error) {
	recordQueryerStatement(ctx, attempt.queryer, attempt.binding.observation)
	rows, err := attempt.queryer.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	if len(columns) != 1 {
		return 0, fmt.Errorf("upsert protocol statement returned %d columns", len(columns))
	}
	var count uint32
	for rows.Next() {
		var discarded any
		if err := rows.Scan(&discarded); err != nil {
			return 0, err
		}
		count++
		if count > 1 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, rows.Close()
}

func (attempt *sqlxUpsertAttempt) Finish(ctx context.Context) error {
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.done {
		return sql.ErrTxDone
	}
	if err := attempt.finish(ctx); err != nil {
		return err
	}
	attempt.done = true
	return nil
}

func (attempt *sqlxUpsertAttempt) Abort() error {
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.done {
		return nil
	}
	err := attempt.abort()
	attempt.done = true
	return err
}

func ignoreTransactionDone(err error) error {
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

type runtimeUpsertBranchExecutor[P, A, M any] struct {
	app        *App[P, A]
	descriptor golem.ModelDescriptor[M]
	projection scalarMutationProjection
	prepared   preparedRuntimeUpsert
	binding    *executionBinding
	policies   mutationplan.PolicySet
	hooks      *callerMutationHookExecution[A]
}

func (executor runtimeUpsertBranchExecutor[P, A, M]) ExecuteBranch(ctx context.Context, generic mutationupsert.Attempt, node mutationir.Node, _ mutationupsert.FrozenValues) (any, error) {
	attempt, ok := generic.(*sqlxUpsertAttempt)
	if !ok || attempt == nil {
		return nil, fmt.Errorf("upsert branch received a foreign transaction attempt")
	}
	branch := node.Branch()
	if branch != mutationir.UpsertCreateBranch && branch != mutationir.UpsertUpdateBranch {
		return nil, fmt.Errorf("upsert selected an unknown branch")
	}
	prepared := executor.prepared
	if executor.hooks != nil {
		request := golem.RuntimeCreateMutationHookRequest(executor.descriptor.Metadata().ModelID(), prepared.request.create)
		if branch == mutationir.UpsertUpdateBranch {
			request = golem.RuntimeUpdateMutationHookRequest(executor.descriptor.Metadata().ModelID(), prepared.request.target, prepared.request.update)
		}
		validate := func(transformed golem.RuntimeMutationHookRequest) error {
			input, target, err := scalarRequestParts(transformed)
			if err != nil {
				return err
			}
			next := prepared.request
			if branch == mutationir.UpsertCreateBranch {
				if transformed.Operation() != golem.HookCreate || input == nil || target != nil {
					return fmt.Errorf("upsert create hook changed branch shape")
				}
				next.create = *input
				next.deferHookOwned = false
			} else {
				if transformed.Operation() != golem.HookUpdate || input == nil || target == nil {
					return fmt.Errorf("upsert update hook changed branch shape")
				}
				next.update, next.target = *input, *target
			}
			replanned, err := prepareRootUpsert(next, mutationir.Caller, executor.app, executor.policies, executor.binding)
			if err == nil {
				prepared = replanned
			}
			return err
		}
		hookContext := golem.RuntimeContextWithActor(ctx, executor.hooks.actor)
		hookContext, hookObservation := observeexec.BeginChild(hookContext, request.ModelID(), observe.KindHook, hookObservationOperation(request.Operation()), observe.PhaseBefore)
		transformed, err := golem.RuntimeInvokeMutationBeforeHooks(hookContext, executor.hooks.bindings, request, validate)
		finishObservation(hookObservation, err)
		if err != nil {
			return nil, &mutationHookFailure{operation: request.Operation(), phase: golem.HookBefore, cause: err}
		}
		if err := validate(transformed); err != nil {
			return nil, &mutationHookFailure{operation: request.Operation(), phase: golem.HookBefore, cause: err}
		}
	}
	program := prepared.create
	if branch == mutationir.UpsertUpdateBranch {
		program = prepared.update
	}
	selectedInput := &prepared.request.create
	if branch == mutationir.UpsertUpdateBranch {
		selectedInput = &prepared.request.update
	}
	if len(selectedInput.Relations()) != 0 {
		compiled := prepared.createNested
		policyErr := prepared.createNestedPolicy
		if branch == mutationir.UpsertUpdateBranch {
			compiled = prepared.updateNested
			policyErr = prepared.updateNestedPolicy
		}
		if policyErr != nil {
			return nil, policyErr
		}
		return executeUpsertNestedBranchProjection(ctx, executor, attempt, compiled)
	}
	return executeUpsertBranchProjection(ctx, executor.app, attempt, executor.descriptor, executor.projection, program, executor.hooks)
}

func executeUpsertNestedBranchProjection[P, A, M any](ctx context.Context, executor runtimeUpsertBranchExecutor[P, A, M], attempt *sqlxUpsertAttempt, compiled *mutationnested.Result) (golem.Row[M], error) {
	stance := mutationir.System
	if executor.hooks != nil {
		stance = mutationir.Caller
	}
	if compiled == nil {
		return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_UPSERT: selected nested branch was not precompiled")
	}
	graph := compiled.Graph()
	var projected golem.Row[M]
	materialized := !executor.projection.active
	boundary := &systemNestedBoundary[P, A]{app: executor.app, source: attempt.binding, graph: graph, compiled: compiled, stance: stance, policies: executor.policies, hooks: executor.hooks, runtimeValues: executor.prepared.request.runtimeValues, reuseBinding: true}
	if executor.hooks != nil {
		boundary.actor = executor.hooks.actor
	}
	if executor.projection.active {
		boundary.verify = nestedProjectionObserver(executor.app, executor.descriptor, executor.projection, &projected, &materialized)
	}
	if _, err := mutationnested.Execute(ctx, graph, uint32(executor.app.mutationLimits.touchedRows), boundary); err != nil {
		return golem.Row[M]{}, err
	}
	if executor.projection.active {
		if !materialized {
			return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_UPSERT: nested branch projection was not materialized")
		}
		return projected, nil
	}
	return golem.RuntimeReadRow(executor.descriptor)
}

func executeUpsertBranchProjection[P, A, M any](ctx context.Context, app *App[P, A], attempt *sqlxUpsertAttempt, descriptor golem.ModelDescriptor[M], projection scalarMutationProjection, program mutationsql.Program, hooks *callerMutationHookExecution[A]) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	var verified scalarMutationVerifiedObserver
	if hooks != nil {
		verified = hooks.verifiedObserver(app.registry)
	}
	if !projection.active {
		if _, err := executeScalarProgramOnQueryerObserved(ctx, attempt.queryer, attempt.binding, app.registry, model, app.provider, program, nil, verified); err != nil {
			return golem.Row[M]{}, err
		}
		return golem.RuntimeReadRow(descriptor)
	}
	var projected golem.Row[M]
	materialized := false
	statements := program.Statements()
	imageStatement := program.IdentityVerification().AfterStatement()
	observeAt := uint32(len(statements) - 1)
	observer := func(observerContext context.Context, observerBinding *executionBinding, statement uint32, execution scalarMutationExecution) error {
		if statement != observeAt {
			return nil
		}
		source, ok := execution.statement(imageStatement)
		if !ok {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, statement, "upsert branch result image is absent", nil)
		}
		row, err := materializeScalarMutationProjection(observerContext, app, observerBinding, descriptor, projection.planned, source)
		if err != nil {
			return err
		}
		projected, materialized = row, true
		return nil
	}
	if _, err := executeScalarProgramOnQueryerObserved(ctx, attempt.queryer, attempt.binding, app.registry, model, app.provider, program, observer, verified); err != nil {
		return golem.Row[M]{}, err
	}
	if !materialized {
		return golem.Row[M]{}, scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "upsert branch projection was not materialized", nil)
	}
	return projected, nil
}

func executePreparedRootUpsert[P, A, M any](ctx context.Context, app *App[P, A], binding *executionBinding, descriptor golem.ModelDescriptor[M], projection scalarMutationProjection, prepared preparedRuntimeUpsert, policies mutationplan.PolicySet, hooks *callerMutationHookExecution[A]) (row golem.Row[M], resultErr error) {
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, app, binding, descriptor.Metadata().ModelID(), observe.KindMutation, observe.OperationMutationUpsert)
	defer func() { finishDeferredObservation(observation, deferredObservation, resultErr) }()
	backend := sqlxUpsertBackend{database: app.database, provider: app.provider, binding: binding, mutation: mutationConfig(app, binding)}
	executor := runtimeUpsertBranchExecutor[P, A, M]{app: app, descriptor: descriptor, projection: projection, prepared: prepared, binding: binding, policies: policies, hooks: hooks}
	kernelResult, err := mutationupsert.Run(ctx, prepared.kernel, backend, func(context.Context) (mutationupsert.FrozenValues, error) {
		// Public inputs were frozen and bound exactly once before Prepare. Branch
		// programs already own those immutable encoded values.
		return mutationupsert.NewFrozenValues(nil), nil
	}, executor, mutationupsert.Options{MaxAttempts: uint32(app.mutationLimits.upsertAttempts)})
	if err != nil {
		return golem.Row[M]{}, publicRootUpsertError(descriptor.Metadata().ModelID(), err)
	}
	row, ok := kernelResult.Value().(golem.Row[M])
	if !ok {
		return golem.Row[M]{}, publicRootUpsertError(descriptor.Metadata().ModelID(), fmt.Errorf("upsert branch returned an invalid result"))
	}
	return row, nil
}

func publicRootUpsertError(model golem.ModelID, err error) error {
	var kernel *mutationupsert.Error
	if errors.As(err, &kernel) && kernel.Code == mutationupsert.CodeConflict {
		return golem.RuntimeOperationError(golem.CodeConflict, "upsert", model, golem.FieldID{}, "mutation conflicted", err)
	}
	// Translate the complete nested error vocabulary (exact-row absence,
	// authorization, batch cardinality/set conflicts, guarded retry exhaustion,
	// scalar and hook failures) before rebasing it to the public root-Upsert
	// boundary. The upsert kernel wraps selected-branch errors, so checking only
	// for an already-public golem.Error loses typed nested causes.
	translated := publicNestedMutationExecutionError(mutationir.Upsert, model, err)
	var public *golem.Error
	if errors.As(translated, &public) {
		message := "mutation could not be completed"
		switch public.Code {
		case golem.CodeNotFound:
			message = "record not found"
		case golem.CodeForbidden:
			message = "mutation is not authorized"
		case golem.CodeConflict:
			message = "mutation conflicted"
		}
		return golem.RuntimeOperationError(public.Code, "upsert", model, public.Field, message, err)
	}
	return golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", model, golem.FieldID{}, "mutation could not be completed", err)
}
