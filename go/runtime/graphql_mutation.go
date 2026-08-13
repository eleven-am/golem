package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/concurrencyclaim"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/observe"
)

// CallerMutationModelAdapter is the generated, model-specific bridge required
// to invoke generic P4 clients after GraphQL has erased the application's Go
// model type. Only Caller-backed constructors exist; System has no GraphQL
// mutation adapter.
type CallerMutationModelAdapter[P, A any] struct {
	model   golem.ModelID
	execute func(context.Context, *Caller[P, A], golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error)
}

// CallerMutationModel binds one generated descriptor to the existing P4
// caller mutation engine. It adds no policy, SQL, hook, transaction,
// invalidation, or fact behavior of its own.
func CallerMutationModel[P, A, M any](descriptor golem.ModelDescriptor[M]) CallerMutationModelAdapter[P, A] {
	model := descriptor.Metadata().ModelID()
	return CallerMutationModelAdapter[P, A]{
		model: model,
		execute: func(ctx context.Context, caller *Caller[P, A], request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {
			return executeCallerFrozenMutation(ctx, caller, descriptor, request)
		},
	}
}

// CallerMutationExecution combines one execution-scoped caller with the
// generated model descriptor inventory. It deliberately delegates reads to the
// same caller and dispatches each mutation call independently to P4, preserving
// GraphQL's serial field order without creating a cross-field transaction.
type CallerMutationExecution[P, A any] struct {
	caller *Caller[P, A]
	models map[golem.ModelID]CallerMutationModelAdapter[P, A]
}

func NewCallerMutationExecution[P, A any](caller *Caller[P, A], models ...CallerMutationModelAdapter[P, A]) (*CallerMutationExecution[P, A], error) {
	if caller == nil || caller.app == nil {
		return nil, fmt.Errorf("P5_RUNTIME_MUTATION: caller execution is unavailable")
	}
	result := &CallerMutationExecution[P, A]{caller: caller, models: make(map[golem.ModelID]CallerMutationModelAdapter[P, A], len(models))}
	for _, model := range models {
		if model.model == (golem.ModelID{}) || model.execute == nil {
			return nil, fmt.Errorf("P5_RUNTIME_MUTATION: model adapter is invalid")
		}
		if _, duplicate := result.models[model.model]; duplicate {
			return nil, fmt.Errorf("P5_RUNTIME_MUTATION: model adapter is duplicated")
		}
		result.models[model.model] = model
	}
	return result, nil
}

func (execution *CallerMutationExecution[P, A]) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	if execution == nil || execution.caller == nil {
		return nil, golem.RuntimeOperationError(golem.CodeUnauthenticated, "read", request.ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	return execution.caller.ExecuteFrozenRead(ctx, request)
}

func (execution *CallerMutationExecution[P, A]) ExecuteFrozenMutation(ctx context.Context, request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {
	if execution == nil || execution.caller == nil {
		return golem.RuntimeMutationResult{}, golem.RuntimeOperationError(golem.CodeUnauthenticated, "mutation", request.ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	model, ok := execution.models[request.ModelID()]
	if !ok || model.execute == nil {
		return golem.RuntimeMutationResult{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", request.ModelID(), golem.FieldID{}, "mutation model is not generated", nil)
	}
	return model.execute(ctx, execution.caller, request)
}

func executeCallerFrozenMutation[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {
	model := descriptor.Metadata().ModelID()
	if caller == nil || caller.app == nil {
		return golem.RuntimeMutationResult{}, golem.RuntimeOperationError(golem.CodeUnauthenticated, "mutation", model, golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if request.ModelID() != model {
		return golem.RuntimeMutationResult{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", model, golem.FieldID{}, "mutation request belongs to another model", nil)
	}
	versioned, err := frozenMutationConcurrencyMode(caller.app.registry, model, request)
	if err != nil {
		return golem.RuntimeMutationResult{}, err
	}
	var projection scalarMutationProjection
	if frozen, present := request.Projection(); present {
		var err error
		projection, err = prepareCallerFrozenMutationProjection(caller, model, frozen)
		if err != nil {
			return golem.RuntimeMutationResult{}, err
		}
	}
	rowResult := func(row golem.Row[M], err error) (golem.RuntimeMutationResult, error) {
		if err != nil {
			return golem.RuntimeMutationResult{}, err
		}
		return golem.RuntimeMutationRowResult(golem.RuntimeModelRowFromTyped(row)), nil
	}
	switch request.Operation() {
	case golem.RuntimeMutationCreate:
		input, ok := request.Input()
		if !ok {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		return rowResult(executeCallerRootScalar(ctx, caller, descriptor, mutationir.Create, &input, nil, projection))
	case golem.RuntimeMutationUpdate:
		target, targetOK := request.Target()
		input, inputOK := request.Input()
		if !targetOK || !inputOK {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		if versioned {
			expected, _ := request.ExistingVersion()
			claim, claimErr := freezeScalarConcurrencyClaim(expected)
			if claimErr != nil {
				return golem.RuntimeMutationResult{}, publicMutationPreparationError(mutationir.Update, model, claimErr)
			}
			if len(input.Relations()) != 0 {
				return golem.RuntimeMutationResult{}, publicMutationPreparationError(mutationir.Update, model, fmt.Errorf("versioned nested mutation requires an expectation for every written row"))
			}
			return rowResult(executeCallerVersionedRootScalar(ctx, caller, descriptor, mutationir.Update, &input, target, claim, projection, mutationir.Update))
		}
		return rowResult(executeCallerRootScalar(ctx, caller, descriptor, mutationir.Update, &input, &target, projection))
	case golem.RuntimeMutationUpsert:
		target, targetOK := request.Target()
		create, createOK := request.CreateInput()
		update, updateOK := request.UpdateInput()
		if !targetOK || !createOK || !updateOK {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		if versioned {
			if len(create.Relations()) != 0 || len(update.Relations()) != 0 {
				return golem.RuntimeMutationResult{}, publicMutationPreparationError(mutationir.Upsert, model, fmt.Errorf("versioned nested upsert requires an expectation for every written row"))
			}
			if _, bindErr := mutationbind.CreateInput(create, caller.app.registry); bindErr != nil {
				return golem.RuntimeMutationResult{}, publicMutationPreparationError(mutationir.Upsert, model, bindErr)
			}
			if _, bindErr := mutationbind.UpdateInput(update, caller.app.registry); bindErr != nil {
				return golem.RuntimeMutationResult{}, publicMutationPreparationError(mutationir.Upsert, model, bindErr)
			}
			expected, _ := request.ConcurrencyExpectation()
			claim := concurrencyclaim.ConcurrencyExpectation(expected)
			if concurrencyclaim.IsAbsent(claim) {
				return rowResult(executeCallerAbsentVersionedUpsert(ctx, caller, descriptor, target, create, update, projection))
			}
			return rowResult(executeCallerExistingVersionedUpsert(ctx, caller, descriptor, target, update, existingExpectationConcurrencyClaim{expectation: claim}, projection))
		}
		return rowResult(executeCallerRootUpsert(ctx, caller, descriptor, target, create, update, projection))
	case golem.RuntimeMutationDelete:
		target, ok := request.Target()
		if !ok {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		if versioned {
			expected, _ := request.ExistingVersion()
			claim, claimErr := freezeScalarConcurrencyClaim(expected)
			if claimErr != nil {
				return golem.RuntimeMutationResult{}, publicMutationPreparationError(mutationir.Delete, model, claimErr)
			}
			return rowResult(executeCallerVersionedRootScalar(ctx, caller, descriptor, mutationir.Delete, nil, target, claim, projection, mutationir.Delete))
		}
		return rowResult(executeCallerRootScalar(ctx, caller, descriptor, mutationir.Delete, nil, &target, projection))
	case golem.RuntimeMutationUpdateMany, golem.RuntimeMutationDeleteMany:
		return executeCallerFrozenBatchMutation(ctx, caller, model, request)
	default:
		return golem.RuntimeMutationResult{}, frozenMutationShape(model)
	}
}

func frozenMutationConcurrencyMode(registry *policyschema.Registry, model golem.ModelID, request golem.RuntimeMutationRequest) (bool, error) {
	// The concrete registry remains the sole owner of whether the model is
	// versioned. The frozen request may retain a closed claim, but that claim is
	// never sufficient to manufacture authority for another model/operation.
	if registry == nil {
		return false, golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", model, golem.FieldID{}, "mutation request is invalid", fmt.Errorf("mutation registry is absent"))
	}
	fact, ok := registry.Model(model)
	versioned := false
	if ok {
		_, versioned = fact.OptimisticConcurrency()
	}
	_, hasExisting := request.ExistingVersion()
	_, hasExpectation := request.ConcurrencyExpectation()
	operation := runtimeMutationOperationName(request.Operation())
	if !versioned {
		if hasExisting || hasExpectation {
			return false, golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, "mutation request is invalid", fmt.Errorf("non-versioned mutation carried a concurrency expectation"))
		}
		return false, nil
	}
	valid := request.Operation() == golem.RuntimeMutationCreate && !hasExisting && !hasExpectation ||
		(request.Operation() == golem.RuntimeMutationUpdate || request.Operation() == golem.RuntimeMutationDelete) && hasExisting && !hasExpectation ||
		request.Operation() == golem.RuntimeMutationUpsert && !hasExisting && hasExpectation
	if !valid {
		return true, golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, "mutation request is invalid", fmt.Errorf("optimistic-concurrency request has no exact operation claim"))
	}
	return true, nil
}

func runtimeMutationOperationName(operation golem.RuntimeMutationOperation) string {
	switch operation {
	case golem.RuntimeMutationCreate:
		return "create"
	case golem.RuntimeMutationUpdate:
		return "update"
	case golem.RuntimeMutationUpsert:
		return "upsert"
	case golem.RuntimeMutationDelete:
		return "delete"
	case golem.RuntimeMutationUpdateMany:
		return "updateMany"
	case golem.RuntimeMutationDeleteMany:
		return "deleteMany"
	default:
		return "mutation"
	}
}

func executeCallerFrozenBatchMutation[P, A any](ctx context.Context, caller *Caller[P, A], model golem.ModelID, request golem.RuntimeMutationRequest) (result golem.RuntimeMutationResult, resultErr error) {
	operation := mutationir.DeleteMany
	if request.Operation() == golem.RuntimeMutationUpdateMany {
		operation = mutationir.UpdateMany
	}
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, caller.app, caller.executor, model, observe.KindMutation, mutationObservationOperation(operation))
	defer func() {
		if count, ok := result.Count(); ok && observation != nil {
			observation.SetAggregateCount(count)
		}
		finishDeferredObservation(observation, deferredObservation, resultErr)
	}()
	where, ok := request.Where()
	if !ok {
		return golem.RuntimeMutationResult{}, frozenMutationShape(model)
	}
	hookRequest := golem.RuntimeDeleteManyMutationHookRequest(model, where)
	if operation == mutationir.UpdateMany {
		input, inputOK := request.Input()
		if !inputOK {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		hookRequest = golem.RuntimeUpdateManyMutationHookRequest(model, where, input)
	}
	program, err := prepareCallerFrozenBatchHooks(ctx, caller, model, hookRequest)
	if err != nil {
		return golem.RuntimeMutationResult{}, publicBatchPreparationError(operation, model, err)
	}
	hooks := callerMutationHookExecution[A]{bindings: caller.app.bindings, actor: caller.actor, executor: func(binding *executionBinding) golem.HookExecutor {
		return newCallerHookExecutor(caller, binding)
	}}
	count, err := executePublicBatch(ctx, caller.app, caller.executor, program, &hooks)
	if err != nil {
		return golem.RuntimeMutationResult{}, err
	}
	return golem.RuntimeMutationCountResult(count)
}

func prepareCallerFrozenMutationProjection[P, A any](caller *Caller[P, A], model golem.ModelID, frozen golem.FrozenReadRequest) (scalarMutationProjection, error) {
	if frozen.ModelID() != model || frozen.Operation() != golem.ReadFindMany || frozen.ProjectionMode() != golem.ProjectionSelect || len(frozen.Selection()) == 0 {
		return scalarMutationProjection{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", model, golem.FieldID{}, "mutation result projection is invalid", nil)
	}
	prepared, err := caller.Prepare(frozen)
	if err != nil {
		return scalarMutationProjection{}, err
	}
	planned, err := preparePlan(prepared, caller.app.registry, caller.app.readLimits.plan)
	if err != nil {
		return scalarMutationProjection{}, publicPlanError(prepared, err)
	}
	return scalarMutationProjection{planned: planned, active: true}, nil
}

func frozenMutationShape(model golem.ModelID) error {
	return golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", model, golem.FieldID{}, "frozen mutation request shape is invalid", nil)
}
