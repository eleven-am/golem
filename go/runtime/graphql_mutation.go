package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
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
		return rowResult(executeCallerRootScalar(ctx, caller, descriptor, mutationir.Update, &input, &target, projection))
	case golem.RuntimeMutationUpsert:
		target, targetOK := request.Target()
		create, createOK := request.CreateInput()
		update, updateOK := request.UpdateInput()
		if !targetOK || !createOK || !updateOK {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		return rowResult(executeCallerRootUpsert(ctx, caller, descriptor, target, create, update, projection))
	case golem.RuntimeMutationDelete:
		target, ok := request.Target()
		if !ok {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		return rowResult(executeCallerRootScalar(ctx, caller, descriptor, mutationir.Delete, nil, &target, projection))
	case golem.RuntimeMutationUpdateMany, golem.RuntimeMutationDeleteMany:
		where, ok := request.Where()
		if !ok {
			return golem.RuntimeMutationResult{}, frozenMutationShape(model)
		}
		operation := mutationir.DeleteMany
		hookRequest := golem.RuntimeDeleteManyMutationHookRequest(model, where)
		if request.Operation() == golem.RuntimeMutationUpdateMany {
			operation = mutationir.UpdateMany
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
	default:
		return golem.RuntimeMutationResult{}, frozenMutationShape(model)
	}
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
