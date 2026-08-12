package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	mutationupsert "github.com/eleven-am/golem/go/internal/mutation/upsert"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/observe"
)

func newCallerHookExecutor[P, A any](caller *Caller[P, A], binding *executionBinding) golem.HookExecutor {
	return golem.RuntimeHookExecutor(func(ctx context.Context, request golem.RuntimeHookExecutorRequest) (golem.RuntimeHookExecutorResult, error) {
		if caller == nil || caller.app == nil || binding == nil || !binding.scoped {
			return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: active caller transaction is required")
		}
		transactionCaller := *caller
		transactionCaller.executor = binding
		switch request.Operation() {
		case golem.RuntimeHookExecutorFindManyOperation:
			return executeCallerHookFindMany(ctx, &transactionCaller, request)
		case golem.RuntimeHookExecutorCreateOperation, golem.RuntimeHookExecutorUpdateOperation, golem.RuntimeHookExecutorDeleteOperation:
			return executeCallerHookScalar(ctx, &transactionCaller, request)
		case golem.RuntimeHookExecutorUpdateManyOperation, golem.RuntimeHookExecutorDeleteManyOperation:
			return executeCallerHookBatch(ctx, &transactionCaller, request)
		case golem.RuntimeHookExecutorUpsertOperation:
			return executeCallerHookUpsert(ctx, &transactionCaller, request)
		default:
			return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: operation is unknown")
		}
	})
}

type callerHookUpsertBranchExecutor[P, A any] struct {
	caller   *Caller[P, A]
	prepared preparedRuntimeUpsert
	hooks    callerMutationHookExecution[A]
}

func (executor *callerHookUpsertBranchExecutor[P, A]) ExecuteBranch(ctx context.Context, generic mutationupsert.Attempt, node mutationir.Node, _ mutationupsert.FrozenValues) (any, error) {
	attempt, ok := generic.(*sqlxUpsertAttempt)
	if !ok || attempt == nil {
		return nil, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: upsert received a foreign attempt")
	}
	branch := node.Branch()
	if branch != mutationir.UpsertCreateBranch && branch != mutationir.UpsertUpdateBranch {
		return nil, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: upsert selected an unknown branch")
	}
	prepared := executor.prepared
	beforeRequest := golem.RuntimeCreateMutationHookRequest(golem.ModelID(prepared.request.model), prepared.request.create)
	if branch == mutationir.UpsertUpdateBranch {
		beforeRequest = golem.RuntimeUpdateMutationHookRequest(golem.ModelID(prepared.request.model), prepared.request.target, prepared.request.update)
	}
	validate := func(transformed golem.RuntimeMutationHookRequest) error {
		input, target, err := scalarRequestParts(transformed)
		if err != nil {
			return err
		}
		next := prepared.request
		if branch == mutationir.UpsertCreateBranch {
			if input == nil || target != nil {
				return fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: transformed upsert create branch is invalid")
			}
			next.create = *input
			next.deferHookOwned = false
		} else {
			if input == nil || target == nil {
				return fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: transformed upsert update branch is invalid")
			}
			next.update, next.target = *input, *target
		}
		replanned, err := prepareCallerRootUpsert(executor.caller, next)
		if err == nil {
			prepared = replanned
		}
		return err
	}
	transformed, err := golem.RuntimeInvokeMutationBeforeHooks(golem.RuntimeContextWithActor(ctx, executor.caller.actor), executor.caller.app.bindings, beforeRequest, validate)
	if err != nil {
		return nil, err
	}
	if err := validate(transformed); err != nil {
		return nil, err
	}
	selectedInput := &prepared.request.create
	program := prepared.create
	if branch == mutationir.UpsertUpdateBranch {
		selectedInput = &prepared.request.update
		program = prepared.update
	}
	var captured golem.RuntimeMutationHookResult
	hooks := executor.hooks
	hooks.capture = func(result golem.RuntimeMutationHookResult) { captured = result }
	if len(selectedInput.Relations()) != 0 {
		compiled := prepared.createNested
		policyErr := prepared.createNestedPolicy
		if branch == mutationir.UpsertUpdateBranch {
			compiled, policyErr = prepared.updateNested, prepared.updateNestedPolicy
		}
		if policyErr != nil {
			return nil, policyErr
		}
		if compiled == nil {
			return nil, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: selected nested upsert branch was not precompiled")
		}
		graph := compiled.Graph()
		boundary := &systemNestedBoundary[P, A]{
			app: executor.caller.app, source: attempt.binding, graph: graph, compiled: compiled,
			stance: mutationir.Caller, policies: executor.caller.policies, actor: executor.caller.actor,
			hooks: &hooks, captureRoot: true, runtimeValues: prepared.request.runtimeValues, reuseBinding: true,
		}
		if _, err := mutationnested.Execute(ctx, graph, uint32(executor.caller.app.mutationLimits.touchedRows), boundary); err != nil {
			return nil, err
		}
		if boundary.rootResult == nil {
			return nil, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: verified nested upsert result is absent")
		}
		if err := hooks.observeResult(ctx, attempt.binding, *boundary.rootResult); err != nil {
			return nil, err
		}
	} else if _, err := executeScalarProgramOnQueryerObserved(ctx, attempt.queryer, attempt.binding, executor.caller.app.registry, prepared.request.model, executor.caller.app.provider, program, nil, hooks.verifiedObserver(executor.caller.app.registry)); err != nil {
		return nil, err
	}
	row, ok := captured.After()
	if !ok {
		return nil, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: upsert branch after image is absent")
	}
	return row, nil
}

func executeCallerHookUpsert[P, A any](ctx context.Context, caller *Caller[P, A], request golem.RuntimeHookExecutorRequest) (golem.RuntimeHookExecutorResult, error) {
	if err := refuseLegacyVersionedMutation(caller.app.registry, request.ModelID(), "upsert"); err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	target, targetOK := request.Target()
	create, createOK := request.Input()
	update, updateOK := request.UpdateInput()
	if !targetOK || !createOK || !updateOK {
		return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: upsert request is invalid")
	}
	requirements, err := completeHookImageRequirements(caller.app, request.ModelID())
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	runtimeValues := newMutationRuntimeValues()
	prepared, err := prepareCallerRootUpsert(caller, rootUpsertPrepareRequest{
		model: policyir.ModelID(request.ModelID()), target: target,
		create: create, update: update, result: requirements, runtimeValues: runtimeValues, deferHookOwned: true,
	})
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, publicMutationPreparationError(mutationir.Upsert, request.ModelID(), err)
	}
	hooks := callerMutationHookExecution[A]{
		bindings: caller.app.bindings,
		actor:    caller.actor,
		executor: func(binding *executionBinding) golem.HookExecutor { return newCallerHookExecutor(caller, binding) },
	}
	backend := sqlxUpsertBackend{database: caller.app.database, provider: caller.app.provider, binding: caller.executor, mutation: mutationConfig(caller.app, caller.executor)}
	executor := &callerHookUpsertBranchExecutor[P, A]{caller: caller, prepared: prepared, hooks: hooks}
	result, err := mutationupsert.Run(ctx, prepared.kernel, backend, func(context.Context) (mutationupsert.FrozenValues, error) {
		return mutationupsert.NewFrozenValues(nil), nil
	}, executor, mutationupsert.Options{MaxAttempts: uint32(caller.app.mutationLimits.upsertAttempts)})
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, publicRootUpsertError(request.ModelID(), err)
	}
	row, ok := result.Value().(golem.RuntimeModelRow)
	if !ok {
		return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: upsert returned an invalid result")
	}
	return golem.RuntimeHookExecutorRows(row), nil
}

func executeCallerHookFindMany[P, A any](ctx context.Context, caller *Caller[P, A], request golem.RuntimeHookExecutorRequest) (golem.RuntimeHookExecutorResult, error) {
	frozen, ok := request.Read()
	if !ok || frozen.ModelID() != request.ModelID() {
		return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: read request is invalid")
	}
	prepared, err := caller.Prepare(frozen)
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	planned, err := preparePlan(prepared, caller.app.registry, caller.app.readLimits.plan)
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, publicPlanError(prepared, err)
	}
	executed, err := executePlan(ctx, caller.app, caller.executor, golem.ReadFindMany, planned)
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	rows := make([]golem.RuntimeModelRow, len(executed))
	for index := range executed {
		rows[index] = executed[index].row
	}
	return golem.RuntimeHookExecutorRows(rows...), nil
}

func completeHookImageRequirements[P, A any](app *App[P, A], model golem.ModelID) (mutationir.ImageRequirements, error) {
	metadata, ok := app.registry.Model(model)
	if !ok {
		return mutationir.ImageRequirements{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: model is unknown")
	}
	fields := make([]policyir.FieldID, 0, len(metadata.Fields()))
	for _, fieldID := range metadata.Fields() {
		field, ok := app.registry.Field(model, fieldID)
		if ok && field.Kind() != compilerir.FieldRelation {
			fields = append(fields, policyir.FieldID(fieldID))
		}
	}
	return mutationir.NewImageRequirements(policyir.ModelID(model), fields, nil)
}

func executeCallerHookScalar[P, A any](ctx context.Context, caller *Caller[P, A], request golem.RuntimeHookExecutorRequest) (golem.RuntimeHookExecutorResult, error) {
	var operation mutationir.Operation
	switch request.Operation() {
	case golem.RuntimeHookExecutorCreateOperation:
		operation = mutationir.Create
	case golem.RuntimeHookExecutorUpdateOperation:
		operation = mutationir.Update
	case golem.RuntimeHookExecutorDeleteOperation:
		operation = mutationir.Delete
	}
	if operation != mutationir.Create {
		if err := refuseLegacyVersionedMutation(caller.app.registry, request.ModelID(), string(mutationObservationOperation(operation))); err != nil {
			return golem.RuntimeHookExecutorResult{}, err
		}
	}
	input, inputOK := request.Input()
	target, targetOK := request.Target()
	var inputPointer *golem.FrozenMutationInput
	if inputOK {
		inputPointer = &input
	}
	var targetPointer *golem.FrozenMutationTarget
	if targetOK {
		targetPointer = &target
	}
	if operation == mutationir.Create && inputPointer != nil && len(inputPointer.Relations()) == 0 {
		if _, err := mutationbind.CreateInput(*inputPointer, caller.app.registry); err != nil {
			return golem.RuntimeHookExecutorResult{}, err
		}
	}
	requirements, err := completeHookImageRequirements(caller.app, request.ModelID())
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	runtimeValues := newMutationRuntimeValues()
	if inputPointer != nil && len(inputPointer.Relations()) != 0 {
		if _, err := prepareNestedGraph(caller.app, caller.policies, mutationir.Caller, operation, inputPointer, targetPointer, requirements, runtimeValues); err != nil {
			return golem.RuntimeHookExecutorResult{}, err
		}
	}
	beforeRequest, err := scalarBeforeHookRequest(operation, request.ModelID(), inputPointer, targetPointer)
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	var program mutationsql.Program
	var finalInput *golem.FrozenMutationInput
	var finalTarget *golem.FrozenMutationTarget
	validate := func(transformed golem.RuntimeMutationHookRequest) error {
		transformedInput, transformedTarget, err := scalarRequestParts(transformed)
		if err != nil {
			return err
		}
		if transformedInput != nil && len(transformedInput.Relations()) != 0 {
			if _, err := prepareNestedGraph(caller.app, caller.policies, mutationir.Caller, operation, transformedInput, transformedTarget, requirements, runtimeValues); err != nil {
				return err
			}
			finalInput, finalTarget = transformedInput, transformedTarget
			return nil
		}
		prepared, err := prepareCallerScalarProgram(caller, scalarMutationPrepareRequest{
			operation: operation, model: policyir.ModelID(request.ModelID()),
			input: transformedInput, target: transformedTarget,
			result: requirements, forceHookSnapshot: true, runtimeValues: runtimeValues,
		})
		if err == nil {
			program = prepared
			finalInput, finalTarget = transformedInput, transformedTarget
		}
		return err
	}
	transformed, err := golem.RuntimeInvokeMutationBeforeHooks(golem.RuntimeContextWithActor(ctx, caller.actor), caller.app.bindings, beforeRequest, validate)
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	if err := validate(transformed); err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	var captured golem.RuntimeMutationHookResult
	hooks := callerMutationHookExecution[A]{
		bindings: caller.app.bindings,
		actor:    caller.actor,
		executor: func(binding *executionBinding) golem.HookExecutor { return newCallerHookExecutor(caller, binding) },
		capture:  func(result golem.RuntimeMutationHookResult) { captured = result },
	}
	if finalInput != nil && len(finalInput.Relations()) != 0 {
		result, err := executeCallerNestedHookScalar(ctx, caller, operation, request.ModelID(), finalInput, finalTarget, runtimeValues)
		if err != nil {
			return golem.RuntimeHookExecutorResult{}, publicScalarMutationError(request.ModelID(), err)
		}
		if err := hooks.observeResult(ctx, caller.executor, result); err != nil {
			return golem.RuntimeHookExecutorResult{}, publicScalarMutationError(request.ModelID(), err)
		}
	} else {
		if _, err := executeScalarMutationProgramWithObservers(ctx, caller.app.database, caller.app.provider, caller.app.registry, policyir.ModelID(request.ModelID()), caller.executor, program, mutationConfig(caller.app, caller.executor), nil, hooks.verifiedObserver(caller.app.registry)); err != nil {
			return golem.RuntimeHookExecutorResult{}, publicScalarMutationError(request.ModelID(), err)
		}
	}
	row, ok := captured.After()
	if operation == mutationir.Delete {
		row, ok = captured.Before()
	}
	if !ok {
		return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: verified result row is absent")
	}
	return golem.RuntimeHookExecutorRows(row), nil
}

func executeCallerHookBatch[P, A any](ctx context.Context, caller *Caller[P, A], request golem.RuntimeHookExecutorRequest) (result golem.RuntimeHookExecutorResult, resultErr error) {
	operationName := "deleteMany"
	if request.Operation() == golem.RuntimeHookExecutorUpdateManyOperation {
		operationName = "updateMany"
	}
	if err := refuseLegacyVersionedMutation(caller.app.registry, request.ModelID(), operationName); err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	predicate, ok := request.Predicate()
	if !ok {
		return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: batch predicate is absent")
	}
	operation := mutationir.DeleteMany
	var input *golem.FrozenMutationInput
	if request.Operation() == golem.RuntimeHookExecutorUpdateManyOperation {
		operation = mutationir.UpdateMany
		value, ok := request.Input()
		if !ok {
			return golem.RuntimeHookExecutorResult{}, fmt.Errorf("P4_RUNTIME_HOOK_EXECUTOR: batch input is absent")
		}
		input = &value
	}
	ctx, observation, deferredObservation := beginDeferredExecutionObservation(ctx, caller.app, caller.executor, request.ModelID(), observe.KindMutation, mutationObservationOperation(operation))
	defer func() {
		if observation != nil {
			observation.SetAggregateCount(result.Count())
		}
		finishDeferredObservation(observation, deferredObservation, resultErr)
	}()
	beforeRequest := golem.RuntimeDeleteManyMutationHookRequest(request.ModelID(), predicate)
	if operation == mutationir.UpdateMany {
		beforeRequest = golem.RuntimeUpdateManyMutationHookRequest(request.ModelID(), predicate, *input)
	}
	program, err := prepareCallerFrozenBatchHooks(ctx, caller, request.ModelID(), beforeRequest)
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, publicBatchPreparationError(operation, request.ModelID(), err)
	}
	hooks := callerMutationHookExecution[A]{
		bindings: caller.app.bindings,
		actor:    caller.actor,
		executor: func(binding *executionBinding) golem.HookExecutor { return newCallerHookExecutor(caller, binding) },
	}
	count, err := executePublicBatch(ctx, caller.app, caller.executor, program, &hooks)
	if err != nil {
		return golem.RuntimeHookExecutorResult{}, err
	}
	return golem.RuntimeHookExecutorCount(count), nil
}
