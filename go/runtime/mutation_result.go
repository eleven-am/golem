package runtime

import (
	"context"
	"errors"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/observe"
)

type scalarMutationProjection struct {
	planned readplan.Plan
	active  bool
}

func (projection scalarMutationProjection) requirements(model policyir.ModelID) (mutationir.ImageRequirements, error) {
	fields := make([]policyir.FieldID, 0)
	if projection.active {
		planned := projection.planned.Fields()
		fields = make([]policyir.FieldID, len(planned))
		for index, field := range planned {
			fields[index] = field.FieldID()
		}
	}
	return mutationir.NewImageRequirements(model, fields, nil)
}

func prepareCallerScalarProjection[P, A, M any](caller *Caller[P, A], descriptor golem.ModelDescriptor[M], projections []golem.Projection[M]) (scalarMutationProjection, error) {
	model := descriptor.Metadata().ModelID()
	if caller == nil || caller.app == nil {
		return scalarMutationProjection{}, golem.RuntimeOperationError(golem.CodeUnauthenticated, "mutation", model, golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if len(projections) == 0 {
		return scalarMutationProjection{}, nil
	}
	if len(projections) != 1 || projections[0] == nil {
		return scalarMutationProjection{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", model, golem.FieldID{}, "mutation accepts at most one projection", nil)
	}
	frozen, err := golem.FreezeFindMany(descriptor, golem.RuntimeProjectionReadOption(projections[0]))
	if err != nil {
		return scalarMutationProjection{}, err
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

func prepareSystemScalarProjection[P, A, M any](system System[P, A], descriptor golem.ModelDescriptor[M], projections []golem.Projection[M]) (scalarMutationProjection, error) {
	model := descriptor.Metadata().ModelID()
	if system.app == nil {
		return scalarMutationProjection{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", model, golem.FieldID{}, "system execution is unavailable", nil)
	}
	if len(projections) == 0 {
		return scalarMutationProjection{}, nil
	}
	if len(projections) != 1 || projections[0] == nil {
		return scalarMutationProjection{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "mutation", model, golem.FieldID{}, "mutation accepts at most one projection", nil)
	}
	frozen, err := golem.FreezeFindMany(descriptor, golem.RuntimeProjectionReadOption(projections[0]))
	if err != nil {
		return scalarMutationProjection{}, err
	}
	prepared, err := system.Prepare(frozen)
	if err != nil {
		return scalarMutationProjection{}, err
	}
	planned, err := preparePlan(prepared, system.app.registry, system.app.readLimits.plan)
	if err != nil {
		return scalarMutationProjection{}, publicPlanError(prepared, err)
	}
	return scalarMutationProjection{planned: planned, active: true}, nil
}

// CallerCreate is the generated-client execution ABI for one authorized root
// scalar create. Hooks, facts, and nested operations are later P4 layers and
// cannot enter this scalar path; root upsert has its own guarded kernel.
func CallerCreate[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], input golem.CreateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	projection, err := prepareCallerScalarProjection(caller, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozen, err := golem.RuntimeFreezeCreateInput(input)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeCallerRootScalar(ctx, caller, descriptor, mutationir.Create, &frozen, nil, projection)
}

func CallerUpdate[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if caller != nil && caller.app != nil {
		if err := refuseLegacyVersionedMutation(caller.app.registry, descriptor.Metadata().ModelID(), "update"); err != nil {
			return golem.Row[M]{}, err
		}
	}
	projection, err := prepareCallerScalarProjection(caller, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenInput, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeCallerRootScalar(ctx, caller, descriptor, mutationir.Update, &frozenInput, &frozenTarget, projection)
}

func CallerDelete[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if caller != nil && caller.app != nil {
		if err := refuseLegacyVersionedMutation(caller.app.registry, descriptor.Metadata().ModelID(), "delete"); err != nil {
			return golem.Row[M]{}, err
		}
	}
	projection, err := prepareCallerScalarProjection(caller, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeCallerRootScalar(ctx, caller, descriptor, mutationir.Delete, nil, &frozenTarget, projection)
}

// CallerUpsert executes exactly one truthful create or update branch. Public
// values and authorization are frozen/planned before the first transaction.
func CallerUpsert[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if caller != nil && caller.app != nil {
		if err := refuseLegacyVersionedMutation(caller.app.registry, descriptor.Metadata().ModelID(), "upsert"); err != nil {
			return golem.Row[M]{}, err
		}
	}
	projection, err := prepareCallerScalarProjection(caller, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenCreate, err := golem.RuntimeFreezeCreateInput(create)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenUpdate, err := golem.RuntimeFreezeUpdateInput(update)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeCallerRootUpsert(ctx, caller, descriptor, frozenTarget, frozenCreate, frozenUpdate, projection)
}

func SystemCreate[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], input golem.CreateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	projection, err := prepareSystemScalarProjection(system, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozen, err := golem.RuntimeFreezeCreateInput(input)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeSystemRootScalar(ctx, system, descriptor, mutationir.Create, &frozen, nil, projection)
}

func SystemUpdate[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if system.app != nil {
		if err := refuseLegacyVersionedMutation(system.app.registry, descriptor.Metadata().ModelID(), "update"); err != nil {
			return golem.Row[M]{}, err
		}
	}
	projection, err := prepareSystemScalarProjection(system, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenInput, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeSystemRootScalar(ctx, system, descriptor, mutationir.Update, &frozenInput, &frozenTarget, projection)
}

func SystemDelete[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if system.app != nil {
		if err := refuseLegacyVersionedMutation(system.app.registry, descriptor.Metadata().ModelID(), "delete"); err != nil {
			return golem.Row[M]{}, err
		}
	}
	projection, err := prepareSystemScalarProjection(system, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeSystemRootScalar(ctx, system, descriptor, mutationir.Delete, nil, &frozenTarget, projection)
}

func SystemUpsert[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if system.app != nil {
		if err := refuseLegacyVersionedMutation(system.app.registry, descriptor.Metadata().ModelID(), "upsert"); err != nil {
			return golem.Row[M]{}, err
		}
	}
	projection, err := prepareSystemScalarProjection(system, descriptor, projections)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(target)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenCreate, err := golem.RuntimeFreezeCreateInput(create)
	if err != nil {
		return golem.Row[M]{}, err
	}
	frozenUpdate, err := golem.RuntimeFreezeUpdateInput(update)
	if err != nil {
		return golem.Row[M]{}, err
	}
	return executeSystemRootUpsert(ctx, system, descriptor, frozenTarget, frozenCreate, frozenUpdate, projection)
}

func CallerTxCreate[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], input golem.CreateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "create", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerCreate(ctx, transaction.caller, descriptor, input, projections...)
}

func CallerTxUpdate[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "update", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerUpdate(ctx, transaction.caller, descriptor, target, input, projections...)
}

func CallerTxDelete[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "delete", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerDelete(ctx, transaction.caller, descriptor, target, projections...)
}

func CallerTxUpsert[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.caller == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller transaction is unavailable", nil)
	}
	return CallerUpsert(ctx, transaction.caller, descriptor, target, create, update, projections...)
}

func SystemTxCreate[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], input golem.CreateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "create", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemCreate(ctx, transaction.system, descriptor, input, projections...)
}

func SystemTxUpdate[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], input golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "update", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemUpdate(ctx, transaction.system, descriptor, target, input, projections...)
}

func SystemTxDelete[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "delete", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemDelete(ctx, transaction.system, descriptor, target, projections...)
}

func SystemTxUpsert[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], target golem.MutationTarget[M], create golem.CreateInput[M], update golem.UpdateInput[M], projections ...golem.Projection[M]) (golem.Row[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return golem.Row[M]{}, golem.RuntimeOperationError(golem.CodeBadUserInput, "upsert", descriptor.Metadata().ModelID(), golem.FieldID{}, "system transaction is unavailable", nil)
	}
	return SystemUpsert(ctx, transaction.system, descriptor, target, create, update, projections...)
}

func executeCallerRootUpsert[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], target golem.FrozenMutationTarget, create, update golem.FrozenMutationInput, projection scalarMutationProjection) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	prepared, err := prepareCallerRootUpsert(caller, rootUpsertPrepareRequest{model: model, target: target, create: create, update: update, result: requirements, deferHookOwned: true})
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	hooks := callerMutationHookExecution[A]{
		bindings: caller.app.bindings,
		actor:    caller.actor,
		executor: func(binding *executionBinding) golem.HookExecutor {
			return newCallerHookExecutor(caller, binding)
		},
	}
	return executePreparedRootUpsert(ctx, caller.app, caller.executor, descriptor, projection, prepared, caller.policies, &hooks)
}

func executeSystemRootUpsert[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], target golem.FrozenMutationTarget, create, update golem.FrozenMutationInput, projection scalarMutationProjection) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	prepared, err := prepareSystemRootUpsert(system, rootUpsertPrepareRequest{model: model, target: target, create: create, update: update, result: requirements})
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(mutationir.Upsert, golem.ModelID(model), err)
	}
	return executePreparedRootUpsert[P, A](ctx, system.app, system.executor, descriptor, projection, prepared, nil, nil)
}

func executeCallerRootScalar[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, projection scalarMutationProjection) (result golem.Row[M], resultErr error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, caller.app, caller.executor, golem.ModelID(model), observe.KindMutation, mutationObservationOperation(operation))
	defer func() { finishDeferredObservation(observation, deferredObservation, resultErr) }()
	runtimeValues := newMutationRuntimeValues()
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	if operation == mutationir.Create && input != nil && len(input.Relations()) == 0 {
		if err := prepareCallerCreatePreHookInput(caller, *input); err != nil {
			return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
		}
	}
	// Nested inputs are model-erased and do not yet carry exact per-row
	// expectations. Compile and reject any existing versioned-row write before
	// application Before code can run. Every hook transform is checked again by
	// validate below.
	if input != nil && len(input.Relations()) != 0 {
		var prepareErr error
		if operation == mutationir.Create {
			_, prepareErr = prepareCallerNestedCreatePreHookGraph(caller, descriptor, input, projection, runtimeValues)
		} else {
			_, prepareErr = prepareCallerNestedGraph(caller, descriptor, operation, input, target, projection, runtimeValues)
		}
		if prepareErr != nil {
			return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), prepareErr)
		}
	}
	hookRequest, err := scalarBeforeHookRequest(operation, golem.ModelID(model), input, target)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	var program mutationsql.Program
	var finalInput *golem.FrozenMutationInput
	var finalTarget *golem.FrozenMutationTarget
	var finalHookAuthored []golem.FieldID
	validate := func(transformed golem.RuntimeMutationHookRequest) error {
		transformedInput, transformedTarget, partsErr := scalarRequestParts(transformed)
		if partsErr != nil {
			return partsErr
		}
		hookAuthored := hookAuthoredSystemFields(caller.app.registry, input, transformedInput)
		if transformedInput != nil && len(transformedInput.Relations()) != 0 {
			if _, prepareErr := prepareCallerNestedGraphFromHook(caller, descriptor, operation, transformedInput, transformedTarget, projection, hookAuthored, runtimeValues); prepareErr != nil {
				return prepareErr
			}
			finalInput, finalTarget, finalHookAuthored = transformedInput, transformedTarget, hookAuthored
			return nil
		}
		prepared, prepareErr := prepareCallerScalarProgram(caller, scalarMutationPrepareRequest{operation: operation, model: model, input: transformedInput, target: transformedTarget, result: requirements, runtimeValues: runtimeValues, hookAuthored: hookAuthored})
		if prepareErr == nil {
			program = prepared
			finalInput, finalTarget, finalHookAuthored = transformedInput, transformedTarget, hookAuthored
		}
		return prepareErr
	}
	hookContext := golem.RuntimeContextWithActor(ctx, caller.actor)
	hookContext, hookObservation := observeexec.BeginChild(hookContext, golem.ModelID(model), observe.KindHook, hookObservationOperation(hookRequest.Operation()), observe.PhaseBefore)
	transformed, err := golem.RuntimeInvokeMutationBeforeHooks(hookContext, caller.app.bindings, hookRequest, validate)
	finishObservation(hookObservation, err)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	if err = validate(transformed); err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	if finalInput != nil && len(finalInput.Relations()) != 0 {
		row, err := executeCallerNestedScalar(ctx, caller, descriptor, operation, finalInput, finalTarget, projection, finalHookAuthored, runtimeValues)
		if err != nil {
			return golem.Row[M]{}, publicNestedMutationExecutionError(operation, golem.ModelID(model), err)
		}
		return row, nil
	}
	hooks := callerMutationHookExecution[A]{
		bindings: caller.app.bindings,
		actor:    caller.actor,
		executor: func(binding *executionBinding) golem.HookExecutor {
			return newCallerHookExecutor(caller, binding)
		},
	}
	return executeRootScalarProjection(ctx, caller.app, caller.executor, descriptor, projection, program, &hooks)
}

func publicNestedMutationExecutionError(operation mutationir.Operation, model golem.ModelID, err error) error {
	var retryConflict *nestedMutationRetryConflict
	if errors.As(err, &retryConflict) {
		return golem.RuntimeOperationError(golem.CodeConflict, scalarMutationOperationName(operation), model, golem.FieldID{}, "mutation conflicted", err)
	}
	var missing *mutationnested.NotFoundError
	if errors.As(err, &missing) {
		return golem.RuntimeOperationError(golem.CodeNotFound, scalarMutationOperationName(operation), model, golem.FieldID(missing.Field), "record not found", err)
	}
	var scalar *scalarMutationFailure
	var hook *mutationHookFailure
	if errors.As(err, &scalar) || errors.As(err, &hook) {
		return publicScalarMutationError(model, err)
	}
	var batch *mutationbatch.Error
	if errors.As(err, &batch) {
		switch batch.Code {
		case mutationbatch.CodeForbidden:
			return golem.RuntimeOperationError(golem.CodeForbidden, scalarMutationOperationName(operation), model, golem.FieldID(batch.Field), "nested batch mutation is not authorized", err)
		case mutationbatch.CodeSet, mutationbatch.CodeIdentity:
			return golem.RuntimeOperationError(golem.CodeConflict, scalarMutationOperationName(operation), model, golem.FieldID(batch.Field), "nested batch mutation conflicted", err)
		case mutationbatch.CodeLimit:
			return golem.RuntimeOperationError(golem.CodeBadUserInput, scalarMutationOperationName(operation), model, golem.FieldID{}, "nested batch mutation exceeds the configured row limit", err)
		}
	}
	// Nested caller batches expand and lock their exact related-row set before
	// execution. An authorization/apply cardinality loss therefore represents a
	// denied member of that already fixed caller set, not invalid public input.
	var cardinality *batchCardinalityError
	if errors.As(err, &cardinality) {
		return golem.RuntimeOperationError(golem.CodeForbidden, scalarMutationOperationName(operation), model, golem.FieldID{}, "nested batch mutation is not authorized", err)
	}
	return publicMutationPreparationError(operation, model, err)
}

func executeSystemRootScalar[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, projection scalarMutationProjection) (result golem.Row[M], resultErr error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, system.app, system.executor, golem.ModelID(model), observe.KindMutation, mutationObservationOperation(operation))
	defer func() { finishDeferredObservation(observation, deferredObservation, resultErr) }()
	if input != nil && len(input.Relations()) != 0 {
		row, err := executeSystemNestedScalar(ctx, system, descriptor, operation, input, target, projection)
		if err != nil {
			return golem.Row[M]{}, publicNestedMutationExecutionError(operation, golem.ModelID(model), err)
		}
		return row, nil
	}
	requirements, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	program, err := prepareSystemScalarProgram(system, scalarMutationPrepareRequest{operation: operation, model: model, input: input, target: target, result: requirements, runtimeValues: newMutationRuntimeValues()})
	if err != nil {
		return golem.Row[M]{}, publicMutationPreparationError(operation, golem.ModelID(model), err)
	}
	return executeRootScalarProjection(ctx, system.app, system.executor, descriptor, projection, program, nil)
}

func executeRootScalarProjection[P, A, M any](ctx context.Context, app *App[P, A], binding *executionBinding, descriptor golem.ModelDescriptor[M], projection scalarMutationProjection, program mutationsql.Program, hooks *callerMutationHookExecution[A]) (golem.Row[M], error) {
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	var verified scalarMutationVerifiedObserver
	if hooks != nil {
		verified = hooks.verifiedObserver(app.registry)
	}
	if !projection.active {
		if _, err := executeScalarMutationProgramWithObservers(ctx, app.database, app.provider, app.registry, model, binding, program, mutationConfig(app, binding), nil, verified); err != nil {
			return golem.Row[M]{}, publicScalarMutationError(golem.ModelID(model), err)
		}
		return golem.RuntimeReadRow(descriptor)
	}

	var projected golem.Row[M]
	materialized := false
	statements := program.Statements()
	identity := program.IdentityVerification()
	imageStatement := identity.AfterStatement()
	observeAt := uint32(len(statements) - 1)
	if program.Operation() == mutationir.Delete {
		var present bool
		imageStatement, present = identity.BeforeStatement()
		if !present {
			return golem.Row[M]{}, publicScalarMutationError(golem.ModelID(model), scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "delete projection has no locked pre-image", nil))
		}
		observeAt = imageStatement
	}
	observer := func(observerContext context.Context, observerBinding *executionBinding, statement uint32, execution scalarMutationExecution) error {
		if statement != observeAt {
			return nil
		}
		source, ok := execution.statement(imageStatement)
		if !ok {
			return scalarMutationError(program.Operation(), scalarMutationInvariant, 0, statement, "mutation result image statement is absent", nil)
		}
		row, err := materializeScalarMutationProjection(observerContext, app, observerBinding, descriptor, projection.planned, source)
		if err != nil {
			return err
		}
		projected, materialized = row, true
		return nil
	}
	if _, err := executeScalarMutationProgramWithObservers(ctx, app.database, app.provider, app.registry, model, binding, program, mutationConfig(app, binding), observer, verified); err != nil {
		return golem.Row[M]{}, publicScalarMutationError(golem.ModelID(model), err)
	}
	if !materialized {
		return golem.Row[M]{}, publicScalarMutationError(golem.ModelID(model), scalarMutationError(program.Operation(), scalarMutationInvariant, 0, 0, "mutation projection was not materialized", nil))
	}
	return projected, nil
}

func materializeScalarMutationProjection[P, A, M any](ctx context.Context, app *App[P, A], binding *executionBinding, descriptor golem.ModelDescriptor[M], planned readplan.Plan, source scalarMutationStatementResult) (golem.Row[M], error) {
	condition, err := scalarMutationIdentityCondition(app, planned.ModelID(), source)
	if err != nil {
		return golem.Row[M]{}, err
	}
	constrained, err := readplan.WithAdditionalWhere(planned, condition)
	if err != nil {
		return golem.Row[M]{}, err
	}
	rows, err := executePlan(ctx, app, binding, golem.ReadFindMany, constrained)
	if err != nil {
		return golem.Row[M]{}, err
	}
	if len(rows) == 0 {
		// The mutation was authorized under its own action reach, but the
		// persisted row is outside caller read reach. Return no disclosed cells
		// without converting a committed/authorized write into an unrestricted
		// read. System plans always reach the exact identity.
		return golem.RuntimeReadRow(descriptor)
	}
	if len(rows) != 1 {
		return golem.Row[M]{}, scalarMutationError(0, scalarMutationInvariant, source.role, 0, "identity-constrained mutation projection returned multiple rows", nil)
	}
	if err := verifyScalarMutationProjectionImage(planned, source, rows[0]); err != nil {
		return golem.Row[M]{}, err
	}
	return golem.RuntimeTypedReadRow(descriptor, rows[0].row)
}

func scalarMutationIdentityCondition[P, A any](app *App[P, A], modelID policyir.ModelID, source scalarMutationStatementResult) (policyir.Condition, error) {
	model, ok := app.registry.Model(golem.ModelID(modelID))
	if !ok || len(model.PrimaryKey()) == 0 {
		return policyir.Condition{}, scalarMutationError(0, scalarMutationInvariant, source.role, 0, "mutation result model has no primary identity", nil)
	}
	resolver := policysql.SchemaResolver(app.registry)
	cells := make(map[policyir.FieldID]readdecode.Cell, len(source.cells))
	for _, cell := range source.cells {
		cells[cell.FieldID()] = cell
	}
	conditions := make([]policyir.Condition, 0, len(model.PrimaryKey()))
	for _, publicField := range model.PrimaryKey() {
		fieldID := policyir.FieldID(publicField)
		cell, present := cells[fieldID]
		if !present || cell.IsNull() {
			return policyir.Condition{}, scalarMutationError(0, scalarMutationInvariant, source.role, 0, "mutation result primary identity is absent or NULL", nil)
		}
		value, present := cell.PolicyValue()
		if !present {
			return policyir.Condition{}, scalarMutationError(0, scalarMutationInvariant, source.role, 0, "mutation result primary identity has no exact value", nil)
		}
		field, present := resolver.Field(app.provider, modelID, fieldID)
		if !present {
			return policyir.Condition{}, scalarMutationError(0, scalarMutationInvariant, source.role, 0, "mutation result primary identity is absent from provider schema", nil)
		}
		operand, operandErr := policyir.OneOperand(value)
		if operandErr != nil {
			return policyir.Condition{}, operandErr
		}
		requirements, shapeErr := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
		if shapeErr != nil {
			return policyir.Condition{}, shapeErr
		}
		condition, conditionErr := policyir.NewScalar(modelID, fieldID, field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if conditionErr != nil {
			return policyir.Condition{}, conditionErr
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return policyir.NewLogical(modelID, policyir.LogicalAnd, conditions)
}

func verifyScalarMutationProjectionImage(planned readplan.Plan, source scalarMutationStatementResult, result executedRow) error {
	sourceCells := make(map[policyir.FieldID]readdecode.Cell, len(source.cells))
	for _, cell := range source.cells {
		sourceCells[cell.FieldID()] = cell
	}
	for _, field := range planned.Fields() {
		before, beforeOK := sourceCells[field.FieldID()]
		after, afterOK := result.values[field.FieldID()]
		if !beforeOK || !afterOK || before.IsNull() != after.IsNull() {
			return scalarMutationError(0, scalarMutationInvariant, source.role, 0, "mutation projection does not match its locked/persisted image", nil)
		}
		if before.IsNull() {
			continue
		}
		beforeValue, beforeOK := before.PolicyValue()
		afterValue, afterOK := after.PolicyValue()
		if !beforeOK || !afterOK || !mutationdecode.EqualValue(beforeValue, afterValue) {
			return scalarMutationError(0, scalarMutationInvariant, source.role, 0, "mutation projection changed after its locked/persisted image", nil)
		}
	}
	return nil
}

func publicMutationPreparationError(operation mutationir.Operation, model golem.ModelID, err error) error {
	var planned *mutationplan.Error
	if errors.As(err, &planned) && planned.Code == mutationplan.CodePolicy {
		return golem.RuntimeOperationError(golem.CodeForbidden, scalarMutationOperationName(operation), model, golem.FieldID(planned.Field), "mutation is not authorized", err)
	}
	var nested *mutationnested.Error
	if errors.As(err, &nested) && nested.Code == mutationnested.CodePolicy {
		return golem.RuntimeOperationError(golem.CodeForbidden, scalarMutationOperationName(operation), model, nested.Field, "mutation is not authorized", err)
	}
	var public *golem.Error
	if errors.As(err, &public) {
		return err
	}
	return golem.RuntimeOperationError(golem.CodeBadUserInput, scalarMutationOperationName(operation), model, golem.FieldID{}, "mutation request is invalid", err)
}
