package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/observe"
)

type mutationHookFailure struct {
	operation golem.HookOperation
	phase     golem.HookPhase
	cause     error
}

// UntrustedRetryCause prevents provider-shaped errors returned by user hook
// code from being mistaken for transaction interference. Only failures from
// trusted execution stages may drive an engine retry.
func (*mutationHookFailure) UntrustedRetryCause() {}

func (failure *mutationHookFailure) Error() string {
	return fmt.Sprintf("P4_RUNTIME_HOOK: %s hook rejected %s mutation: %v", failure.phase, failure.operation, failure.cause)
}

func (failure *mutationHookFailure) Unwrap() error { return failure.cause }

func mutationHookInventory[A any](bindings golem.ApplicationBindings[A], model golem.ModelID) mutationplan.HookInventory {
	var result mutationplan.HookInventory
	for _, hook := range bindings.RuntimeHookInventory() {
		if hook.Model != model {
			continue
		}
		phase := mutationir.HookPhase(0)
		switch hook.Phase {
		case golem.HookBefore:
			phase = mutationir.BeforeHook
		case golem.HookAfter:
			phase = mutationir.TransactionAfterHook
		case golem.HookAfterCommit:
			phase = mutationir.AfterCommitHook
		}
		if phase == 0 {
			continue
		}
		switch hook.Operation {
		case golem.HookCreate:
			result.Create = append(result.Create, phase)
		case golem.HookUpdate:
			result.Update = append(result.Update, phase)
		case golem.HookDelete:
			result.Delete = append(result.Delete, phase)
		case golem.HookUpdateMany:
			result.UpdateMany = append(result.UpdateMany, phase)
		case golem.HookDeleteMany:
			result.DeleteMany = append(result.DeleteMany, phase)
		}
	}
	return result
}

func hasBeforeCreateHook[A any](bindings golem.ApplicationBindings[A], model golem.ModelID) bool {
	for _, hook := range bindings.RuntimeHookInventory() {
		if hook.Model == model && hook.Operation == golem.HookCreate && hook.Phase == golem.HookBefore {
			return true
		}
	}
	return false
}

func mutationHookOperation(operation mutationir.Operation) (golem.HookOperation, bool) {
	switch operation {
	case mutationir.Create:
		return golem.HookCreate, true
	case mutationir.Update:
		return golem.HookUpdate, true
	case mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
		return golem.HookUpdate, true
	case mutationir.Delete:
		return golem.HookDelete, true
	case mutationir.UpdateMany:
		return golem.HookUpdateMany, true
	case mutationir.DeleteMany:
		return golem.HookDeleteMany, true
	default:
		return "", false
	}
}

func scalarBeforeHookRequest(operation mutationir.Operation, model golem.ModelID, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget) (golem.RuntimeMutationHookRequest, error) {
	switch operation {
	case mutationir.Create:
		if input == nil || target != nil {
			break
		}
		return golem.RuntimeCreateMutationHookRequest(model, *input), nil
	case mutationir.Update:
		if input == nil || target == nil {
			break
		}
		return golem.RuntimeUpdateMutationHookRequest(model, *target, *input), nil
	case mutationir.Delete:
		if input != nil || target == nil {
			break
		}
		return golem.RuntimeDeleteMutationHookRequest(model, *target), nil
	}
	return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_HOOK: scalar before-hook request shape is invalid")
}

func scalarRequestParts(request golem.RuntimeMutationHookRequest) (*golem.FrozenMutationInput, *golem.FrozenMutationTarget, error) {
	var input *golem.FrozenMutationInput
	if value, ok := request.Input(); ok {
		input = &value
	}
	var target *golem.FrozenMutationTarget
	if value, ok := request.Target(); ok {
		target = &value
	}
	switch request.Operation() {
	case golem.HookCreate:
		if input == nil || target != nil {
			return nil, nil, fmt.Errorf("P4_RUNTIME_HOOK: transformed create shape is invalid")
		}
	case golem.HookUpdate:
		if input == nil || target == nil {
			return nil, nil, fmt.Errorf("P4_RUNTIME_HOOK: transformed update shape is invalid")
		}
	case golem.HookDelete:
		if input != nil || target == nil {
			return nil, nil, fmt.Errorf("P4_RUNTIME_HOOK: transformed delete shape is invalid")
		}
	default:
		return nil, nil, fmt.Errorf("P4_RUNTIME_HOOK: transformed request is not scalar")
	}
	return input, target, nil
}

func scalarHookResult(registry *schema.Registry, program mutationsql.Program, execution scalarMutationExecution) (golem.RuntimeMutationHookResult, error) {
	model, operation := program.ModelID(), program.Operation()
	toRow := func(result scalarMutationStatementResult) (golem.RuntimeModelRow, error) {
		exact, err := mutationdecode.FromReadCells(registry, model, result.cells)
		if err != nil {
			return golem.RuntimeModelRow{}, err
		}
		complete, err := exact.IsComplete(registry)
		if err != nil || !complete {
			return golem.RuntimeModelRow{}, fmt.Errorf("P4_RUNTIME_HOOK: hook snapshot is not a complete scalar row")
		}
		cells := make([]golem.RuntimeReadCell, len(result.cells))
		for index, cell := range result.cells {
			cells[index] = cell.RuntimeCell()
		}
		return golem.RuntimeModelReadRow(golem.ModelID(model), cells...)
	}
	var before, after golem.RuntimeModelRow
	verification := program.IdentityVerification()
	afterIndex := verification.AfterStatement()
	get := func(index uint32) (scalarMutationStatementResult, error) {
		value, ok := execution.statement(index)
		if !ok {
			return scalarMutationStatementResult{}, fmt.Errorf("P4_RUNTIME_HOOK: designated snapshot statement %d is absent", index)
		}
		return value, nil
	}
	switch operation {
	case mutationir.Create:
		result, err := get(afterIndex)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		after, err = toRow(result)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		return golem.RuntimeCreateMutationHookResult(golem.ModelID(model), after), nil
	case mutationir.Update:
		beforeIndex, ok := verification.BeforeStatement()
		if !ok {
			return golem.RuntimeMutationHookResult{}, fmt.Errorf("P4_RUNTIME_HOOK: update before image is absent")
		}
		beforeResult, err := get(beforeIndex)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		afterResult, err := get(afterIndex)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		before, err = toRow(beforeResult)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		after, err = toRow(afterResult)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		return golem.RuntimeUpdateMutationHookResult(golem.ModelID(model), before, after), nil
	case mutationir.Delete:
		beforeIndex, ok := verification.BeforeStatement()
		if !ok {
			return golem.RuntimeMutationHookResult{}, fmt.Errorf("P4_RUNTIME_HOOK: delete before image is absent")
		}
		beforeResult, err := get(beforeIndex)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		before, err = toRow(beforeResult)
		if err != nil {
			return golem.RuntimeMutationHookResult{}, err
		}
		return golem.RuntimeDeleteMutationHookResult(golem.ModelID(model), before), nil
	default:
		return golem.RuntimeMutationHookResult{}, fmt.Errorf("P4_RUNTIME_HOOK: operation is not scalar")
	}
}

func invokeMutationResultHooks[A any](ctx context.Context, bindings golem.ApplicationBindings[A], actor A, result golem.RuntimeMutationHookResult, phase golem.HookPhase) (resultErr error) {
	ctx, observation := observeexec.BeginChild(ctx, result.ModelID(), observe.KindHook, hookObservationOperation(result.Operation()), hookObservationPhase(phase))
	defer func() { finishObservation(observation, resultErr) }()
	hookContext := golem.RuntimeContextWithActor(ctx, actor)
	if err := golem.RuntimeInvokeMutationResultHooks(hookContext, bindings, result, phase); err != nil {
		return &mutationHookFailure{operation: result.Operation(), phase: phase, cause: err}
	}
	return nil
}

func hasMutationHook[A any](bindings golem.ApplicationBindings[A], model golem.ModelID, operation golem.HookOperation, phase golem.HookPhase) bool {
	for _, hook := range bindings.RuntimeHookInventory() {
		if hook.Model == model && hook.Operation == operation && hook.Phase == phase {
			return true
		}
	}
	return false
}

type callerMutationHookExecution[A any] struct {
	bindings golem.ApplicationBindings[A]
	actor    A
	executor func(*executionBinding) golem.HookExecutor
	capture  func(golem.RuntimeMutationHookResult)
}

func (hooks callerMutationHookExecution[A]) verifiedObserver(registry *schema.Registry) scalarMutationVerifiedObserver {
	return func(ctx context.Context, binding *executionBinding, program mutationsql.Program, execution scalarMutationExecution) error {
		operation, ok := mutationHookOperation(program.Operation())
		if !ok {
			return fmt.Errorf("P4_RUNTIME_HOOK: verified operation has no hook family")
		}
		model := golem.ModelID(program.ModelID())
		if hooks.capture == nil && !hasMutationHook(hooks.bindings, model, operation, golem.HookAfter) && !hasMutationHook(hooks.bindings, model, operation, golem.HookAfterCommit) {
			return nil
		}
		result, err := scalarHookResult(registry, program, execution)
		if err != nil {
			return err
		}
		return hooks.observeResult(ctx, binding, result)
	}
}

func (hooks callerMutationHookExecution[A]) observeResult(ctx context.Context, binding *executionBinding, result golem.RuntimeMutationHookResult) error {
	operation := result.Operation()
	model := result.ModelID()
	if hooks.executor != nil {
		result = golem.RuntimeMutationHookResultWithExecutor(result, hooks.executor(binding))
	}
	if hooks.capture != nil {
		hooks.capture(result)
	}
	if hasMutationHook(hooks.bindings, model, operation, golem.HookAfter) {
		if err := invokeMutationResultHooks(ctx, hooks.bindings, hooks.actor, result, golem.HookAfter); err != nil {
			return err
		}
	}
	if hasMutationHook(hooks.bindings, model, operation, golem.HookAfterCommit) {
		state, err := binding.mutationState()
		if err != nil {
			return err
		}
		afterCommitResult := golem.RuntimeMutationHookResultWithoutExecutor(result)
		if err := state.addAfterCommit(operation, model, func(commitContext context.Context) error {
			return invokeMutationResultHooks(commitContext, hooks.bindings, hooks.actor, afterCommitResult, golem.HookAfterCommit)
		}); err != nil {
			return err
		}
	}
	return nil
}
