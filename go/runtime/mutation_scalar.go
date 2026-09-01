package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	mutationupsert "github.com/eleven-am/golem/go/internal/mutation/upsert"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/jmoiron/sqlx"
)

// scalarMutationExecution is the exact physical handoff between the closed
// mutation SQL program and P4's logical decoder/result builder. It is private
// deliberately: driver values must never become an application-facing result
// or an authorization input.
type scalarMutationExecution struct {
	operation  mutationir.Operation
	statements []scalarMutationStatementResult
}

type scalarMutationStatementResult struct {
	role   mutationsql.Role
	fields map[policyir.FieldID]any
	cells  []readdecode.Cell
	grants map[policyir.FieldID]bool
}

type scalarMutationStatementObserver func(context.Context, *executionBinding, uint32, scalarMutationExecution) error
type scalarMutationVerifiedObserver func(context.Context, *executionBinding, mutationsql.Program, scalarMutationExecution) error

func (execution scalarMutationExecution) statement(index uint32) (scalarMutationStatementResult, bool) {
	if uint64(index) >= uint64(len(execution.statements)) {
		return scalarMutationStatementResult{}, false
	}
	return execution.statements[index].clone(), true
}

func (result scalarMutationStatementResult) clone() scalarMutationStatementResult {
	copy := scalarMutationStatementResult{role: result.role, fields: make(map[policyir.FieldID]any, len(result.fields)), cells: append([]readdecode.Cell(nil), result.cells...), grants: make(map[policyir.FieldID]bool, len(result.grants))}
	for field, value := range result.fields {
		copy.fields[field] = cloneMutationPhysicalValue(value)
	}
	for field, granted := range result.grants {
		copy.grants[field] = granted
	}
	return copy
}

type scalarMutationFailureKind uint8

const (
	scalarMutationInvalid scalarMutationFailureKind = iota + 1
	scalarMutationNotFound
	scalarMutationForbidden
	scalarMutationConflict
	scalarMutationProvider
	scalarMutationInvariant
)

type scalarMutationFailure struct {
	kind      scalarMutationFailureKind
	operation mutationir.Operation
	role      mutationsql.Role
	statement uint32
	detail    string
	cause     error
}

type scalarMutationPrepareRequest struct {
	operation         mutationir.Operation
	model             policyir.ModelID
	input             *golem.FrozenMutationInput
	target            *golem.FrozenMutationTarget
	result            mutationir.ImageRequirements
	forceHookSnapshot bool
	runtimeValues     *mutationRuntimeValues
}

// prepareCallerScalarProgram is the single pre-transaction caller path for
// root scalar Create, Update, and Delete. Binding, selector/read
// classification, policy resolution, plan construction, and SQL rendering all
// finish before any execution can begin or join a transaction.
func prepareCallerScalarProgram[P, A any](caller *Caller[P, A], request scalarMutationPrepareRequest) (mutationsql.Program, error) {
	if caller == nil || caller.app == nil || caller.policies == nil {
		return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "caller mutation preparation is unavailable", nil)
	}
	return prepareScalarProgram(request, mutationir.Caller, caller.app, caller.policies, false)
}

func prepareCallerVersionedScalarProgram[P, A any](caller *Caller[P, A], request scalarMutationPrepareRequest) (mutationsql.Program, error) {
	if caller == nil || caller.app == nil || caller.policies == nil {
		return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "caller versioned mutation preparation is unavailable", nil)
	}
	return prepareScalarProgram(request, mutationir.Caller, caller.app, caller.policies, true)
}

// prepareSystemScalarProgram uses the same binder, planner, and renderer but
// supplies the explicit System stance. The planner therefore cannot acquire a
// caller policy or accidentally invoke Go authorization evaluation.
func prepareSystemScalarProgram[P, A any](system System[P, A], request scalarMutationPrepareRequest) (mutationsql.Program, error) {
	if system.app == nil {
		return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "system mutation preparation is unavailable", nil)
	}
	return prepareScalarProgram(request, mutationir.System, system.app, nil, false)
}

func prepareSystemVersionedScalarProgram[P, A any](system System[P, A], request scalarMutationPrepareRequest) (mutationsql.Program, error) {
	if system.app == nil {
		return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "system versioned mutation preparation is unavailable", nil)
	}
	return prepareScalarProgram(request, mutationir.System, system.app, nil, true)
}

func prepareScalarProgram[P, A any](request scalarMutationPrepareRequest, stance mutationir.Stance, app *App[P, A], policies mutationplan.PolicySet, versioned bool) (mutationsql.Program, error) {
	if app == nil || request.model == (policyir.ModelID{}) || request.result.ModelID() != request.model {
		return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "mutation model or result requirements are invalid", nil)
	}
	if request.operation != mutationir.Create && request.operation != mutationir.Update && request.operation != mutationir.Delete {
		return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "preparation accepts only root scalar create, update, and delete", nil)
	}
	bounds, err := mutationir.NewStatementBounds(uint32(app.mutationLimits.statementParameters), uint32(app.mutationLimits.touchedRows))
	if err != nil {
		return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvariant, 0, 0, "runtime mutation limits could not form statement bounds", err)
	}
	planning := mutationplan.RootRequest{
		Stance: stance, Operation: request.operation, Model: request.model,
		Registry: app.registry, Policies: policies, Result: request.result,
		Retry: mutationir.NoRetry, Bounds: bounds,
	}
	planning.ConcurrencyPrecheck = versioned && stance == mutationir.Caller && request.operation == mutationir.Update
	if stance == mutationir.Caller {
		planning.Hooks = mutationHookInventory(app.bindings, golem.ModelID(request.model))
		if request.forceHookSnapshot {
			switch request.operation {
			case mutationir.Create:
				planning.Hooks.Create = append(planning.Hooks.Create, mutationir.TransactionAfterHook)
			case mutationir.Update:
				planning.Hooks.Update = append(planning.Hooks.Update, mutationir.TransactionAfterHook)
			case mutationir.Delete:
				planning.Hooks.Delete = append(planning.Hooks.Delete, mutationir.TransactionAfterHook)
			}
		}
	}
	switch request.operation {
	case mutationir.Create:
		if request.input == nil || request.target != nil {
			return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "create requires input and forbids a target", nil)
		}
		bound, bindErr := mutationbind.CreateInput(*request.input, app.registry)
		if bindErr != nil {
			return mutationsql.Program{}, bindErr
		}
		values := request.runtimeValues
		if values == nil {
			values = newMutationRuntimeValues()
		}
		bound, bindErr = values.apply(bound, app.registry)
		if bindErr != nil {
			return mutationsql.Program{}, bindErr
		}
		planning.Create = &bound
	case mutationir.Update:
		if request.input == nil || request.target == nil {
			return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "update requires input and target", nil)
		}
		boundInput, bindErr := mutationbind.UpdateInput(*request.input, app.registry)
		if bindErr != nil {
			return mutationsql.Program{}, bindErr
		}
		values := request.runtimeValues
		if values == nil {
			values = newMutationRuntimeValues()
		}
		boundInput, bindErr = values.apply(boundInput, app.registry)
		if bindErr != nil {
			return mutationsql.Program{}, bindErr
		}
		boundTarget, bindErr := mutationbind.Target(*request.target, golem.ModelID(request.model), app.registry)
		if bindErr != nil {
			return mutationsql.Program{}, bindErr
		}
		planning.Update, planning.Target = &boundInput, &boundTarget
	case mutationir.Delete:
		if request.input != nil || request.target == nil {
			return mutationsql.Program{}, scalarMutationError(request.operation, scalarMutationInvalid, 0, 0, "delete requires a target and forbids input", nil)
		}
		boundTarget, bindErr := mutationbind.Target(*request.target, golem.ModelID(request.model), app.registry)
		if bindErr != nil {
			return mutationsql.Program{}, bindErr
		}
		planning.Target = &boundTarget
	}
	planned, err := mutationplan.BuildRoot(planning)
	if err != nil {
		return mutationsql.Program{}, err
	}
	var program mutationsql.Program
	if versioned {
		program, err = mutationsql.RenderVersioned(planned, app.registry, app.provider, app.capabilities)
	} else {
		program, err = mutationsql.Render(planned, app.registry, app.provider, app.capabilities)
	}
	if err != nil {
		return mutationsql.Program{}, err
	}
	return program, nil
}

func (failure *scalarMutationFailure) Error() string {
	if failure == nil {
		return ""
	}
	return fmt.Sprintf("P4_RUNTIME_MUTATION: operation=%d role=%d statement=%d: %s", failure.operation, failure.role, failure.statement, failure.detail)
}

func (failure *scalarMutationFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func executeSystemScalarProgram[P, A any](ctx context.Context, system System[P, A], model policyir.ModelID, program mutationsql.Program) (scalarMutationExecution, error) {
	if system.app == nil || system.executor == nil {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "system mutation execution is unavailable", nil)
	}
	return executeScalarMutationProgramConfigured(ctx, system.app.database, system.app.provider, system.app.registry, model, system.executor, program, mutationConfig(system.app, system.executor), nil)
}

// executeScalarMutationProgram executes the complete ordered program on one
// transaction. When binding already carries a transaction, ownership remains
// with the outer callback: this function neither commits nor rolls it back.
// For an ordinary client it owns begin/rollback/commit and rolls back every
// failure, including context cancellation and identity verification failure.
func executeScalarMutationProgram(ctx context.Context, database *sqlx.DB, provider policyir.Provider, registry *schema.Registry, model policyir.ModelID, binding *executionBinding, program mutationsql.Program) (result scalarMutationExecution, err error) {
	limits, _ := normalizeMutationLimits(MutationLimits{})
	return executeScalarMutationProgramConfigured(ctx, database, provider, registry, model, binding, program, executionMutationConfig{enabled: true, provider: provider, limits: limits}, nil)
}

func executeScalarMutationProgramConfigured(ctx context.Context, database *sqlx.DB, provider policyir.Provider, registry *schema.Registry, model policyir.ModelID, binding *executionBinding, program mutationsql.Program, config executionMutationConfig, observer scalarMutationStatementObserver) (result scalarMutationExecution, err error) {
	return executeScalarMutationProgramWithObservers(ctx, database, provider, registry, model, binding, program, config, observer, nil)
}

func executeScalarMutationProgramWithObservers(ctx context.Context, database *sqlx.DB, provider policyir.Provider, registry *schema.Registry, model policyir.ModelID, binding *executionBinding, program mutationsql.Program, config executionMutationConfig, observer scalarMutationStatementObserver, verified scalarMutationVerifiedObserver) (result scalarMutationExecution, err error) {
	if ctx == nil || database == nil || registry == nil || model == (policyir.ModelID{}) || binding == nil {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "context, database, registry, model, and execution binding are required", nil)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return scalarMutationExecution{}, contextErr
	}
	if err := validateScalarProgramProvider(provider, program); err != nil {
		return scalarMutationExecution{}, err
	}
	if _, err := binding.queryerFor(database); err != nil {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "execution binding is unavailable", err)
	}

	if binding.transaction != nil {
		transaction, transactionErr := binding.transactionFor(database)
		if transactionErr != nil {
			return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "bound transaction is unavailable", transactionErr)
		}
		return executeScalarProgramOnTransactionObserved(ctx, transaction, binding, registry, model, provider, program, observer, verified)
	}
	if binding.scoped {
		queryer, queryErr := binding.queryerFor(database)
		if queryErr != nil {
			return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "scoped transaction executor is unavailable", queryErr)
		}
		return executeScalarProgramOnQueryerObserved(ctx, queryer, binding, registry, model, provider, program, observer, verified)
	}

	transaction, beginErr := database.BeginTxx(ctx, nil)
	if beginErr != nil {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationProviderFailureKind(beginErr), 0, 0, "transaction could not begin", beginErr)
	}
	defer undoOnPanic(func() { _ = transaction.Rollback() })

	transactionBinding := transactionExecution(database, transaction)
	if err := transactionBinding.enableMutation(config); err != nil {
		return scalarMutationExecution{}, rollbackScalarMutation(transaction, err)
	}
	defer transactionBinding.close()
	result, err = executeScalarProgramOnTransactionObserved(ctx, transaction, transactionBinding, registry, model, provider, program, observer, verified)
	if err != nil {
		transactionBinding.discardMutation()
		return scalarMutationExecution{}, rollbackScalarMutation(transaction, err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		transactionBinding.discardMutation()
		return scalarMutationExecution{}, rollbackScalarMutation(transaction, contextErr)
	}
	if err := flushMutationBinding(ctx, transaction, transactionBinding); err != nil {
		transactionBinding.discardMutation()
		return scalarMutationExecution{}, rollbackScalarMutation(transaction, err)
	}
	if commitErr := transaction.Commit(); commitErr != nil {
		transactionBinding.discardMutation()
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationProviderFailureKind(commitErr), 0, 0, "transaction commit failed", commitErr)
	}
	commitMutationBinding(ctx, transactionBinding)
	return result, nil
}

func validateScalarProgramProvider(provider policyir.Provider, program mutationsql.Program) error {
	if program.Provider() != provider {
		return scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "program provider does not match the application", nil)
	}
	switch provider {
	case policyir.ProviderSQLite:
		if program.TransactionRequirement() != mutationsql.SQLiteImmediateTransaction {
			return scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "SQLite program does not require an immediate transaction", nil)
		}
	case policyir.ProviderPostgreSQL:
		if program.TransactionRequirement() != mutationsql.PostgreSQLTransaction {
			return scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "PostgreSQL program has the wrong transaction requirement", nil)
		}
	default:
		return scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "mutation provider is unsupported", nil)
	}
	if operation := program.Operation(); operation != mutationir.Create && operation != mutationir.Update && operation != mutationir.Delete {
		return scalarMutationError(operation, scalarMutationInvalid, 0, 0, "program is not a root scalar create, update, or delete", nil)
	}
	if len(program.Statements()) == 0 {
		return scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "program contains no statements", nil)
	}
	return nil
}

func executeScalarProgramOnTransactionObserved(ctx context.Context, transaction *sqlx.Tx, binding *executionBinding, registry *schema.Registry, model policyir.ModelID, provider policyir.Provider, program mutationsql.Program, observer scalarMutationStatementObserver, verified scalarMutationVerifiedObserver) (scalarMutationExecution, error) {
	if transaction == nil {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "transaction is required", nil)
	}
	if binding == nil || binding.transaction != transaction {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "transaction binding is unavailable", nil)
	}
	queryer, err := binding.queryerFor(binding.database)
	if err != nil {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "transaction queryer is unavailable", err)
	}
	return executeScalarProgramOnQueryerObserved(ctx, queryer, binding, registry, model, provider, program, observer, verified)
}

// executeScalarProgramOnQueryer is the no-transaction-ownership core used by
// ordinary sqlx.Tx execution and by the root-upsert attempt kernel. The caller
// must already own the exact transaction/connection boundary.
func executeScalarProgramOnQueryer(ctx context.Context, queryer sqlx.QueryerContext, binding *executionBinding, registry *schema.Registry, model policyir.ModelID, provider policyir.Provider, program mutationsql.Program, observer scalarMutationStatementObserver) (execution scalarMutationExecution, executionErr error) {
	return executeScalarProgramOnQueryerObserved(ctx, queryer, binding, registry, model, provider, program, observer, nil)
}

func executeScalarProgramOnQueryerObserved(ctx context.Context, queryer sqlx.QueryerContext, binding *executionBinding, registry *schema.Registry, model policyir.ModelID, provider policyir.Provider, program mutationsql.Program, observer scalarMutationStatementObserver, verified scalarMutationVerifiedObserver) (execution scalarMutationExecution, executionErr error) {
	defer func() {
		if executionErr == nil || binding == nil || !binding.mutation.enabled {
			return
		}
		if state, err := binding.mutationState(); err == nil {
			state.poison(executionErr)
		}
	}()
	if queryer == nil {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "transaction queryer is required", nil)
	}
	if program.RequiresConcurrencyPrecheck() {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "optimistic-concurrency program lacks a validated expectation", nil)
	}
	statements := program.Statements()
	result := scalarMutationExecution{operation: program.Operation(), statements: make([]scalarMutationStatementResult, 0, len(statements))}
	for index, statement := range statements {
		if contextErr := ctx.Err(); contextErr != nil {
			return scalarMutationExecution{}, contextErr
		}
		arguments, err := scalarMutationArguments(program.Operation(), uint32(index), statement, result.statements)
		if err != nil {
			return scalarMutationExecution{}, err
		}
		row, err := queryExactlyOneMutationRow(ctx, queryer, registry, model, provider, program.Operation(), uint32(index), statement, arguments)
		if err != nil {
			return scalarMutationExecution{}, err
		}
		result.statements = append(result.statements, row)
		if observer != nil {
			if err := observer(ctx, binding, uint32(index), result); err != nil {
				return scalarMutationExecution{}, err
			}
		}
	}
	if err := commitScalarMutationExecution(ctx, binding, registry, model, program, result, verified); err != nil {
		return scalarMutationExecution{}, err
	}
	return result, nil
}

// executeVersionedScalarProgramAfterPrecheck consumes the one already locked,
// expectation-validated pre-image as statement zero and executes only the CAS
// remainder. Re-selecting after the before hook would both add work and mask an
// incomplete original authorization snapshot.
func executeVersionedScalarProgramAfterPrecheck(ctx context.Context, queryer sqlx.QueryerContext, binding *executionBinding, registry *schema.Registry, model policyir.ModelID, provider policyir.Provider, program mutationsql.Program, preimage scalarMutationStatementResult, observer scalarMutationStatementObserver, verified scalarMutationVerifiedObserver) (execution scalarMutationExecution, executionErr error) {
	defer func() {
		if executionErr == nil || binding == nil || !binding.mutation.enabled {
			return
		}
		if state, err := binding.mutationState(); err == nil {
			state.poison(executionErr)
		}
	}()
	if queryer == nil || !program.RequiresConcurrencyPrecheck() || preimage.role != mutationsql.SelectPreImage {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvalid, 0, 0, "validated concurrency pre-image is required", nil)
	}
	statements := program.Statements()
	if len(statements) < 2 || statements[0].Role() != mutationsql.SelectPreImage {
		return scalarMutationExecution{}, scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "concurrency program has no apply remainder", nil)
	}
	result := scalarMutationExecution{operation: program.Operation(), statements: make([]scalarMutationStatementResult, 1, len(statements))}
	result.statements[0] = preimage.clone()
	if observer != nil {
		if err := observer(ctx, binding, 0, result); err != nil {
			return scalarMutationExecution{}, err
		}
	}
	for index := 1; index < len(statements); index++ {
		if contextErr := ctx.Err(); contextErr != nil {
			return scalarMutationExecution{}, contextErr
		}
		statement := statements[index]
		arguments, err := scalarMutationArguments(program.Operation(), uint32(index), statement, result.statements)
		if err != nil {
			return scalarMutationExecution{}, err
		}
		row, err := queryExactlyOneMutationRow(ctx, queryer, registry, model, provider, program.Operation(), uint32(index), statement, arguments)
		if err != nil {
			return scalarMutationExecution{}, err
		}
		result.statements = append(result.statements, row)
		if observer != nil {
			if err := observer(ctx, binding, uint32(index), result); err != nil {
				return scalarMutationExecution{}, err
			}
		}
	}
	if err := commitScalarMutationExecution(ctx, binding, registry, model, program, result, verified); err != nil {
		return scalarMutationExecution{}, err
	}
	return result, nil
}

// commitScalarMutationExecution is the single owner of everything a scalar
// program must do after its statements have run. Both the ordinary kernel and
// the optimistic-concurrency kernel reach it, so no scalar write can skip
// identity verification, field authorization, row accounting, or fact capture.
func commitScalarMutationExecution(ctx context.Context, binding *executionBinding, registry *schema.Registry, model policyir.ModelID, program mutationsql.Program, result scalarMutationExecution, verified scalarMutationVerifiedObserver) error {
	if err := verifyScalarMutationIdentity(program, result.statements); err != nil {
		return err
	}
	if err := verifyScalarMutationFieldAuthorizations(registry, program, result.statements); err != nil {
		return err
	}
	if verified != nil {
		if err := verified(ctx, binding, program, result); err != nil {
			return err
		}
	}
	if binding == nil || !binding.mutation.enabled {
		return nil
	}
	state, err := binding.mutationState()
	if err != nil {
		return err
	}
	if err := state.touch(1); err != nil {
		state.poison(err)
		return err
	}
	if requirement := program.FactRequirement(); requirement.Enabled() {
		before, after, imageErr := scalarMutationFactImages(registry, model, program, result)
		if imageErr != nil {
			state.poison(imageErr)
			return imageErr
		}
		if _, factErr := state.buildFact(registry, requirement, before, after, time.Now()); factErr != nil {
			state.poison(factErr)
			return factErr
		}
	}
	if program.SemanticIndexed() {
		if markErr := markScalarSemanticRecord(state, registry, model, program, result); markErr != nil {
			state.poison(markErr)
			return markErr
		}
	}
	return nil
}

func scalarMutationFactImages(registry *schema.Registry, model policyir.ModelID, program mutationsql.Program, execution scalarMutationExecution) (*mutationdecode.Row, *mutationdecode.Row, error) {
	requirement := program.FactRequirement()
	action, enabled := requirement.Action()
	if !enabled {
		return nil, nil, nil
	}
	verification := program.IdentityVerification()
	decodeAt := func(index uint32) (*mutationdecode.Row, error) {
		statement, ok := execution.statement(index)
		if !ok {
			return nil, fmt.Errorf("fact image statement %d is absent", index)
		}
		row, err := mutationdecode.FromReadCells(registry, model, statement.cells)
		if err != nil {
			return nil, err
		}
		return &row, nil
	}
	switch action {
	case mutationir.FactCreated:
		after, err := decodeAt(verification.AfterStatement())
		return nil, after, err
	case mutationir.FactUpdated:
		beforeIndex, ok := verification.BeforeStatement()
		if !ok {
			return nil, nil, fmt.Errorf("updated fact has no before-image statement")
		}
		before, err := decodeAt(beforeIndex)
		if err != nil {
			return nil, nil, err
		}
		after, err := decodeAt(verification.AfterStatement())
		return before, after, err
	case mutationir.FactDeleted:
		beforeIndex, ok := verification.BeforeStatement()
		if !ok {
			return nil, nil, fmt.Errorf("deleted fact has no before-image statement")
		}
		before, err := decodeAt(beforeIndex)
		return before, nil, err
	default:
		return nil, nil, fmt.Errorf("unknown fact action %d", action)
	}
}

func scalarMutationArguments(operation mutationir.Operation, statementIndex uint32, statement mutationsql.Statement, results []scalarMutationStatementResult) ([]any, error) {
	bindings := statement.Bindings()
	arguments := make([]any, len(bindings))
	for index, binding := range bindings {
		if value, ok := binding.StaticValue(); ok {
			arguments[index] = cloneMutationPhysicalValue(value)
			continue
		}
		source, field, ok := binding.PriorResult()
		if !ok || uint64(source) >= uint64(len(results)) || source >= statementIndex {
			return nil, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "prior-result binding has an invalid source", nil)
		}
		value, present := results[source].fields[field]
		if !present {
			return nil, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "prior-result binding field is absent", nil)
		}
		arguments[index] = cloneMutationPhysicalValue(value)
	}
	return arguments, nil
}

func queryExactlyOneMutationRow(ctx context.Context, queryer sqlx.QueryerContext, registry *schema.Registry, model policyir.ModelID, provider policyir.Provider, operation mutationir.Operation, statementIndex uint32, statement mutationsql.Statement, arguments []any) (scalarMutationStatementResult, error) {
	if statement.Cardinality() != mutationsql.ExactlyOneRow {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "statement has an unsupported cardinality contract", nil)
	}
	columns := statement.Columns()
	authorizations := statement.AuthorizationColumns()
	if len(columns) == 0 {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "row-producing statement has no result columns", nil)
	}
	seen := make(map[policyir.FieldID]struct{}, len(columns))
	for _, column := range columns {
		if column.FieldID() == (policyir.FieldID{}) {
			return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "result column has a zero field identity", nil)
		}
		if _, duplicate := seen[column.FieldID()]; duplicate {
			return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "result column field appears more than once", nil)
		}
		seen[column.FieldID()] = struct{}{}
	}
	seenAuthorizations := make(map[policyir.FieldID]struct{}, len(authorizations))
	for _, authorization := range authorizations {
		if authorization.FieldID() == (policyir.FieldID{}) {
			return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "authorization column has a zero field identity", nil)
		}
		if _, duplicate := seenAuthorizations[authorization.FieldID()]; duplicate {
			return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "authorization column field appears more than once", nil)
		}
		seenAuthorizations[authorization.FieldID()] = struct{}{}
	}
	fieldOrder := make([]policyir.FieldID, len(columns))
	for index, column := range columns {
		fieldOrder[index] = column.FieldID()
	}
	decoder, err := readdecode.NewFields(model, registry, provider, fieldOrder)
	if err != nil {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "exact mutation decoder could not be built", err)
	}

	recordQueryerStatement(ctx, queryer)
	rows, err := queryer.QueryxContext(ctx, statement.SQL(), arguments...)
	if err != nil {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationProviderFailureKind(err), statement.Role(), statementIndex, "mutation statement failed", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if streamErr := rows.Err(); streamErr != nil {
			return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationProviderFailureKind(streamErr), statement.Role(), statementIndex, "mutation result stream failed", streamErr)
		}
		return scalarMutationStatementResult{}, zeroRowMutationError(operation, statement.Role(), statementIndex)
	}
	scan := decoder.NewScan()
	destinations := scan.Destinations()
	rawGrants := make([]any, len(authorizations))
	for index := range rawGrants {
		destinations = append(destinations, &rawGrants[index])
	}
	if err := rows.Scan(destinations...); err != nil {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationProvider, statement.Role(), statementIndex, "mutation row scan failed", err)
	}
	if rows.Next() {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationInvariant, statement.Role(), statementIndex, "mutation statement returned more than one row", nil)
	}
	if err := rows.Err(); err != nil {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationProviderFailureKind(err), statement.Role(), statementIndex, "mutation result stream failed", err)
	}
	if err := rows.Close(); err != nil {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationProvider, statement.Role(), statementIndex, "mutation result stream could not close", err)
	}
	values := scan.RawValues()
	cells, err := scan.Decode()
	if err != nil {
		return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationProvider, statement.Role(), statementIndex, "mutation row could not be decoded exactly", err)
	}
	result := scalarMutationStatementResult{role: statement.Role(), fields: make(map[policyir.FieldID]any, len(columns)), cells: cells, grants: make(map[policyir.FieldID]bool, len(authorizations))}
	for index, column := range columns {
		result.fields[column.FieldID()] = cloneMutationPhysicalValue(values[index])
	}
	for index, authorization := range authorizations {
		granted, decodeErr := decodeMutationAuthorizationGrant(rawGrants[index])
		if decodeErr != nil {
			return scalarMutationStatementResult{}, scalarMutationError(operation, scalarMutationProvider, statement.Role(), statementIndex, "field authorization result is not an exact boolean", decodeErr)
		}
		result.grants[authorization.FieldID()] = granted
	}
	return result, nil
}

func decodeMutationAuthorizationGrant(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case int64:
		if typed == 0 || typed == 1 {
			return typed == 1, nil
		}
	case int32:
		if typed == 0 || typed == 1 {
			return typed == 1, nil
		}
	case []byte:
		if string(typed) == "0" || string(typed) == "1" {
			return string(typed) == "1", nil
		}
	case string:
		if typed == "0" || typed == "1" {
			return typed == "1", nil
		}
	}
	return false, fmt.Errorf("unexpected authorization boolean %T", value)
}

func zeroRowMutationError(operation mutationir.Operation, role mutationsql.Role, statement uint32) error {
	switch role {
	case mutationsql.SelectPreImage:
		return scalarMutationError(operation, scalarMutationNotFound, role, statement, "mutation target was not found", nil)
	case mutationsql.VerifyPostcondition:
		return scalarMutationError(operation, scalarMutationForbidden, role, statement, "persisted mutation result was not authorized", nil)
	case mutationsql.ApplyUpdate:
		return scalarMutationError(operation, scalarMutationConflict, role, statement, "locked update target changed before application", nil)
	case mutationsql.ApplyDelete:
		return scalarMutationError(operation, scalarMutationConflict, role, statement, "locked delete target changed before application", nil)
	default:
		return scalarMutationError(operation, scalarMutationInvariant, role, statement, "row-producing mutation statement returned no row", nil)
	}
}

func verifyScalarMutationFieldAuthorizations(registry *schema.Registry, program mutationsql.Program, results []scalarMutationStatementResult) error {
	if program.Operation() != mutationir.Update || program.Stance() != mutationir.Caller {
		return nil
	}
	verification := program.IdentityVerification()
	beforeIndex, hasBefore := verification.BeforeStatement()
	afterIndex := verification.AfterStatement()
	if !hasBefore || uint64(beforeIndex) >= uint64(len(results)) || uint64(afterIndex) >= uint64(len(results)) {
		return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "field authorization images are absent", nil)
	}
	authored := program.AuthoredFields()
	beforeCells := make(map[policyir.FieldID]readdecode.Cell, len(results[beforeIndex].cells))
	for _, cell := range results[beforeIndex].cells {
		beforeCells[cell.FieldID()] = cell
	}
	afterCells := make(map[policyir.FieldID]readdecode.Cell, len(results[afterIndex].cells))
	for _, cell := range results[afterIndex].cells {
		afterCells[cell.FieldID()] = cell
	}
	beforeImage := make([]mutationdecode.Cell, 0, len(authored))
	afterImage := make([]mutationdecode.Cell, 0, len(authored))
	for _, field := range authored {
		before, beforeOK := beforeCells[field]
		after, afterOK := afterCells[field]
		if !beforeOK || !afterOK {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, mutationsql.ApplyUpdate, afterIndex, "authored field is absent from exact before/after images", nil)
		}
		beforeCell, err := exactMutationCell(before)
		if err != nil {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, mutationsql.SelectPreImage, beforeIndex, "locked authored field could not form an exact image", err)
		}
		afterCell, err := exactMutationCell(after)
		if err != nil {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, mutationsql.ApplyUpdate, afterIndex, "persisted authored field could not form an exact image", err)
		}
		beforeImage = append(beforeImage, beforeCell)
		afterImage = append(afterImage, afterCell)
	}
	beforeRow, err := mutationdecode.NewRow(registry, program.ModelID(), beforeImage)
	if err != nil {
		return scalarMutationError(program.Operation(), scalarMutationInvariant, mutationsql.SelectPreImage, beforeIndex, "locked authored image is invalid", err)
	}
	afterRow, err := mutationdecode.NewRow(registry, program.ModelID(), afterImage)
	if err != nil {
		return scalarMutationError(program.Operation(), scalarMutationInvariant, mutationsql.ApplyUpdate, afterIndex, "persisted authored image is invalid", err)
	}
	changed, err := mutationdecode.AuthoredFields(registry, beforeRow, afterRow, authored)
	if err != nil {
		return scalarMutationError(program.Operation(), scalarMutationInvariant, mutationsql.ApplyUpdate, afterIndex, "exact authored-field diff failed", err)
	}
	for _, field := range changed {
		granted, present := results[beforeIndex].grants[field]
		if !present {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, mutationsql.SelectPreImage, beforeIndex, "changed caller-authored field has no precomputed grant", nil)
		}
		if !granted {
			return scalarMutationError(program.Operation(), scalarMutationForbidden, mutationsql.ApplyUpdate, afterIndex, "changed caller-authored field was denied on the locked pre-image", nil)
		}
	}
	return nil
}

func exactMutationCell(cell readdecode.Cell) (mutationdecode.Cell, error) {
	if cell.IsNull() {
		return mutationdecode.Null(cell.FieldID()), nil
	}
	value, ok := cell.PolicyValue()
	if !ok {
		return mutationdecode.Cell{}, fmt.Errorf("decoded cell has no exact policy value")
	}
	return mutationdecode.Value(cell.FieldID(), value), nil
}

func verifyScalarMutationIdentity(program mutationsql.Program, results []scalarMutationStatementResult) error {
	verification := program.IdentityVerification()
	fields := verification.Fields()
	if len(fields) == 0 {
		return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "identity verification has no fields", nil)
	}
	after, err := scalarMutationIdentityTuple(program.Operation(), results, verification.AfterStatement(), fields)
	if err != nil {
		return err
	}
	beforeIndex, hasBefore := verification.BeforeStatement()
	switch verification.Behavior() {
	case mutationir.IdentityProduced:
		if hasBefore {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "produced identity unexpectedly has a before image", nil)
		}
	case mutationir.IdentityUnchanged:
		if !hasBefore {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "unchanged identity has no before image", nil)
		}
		before, tupleErr := scalarMutationIdentityTuple(program.Operation(), results, beforeIndex, fields)
		if tupleErr != nil {
			return tupleErr
		}
		for index := range before {
			if !equalMutationPhysicalValue(before[index], after[index]) {
				return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "an identity declared unchanged was modified", nil)
			}
		}
	case mutationir.IdentityMayChange:
		if !hasBefore {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "changeable identity has no before image", nil)
		}
		if _, tupleErr := scalarMutationIdentityTuple(program.Operation(), results, beforeIndex, fields); tupleErr != nil {
			return tupleErr
		}
	default:
		return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "identity behavior is invalid for a scalar mutation", nil)
	}
	return nil
}

func scalarMutationIdentityTuple(operation mutationir.Operation, results []scalarMutationStatementResult, statement uint32, fields []policyir.FieldID) ([]any, error) {
	if uint64(statement) >= uint64(len(results)) {
		return nil, scalarMutationError(operation, scalarMutationInvariant, 0, statement, "identity statement is absent", nil)
	}
	tuple := make([]any, len(fields))
	for index, field := range fields {
		value, present := results[statement].fields[field]
		if !present || value == nil {
			return nil, scalarMutationError(operation, scalarMutationInvariant, results[statement].role, statement, "identity field is absent or NULL", nil)
		}
		tuple[index] = cloneMutationPhysicalValue(value)
	}
	return tuple, nil
}

func equalMutationPhysicalValue(left, right any) bool {
	switch typed := left.(type) {
	case []byte:
		other, ok := right.([]byte)
		return ok && bytes.Equal(typed, other)
	case time.Time:
		other, ok := right.(time.Time)
		return ok && typed.Equal(other)
	default:
		return reflect.DeepEqual(left, right)
	}
}

func cloneMutationPhysicalValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case time.Time:
		return typed
	default:
		return value
	}
}

// scalarMutationProviderFailureKind translates only the faults the shared
// provider classifier recognises. Driver text, physical constraint names, SQL,
// and values remain exclusively in the trusted cause. Unknown provider failures
// fail closed as BAD_USER_INPUT at the public boundary rather than exposing or
// guessing their semantics.
func scalarMutationProviderFailureKind(err error) scalarMutationFailureKind {
	if mutationupsert.ClassifyProviderFault(err) == mutationupsert.ProviderFaultNone {
		return scalarMutationProvider
	}
	return scalarMutationConflict
}

func rollbackScalarMutation(transaction *sqlx.Tx, cause error) error {
	if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		return errors.Join(cause, scalarMutationError(0, scalarMutationProvider, 0, 0, "transaction rollback failed", rollbackErr))
	}
	return cause
}

func scalarMutationError(operation mutationir.Operation, kind scalarMutationFailureKind, role mutationsql.Role, statement uint32, detail string, cause error) error {
	return &scalarMutationFailure{kind: kind, operation: operation, role: role, statement: statement, detail: detail, cause: cause}
}

func publicScalarMutationError(model golem.ModelID, err error) error {
	var hook *mutationHookFailure
	if errors.As(err, &hook) {
		return golem.RuntimeOperationError(golem.CodeBadUserInput, string(hook.operation), model, golem.FieldID{}, "mutation hook rejected the operation", err)
	}
	var failure *scalarMutationFailure
	if !errors.As(err, &failure) {
		return err
	}
	operation := scalarMutationOperationName(failure.operation)
	switch failure.kind {
	case scalarMutationNotFound:
		return golem.RuntimeOperationError(golem.CodeNotFound, operation, model, golem.FieldID{}, "record not found", err)
	case scalarMutationForbidden:
		return golem.RuntimeOperationError(golem.CodeForbidden, operation, model, golem.FieldID{}, "mutation is not authorized", err)
	case scalarMutationConflict:
		return golem.RuntimeOperationError(golem.CodeConflict, operation, model, golem.FieldID{}, "mutation conflicted", err)
	default:
		return golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, "mutation could not be completed", err)
	}
}

func scalarMutationOperationName(operation mutationir.Operation) string {
	switch operation {
	case mutationir.Create:
		return "create"
	case mutationir.Update:
		return "update"
	case mutationir.Delete:
		return "delete"
	case mutationir.Upsert:
		return "upsert"
	default:
		return "mutation"
	}
}
