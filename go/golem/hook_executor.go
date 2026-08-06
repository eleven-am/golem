package golem

import (
	"context"
	"fmt"
)

// HookExecutor is an opaque capability for Golem operations on the exact
// transaction that is running a transaction-after mutation hook. It contains
// no database or SQL handle and is absent from after-commit results.
type HookExecutor struct {
	execute func(context.Context, RuntimeHookExecutorRequest) (RuntimeHookExecutorResult, error)
}

// RuntimeHookExecutor constructs the runtime-owned capability. Generated and
// application code consume it only through the typed Hook*Row helpers below.
func RuntimeHookExecutor(execute func(context.Context, RuntimeHookExecutorRequest) (RuntimeHookExecutorResult, error)) HookExecutor {
	return HookExecutor{execute: execute}
}

type RuntimeHookExecutorOperation uint8

const (
	RuntimeHookExecutorFindManyOperation RuntimeHookExecutorOperation = iota + 1
	RuntimeHookExecutorCreateOperation
	RuntimeHookExecutorUpdateOperation
	RuntimeHookExecutorDeleteOperation
	RuntimeHookExecutorUpsertOperation
	RuntimeHookExecutorUpdateManyOperation
	RuntimeHookExecutorDeleteManyOperation
)

// RuntimeHookExecutorRequest is the immutable model-erased runtime ABI.
type RuntimeHookExecutorRequest struct {
	operation RuntimeHookExecutorOperation
	model     ModelID
	read      *FrozenReadRequest
	input     *FrozenMutationInput
	update    *FrozenMutationInput
	target    *FrozenMutationTarget
	predicate *FrozenPredicate
}

func RuntimeHookExecutorFindMany(request FrozenReadRequest) RuntimeHookExecutorRequest {
	copy := request
	return RuntimeHookExecutorRequest{operation: RuntimeHookExecutorFindManyOperation, model: request.ModelID(), read: &copy}
}
func RuntimeHookExecutorCreate(model ModelID, input FrozenMutationInput) RuntimeHookExecutorRequest {
	copy := cloneFrozenMutationInput(input)
	return RuntimeHookExecutorRequest{operation: RuntimeHookExecutorCreateOperation, model: model, input: &copy}
}
func RuntimeHookExecutorUpdate(model ModelID, target FrozenMutationTarget, input FrozenMutationInput) RuntimeHookExecutorRequest {
	targetCopy, inputCopy := cloneFrozenMutationTarget(target), cloneFrozenMutationInput(input)
	return RuntimeHookExecutorRequest{operation: RuntimeHookExecutorUpdateOperation, model: model, input: &inputCopy, target: &targetCopy}
}
func RuntimeHookExecutorDelete(model ModelID, target FrozenMutationTarget) RuntimeHookExecutorRequest {
	copy := cloneFrozenMutationTarget(target)
	return RuntimeHookExecutorRequest{operation: RuntimeHookExecutorDeleteOperation, model: model, target: &copy}
}
func RuntimeHookExecutorUpsert(model ModelID, target FrozenMutationTarget, create, update FrozenMutationInput) RuntimeHookExecutorRequest {
	targetCopy, createCopy, updateCopy := cloneFrozenMutationTarget(target), cloneFrozenMutationInput(create), cloneFrozenMutationInput(update)
	return RuntimeHookExecutorRequest{operation: RuntimeHookExecutorUpsertOperation, model: model, target: &targetCopy, input: &createCopy, update: &updateCopy}
}
func RuntimeHookExecutorUpdateMany(model ModelID, predicate FrozenPredicate, input FrozenMutationInput) RuntimeHookExecutorRequest {
	predicateCopy, inputCopy := cloneFrozenPredicate(predicate), cloneFrozenMutationInput(input)
	return RuntimeHookExecutorRequest{operation: RuntimeHookExecutorUpdateManyOperation, model: model, predicate: &predicateCopy, input: &inputCopy}
}
func RuntimeHookExecutorDeleteMany(model ModelID, predicate FrozenPredicate) RuntimeHookExecutorRequest {
	copy := cloneFrozenPredicate(predicate)
	return RuntimeHookExecutorRequest{operation: RuntimeHookExecutorDeleteManyOperation, model: model, predicate: &copy}
}

func (request RuntimeHookExecutorRequest) Operation() RuntimeHookExecutorOperation {
	return request.operation
}
func (request RuntimeHookExecutorRequest) ModelID() ModelID { return request.model }
func (request RuntimeHookExecutorRequest) Read() (FrozenReadRequest, bool) {
	if request.read == nil {
		return FrozenReadRequest{}, false
	}
	return *request.read, true
}
func (request RuntimeHookExecutorRequest) Input() (FrozenMutationInput, bool) {
	if request.input == nil {
		return FrozenMutationInput{}, false
	}
	return cloneFrozenMutationInput(*request.input), true
}
func (request RuntimeHookExecutorRequest) UpdateInput() (FrozenMutationInput, bool) {
	if request.update == nil {
		return FrozenMutationInput{}, false
	}
	return cloneFrozenMutationInput(*request.update), true
}
func (request RuntimeHookExecutorRequest) Target() (FrozenMutationTarget, bool) {
	if request.target == nil {
		return FrozenMutationTarget{}, false
	}
	return cloneFrozenMutationTarget(*request.target), true
}
func (request RuntimeHookExecutorRequest) Predicate() (FrozenPredicate, bool) {
	if request.predicate == nil {
		return FrozenPredicate{}, false
	}
	return cloneFrozenPredicate(*request.predicate), true
}

type RuntimeHookExecutorResult struct {
	rows  []RuntimeModelRow
	count int64
}

func RuntimeHookExecutorRows(rows ...RuntimeModelRow) RuntimeHookExecutorResult {
	return RuntimeHookExecutorResult{rows: cloneRuntimeModelRows(rows)}
}
func RuntimeHookExecutorCount(count int64) RuntimeHookExecutorResult {
	return RuntimeHookExecutorResult{count: count}
}
func (result RuntimeHookExecutorResult) Rows() []RuntimeModelRow {
	return cloneRuntimeModelRows(result.rows)
}
func (result RuntimeHookExecutorResult) Count() int64 { return result.count }

func runHookExecutor(ctx context.Context, executor HookExecutor, request RuntimeHookExecutorRequest) (RuntimeHookExecutorResult, error) {
	if ctx == nil || executor.execute == nil {
		return RuntimeHookExecutorResult{}, fmt.Errorf("hook executor is unavailable outside a transaction-after hook")
	}
	return executor.execute(ctx, request)
}

func HookFindManyRows[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], options ...ReadOption[M]) ([]Row[M], error) {
	frozen, err := FreezeFindMany(descriptor, options...)
	if err != nil {
		return nil, err
	}
	result, err := runHookExecutor(ctx, executor, RuntimeHookExecutorFindMany(frozen))
	if err != nil {
		return nil, err
	}
	runtimeRows := result.Rows()
	rows := make([]Row[M], len(runtimeRows))
	for index := range runtimeRows {
		rows[index], err = RuntimeTypedReadRow(descriptor, runtimeRows[index])
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func HookCreateRow[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], input CreateInput[M]) (Row[M], error) {
	frozen, err := RuntimeFreezeCreateInput(input)
	if err != nil {
		return Row[M]{}, err
	}
	return runHookMutationRow(ctx, executor, descriptor, RuntimeHookExecutorCreate(descriptor.Metadata().ModelID(), frozen))
}
func HookUpdateRow[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], target MutationTarget[M], input UpdateInput[M]) (Row[M], error) {
	frozenTarget, err := RuntimeFreezeMutationTarget(target)
	if err != nil {
		return Row[M]{}, err
	}
	frozenInput, err := RuntimeFreezeUpdateInput(input)
	if err != nil {
		return Row[M]{}, err
	}
	return runHookMutationRow(ctx, executor, descriptor, RuntimeHookExecutorUpdate(descriptor.Metadata().ModelID(), frozenTarget, frozenInput))
}
func HookDeleteRow[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], target MutationTarget[M]) (Row[M], error) {
	frozen, err := RuntimeFreezeMutationTarget(target)
	if err != nil {
		return Row[M]{}, err
	}
	return runHookMutationRow(ctx, executor, descriptor, RuntimeHookExecutorDelete(descriptor.Metadata().ModelID(), frozen))
}
func HookUpsertRow[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], target MutationTarget[M], create CreateInput[M], update UpdateInput[M]) (Row[M], error) {
	frozenTarget, err := RuntimeFreezeMutationTarget(target)
	if err != nil {
		return Row[M]{}, err
	}
	frozenCreate, err := RuntimeFreezeCreateInput(create)
	if err != nil {
		return Row[M]{}, err
	}
	frozenUpdate, err := RuntimeFreezeUpdateInput(update)
	if err != nil {
		return Row[M]{}, err
	}
	return runHookMutationRow(ctx, executor, descriptor, RuntimeHookExecutorUpsert(descriptor.Metadata().ModelID(), frozenTarget, frozenCreate, frozenUpdate))
}
func HookUpdateManyRows[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], where Predicate[M], input UpdateManyInput[M]) (int64, error) {
	predicate, err := where.Freeze(descriptor)
	if err != nil {
		return 0, err
	}
	frozen, err := RuntimeFreezeUpdateManyInput(input)
	if err != nil {
		return 0, err
	}
	result, err := runHookExecutor(ctx, executor, RuntimeHookExecutorUpdateMany(descriptor.Metadata().ModelID(), predicate, frozen))
	return result.Count(), err
}
func HookDeleteManyRows[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], where Predicate[M]) (int64, error) {
	predicate, err := where.Freeze(descriptor)
	if err != nil {
		return 0, err
	}
	result, err := runHookExecutor(ctx, executor, RuntimeHookExecutorDeleteMany(descriptor.Metadata().ModelID(), predicate))
	return result.Count(), err
}

func runHookMutationRow[M any](ctx context.Context, executor HookExecutor, descriptor ModelDescriptor[M], request RuntimeHookExecutorRequest) (Row[M], error) {
	result, err := runHookExecutor(ctx, executor, request)
	if err != nil {
		return Row[M]{}, err
	}
	rows := result.Rows()
	if len(rows) != 1 {
		return Row[M]{}, fmt.Errorf("hook mutation returned %d rows", len(rows))
	}
	return RuntimeTypedReadRow(descriptor, rows[0])
}
