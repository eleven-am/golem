package golem

import (
	"context"
	"fmt"
)

// RuntimeMutationHookRequest is the immutable model-erased handoff used by
// the mutation engine. Generated hook bindings recover their concrete model
// type without reflection and return a freshly frozen transformed request.
type RuntimeMutationHookRequest struct {
	model     ModelID
	operation HookOperation
	input     *FrozenMutationInput
	target    *FrozenMutationTarget
	predicate *FrozenPredicate
}

func RuntimeCreateMutationHookRequest(model ModelID, input FrozenMutationInput) RuntimeMutationHookRequest {
	value := cloneFrozenMutationInput(input)
	return RuntimeMutationHookRequest{model: model, operation: HookCreate, input: &value}
}

func RuntimeUpdateMutationHookRequest(model ModelID, target FrozenMutationTarget, input FrozenMutationInput) RuntimeMutationHookRequest {
	targetCopy, inputCopy := cloneFrozenMutationTarget(target), cloneFrozenMutationInput(input)
	return RuntimeMutationHookRequest{model: model, operation: HookUpdate, input: &inputCopy, target: &targetCopy}
}

func RuntimeDeleteMutationHookRequest(model ModelID, target FrozenMutationTarget) RuntimeMutationHookRequest {
	value := cloneFrozenMutationTarget(target)
	return RuntimeMutationHookRequest{model: model, operation: HookDelete, target: &value}
}

func RuntimeUpdateManyMutationHookRequest(model ModelID, predicate FrozenPredicate, input FrozenMutationInput) RuntimeMutationHookRequest {
	whereCopy, inputCopy := cloneFrozenPredicate(predicate), cloneFrozenMutationInput(input)
	return RuntimeMutationHookRequest{model: model, operation: HookUpdateMany, input: &inputCopy, predicate: &whereCopy}
}

func RuntimeDeleteManyMutationHookRequest(model ModelID, predicate FrozenPredicate) RuntimeMutationHookRequest {
	value := cloneFrozenPredicate(predicate)
	return RuntimeMutationHookRequest{model: model, operation: HookDeleteMany, predicate: &value}
}

func (request RuntimeMutationHookRequest) ModelID() ModelID         { return request.model }
func (request RuntimeMutationHookRequest) Operation() HookOperation { return request.operation }
func (request RuntimeMutationHookRequest) Input() (FrozenMutationInput, bool) {
	if request.input == nil {
		return FrozenMutationInput{}, false
	}
	return cloneFrozenMutationInput(*request.input), true
}
func (request RuntimeMutationHookRequest) Target() (FrozenMutationTarget, bool) {
	if request.target == nil {
		return FrozenMutationTarget{}, false
	}
	return cloneFrozenMutationTarget(*request.target), true
}
func (request RuntimeMutationHookRequest) Predicate() (FrozenPredicate, bool) {
	if request.predicate == nil {
		return FrozenPredicate{}, false
	}
	return cloneFrozenPredicate(*request.predicate), true
}

// RuntimeMutationHookResult carries detached verified images only. RuntimeModelRow
// keeps the payload model-erased until the generated binding restores Row[M].
type RuntimeMutationHookResult struct {
	model     ModelID
	operation HookOperation
	before    *RuntimeModelRow
	after     *RuntimeModelRow
	count     int64
	executor  HookExecutor
}

func RuntimeCreateMutationHookResult(model ModelID, after RuntimeModelRow) RuntimeMutationHookResult {
	value := cloneRuntimeModelRow(after)
	return RuntimeMutationHookResult{model: model, operation: HookCreate, after: &value}
}
func RuntimeUpdateMutationHookResult(model ModelID, before, after RuntimeModelRow) RuntimeMutationHookResult {
	beforeCopy, afterCopy := cloneRuntimeModelRow(before), cloneRuntimeModelRow(after)
	return RuntimeMutationHookResult{model: model, operation: HookUpdate, before: &beforeCopy, after: &afterCopy}
}
func RuntimeDeleteMutationHookResult(model ModelID, before RuntimeModelRow) RuntimeMutationHookResult {
	value := cloneRuntimeModelRow(before)
	return RuntimeMutationHookResult{model: model, operation: HookDelete, before: &value}
}
func RuntimeUpdateManyMutationHookResult(model ModelID, count int64) RuntimeMutationHookResult {
	return RuntimeMutationHookResult{model: model, operation: HookUpdateMany, count: count}
}
func RuntimeDeleteManyMutationHookResult(model ModelID, count int64) RuntimeMutationHookResult {
	return RuntimeMutationHookResult{model: model, operation: HookDeleteMany, count: count}
}
func (result RuntimeMutationHookResult) ModelID() ModelID         { return result.model }
func (result RuntimeMutationHookResult) Operation() HookOperation { return result.operation }
func (result RuntimeMutationHookResult) Count() int64             { return result.count }
func (result RuntimeMutationHookResult) Before() (RuntimeModelRow, bool) {
	if result.before == nil {
		return RuntimeModelRow{}, false
	}
	return cloneRuntimeModelRow(*result.before), true
}
func (result RuntimeMutationHookResult) After() (RuntimeModelRow, bool) {
	if result.after == nil {
		return RuntimeModelRow{}, false
	}
	return cloneRuntimeModelRow(*result.after), true
}
func RuntimeMutationHookResultWithExecutor(result RuntimeMutationHookResult, executor HookExecutor) RuntimeMutationHookResult {
	result.executor = executor
	return result
}
func RuntimeMutationHookResultWithoutExecutor(result RuntimeMutationHookResult) RuntimeMutationHookResult {
	result.executor = HookExecutor{}
	return result
}

type mutationBeforeBridge func(context.Context, RuntimeMutationHookRequest) (RuntimeMutationHookRequest, error)
type mutationResultBridge func(context.Context, RuntimeMutationHookResult) error

func generatedMutationBeforeBridge[M, Request any](operation HookOperation, invoke func(context.Context, *Request) error) mutationBeforeBridge {
	return func(ctx context.Context, erased RuntimeMutationHookRequest) (RuntimeMutationHookRequest, error) {
		if erased.model == (ModelID{}) || erased.operation != operation || invoke == nil {
			return RuntimeMutationHookRequest{}, fmt.Errorf("generated mutation hook: invalid before-hook envelope")
		}
		switch operation {
		case HookCreate:
			input, ok := erased.Input()
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			typed := RuntimeCreateHookRequest(thawCreateInput[M](input))
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			frozen, err := RuntimeFreezeCreateInput(typed.Input())
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			return RuntimeCreateMutationHookRequest(erased.model, frozen), nil
		case HookUpdate:
			input, inputOK := erased.Input()
			target, targetOK := erased.Target()
			if !inputOK || !targetOK {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			typed := RuntimeUpdateHookRequest[M](runtimeFrozenMutationTarget[M]{target: target}, thawUpdateInput[M](input))
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			frozenInput, err := RuntimeFreezeUpdateInput(typed.Input())
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			frozenTarget, err := RuntimeFreezeMutationTarget(typed.Target())
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			return RuntimeUpdateMutationHookRequest(erased.model, frozenTarget, frozenInput), nil
		case HookDelete:
			target, ok := erased.Target()
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			typed := RuntimeDeleteHookRequest[M](runtimeFrozenMutationTarget[M]{target: target})
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			frozenTarget, err := RuntimeFreezeMutationTarget(typed.Target())
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			return RuntimeDeleteMutationHookRequest(erased.model, frozenTarget), nil
		case HookUpdateMany:
			input, inputOK := erased.Input()
			predicate, predicateOK := erased.Predicate()
			if !inputOK || !predicateOK {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			typed := RuntimeUpdateManyHookRequest(thawPredicate[M](predicate), thawUpdateManyInput[M](input))
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			frozenInput, err := RuntimeFreezeUpdateManyInput(typed.Input())
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			frozenPredicate, err := typed.Where().freezeForModel(erased.model)
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			return RuntimeUpdateManyMutationHookRequest(erased.model, frozenPredicate, frozenInput), nil
		case HookDeleteMany:
			predicate, ok := erased.Predicate()
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			typed := RuntimeDeleteManyHookRequest(thawPredicate[M](predicate))
			request, ok := any(typed).(*Request)
			if !ok {
				return RuntimeMutationHookRequest{}, errGeneratedBindingType
			}
			if err := invoke(ctx, request); err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			frozenPredicate, err := typed.Where().freezeForModel(erased.model)
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			return RuntimeDeleteManyMutationHookRequest(erased.model, frozenPredicate), nil
		default:
			return RuntimeMutationHookRequest{}, errGeneratedBindingType
		}
	}
}

func generatedMutationResultBridge[M, Result any](operation HookOperation, invoke func(context.Context, Result) error) mutationResultBridge {
	return func(ctx context.Context, erased RuntimeMutationHookResult) error {
		if erased.model == (ModelID{}) || erased.operation != operation || invoke == nil {
			return fmt.Errorf("generated mutation hook: invalid result envelope")
		}
		var payload any
		switch operation {
		case HookCreate:
			if erased.after == nil {
				return errGeneratedBindingType
			}
			row, err := runtimeTypedHookRow[M](erased.model, *erased.after)
			if err != nil {
				return err
			}
			payload = runtimeCreateHookResultWithExecutor(row, erased.executor)
		case HookUpdate:
			if erased.before == nil || erased.after == nil {
				return errGeneratedBindingType
			}
			before, err := runtimeTypedHookRow[M](erased.model, *erased.before)
			if err != nil {
				return err
			}
			after, err := runtimeTypedHookRow[M](erased.model, *erased.after)
			if err != nil {
				return err
			}
			payload = runtimeUpdateHookResultWithExecutor(before, after, erased.executor)
		case HookDelete:
			if erased.before == nil {
				return errGeneratedBindingType
			}
			before, err := runtimeTypedHookRow[M](erased.model, *erased.before)
			if err != nil {
				return err
			}
			payload = runtimeDeleteHookResultWithExecutor(before, erased.executor)
		case HookUpdateMany:
			payload = runtimeUpdateManyHookResultWithExecutor[M](erased.count, erased.executor)
		case HookDeleteMany:
			payload = runtimeDeleteManyHookResultWithExecutor[M](erased.count, erased.executor)
		default:
			return errGeneratedBindingType
		}
		result, ok := payload.(Result)
		if !ok {
			return errGeneratedBindingType
		}
		return invoke(ctx, result)
	}
}

func runtimeTypedHookRow[M any](model ModelID, runtime RuntimeModelRow) (Row[M], error) {
	if model == (ModelID{}) || runtime.model != model {
		return Row[M]{}, fmt.Errorf("generated mutation hook: result model mismatch")
	}
	return Row[M]{model: model, cells: cloneReadCells(runtime.cells), counts: cloneReadCounts(runtime.counts), occurrences: cloneOccurrences(runtime.occurrences)}, nil
}

func thawCreateInput[M any](input FrozenMutationInput) CreateInput[M] {
	return CreateInput[M]{model: input.model, values: thawMutationNodes(input)}
}
func thawUpdateInput[M any](input FrozenMutationInput) UpdateInput[M] {
	return UpdateInput[M]{model: input.model, values: thawMutationNodes(input)}
}
func thawUpdateManyInput[M any](input FrozenMutationInput) UpdateManyInput[M] {
	return UpdateManyInput[M]{model: input.model, values: thawMutationNodes(input)}
}

func thawMutationNodes(input FrozenMutationInput) []mutationValueNode {
	result := make([]mutationValueNode, 0, len(input.fields)+len(input.relations))
	for _, field := range input.Fields() {
		value, present := field.Value()
		result = append(result, mutationValueNode{model: input.model, field: field.field, operation: field.operation, value: value, hasValue: present})
	}
	for _, relation := range input.Relations() {
		node := thawNestedMutation(relation)
		result = append(result, mutationValueNode{model: input.model, field: relation.field, relation: &node})
	}
	return result
}

func thawNestedMutation(value FrozenNestedMutation) nestedMutationNode {
	result := nestedMutationNode{parent: value.parent, field: value.field, relation: value.relation, target: value.target, action: value.action, branches: make([]nestedMutationBranchNode, len(value.branches))}
	for index, branch := range value.Branches() {
		result.branches[index] = nestedMutationBranchNode{branch: branch.branch, model: branch.model, action: branch.action}
		if target, ok := branch.Target(); ok {
			result.branches[index].target = &target
		}
		if predicate, ok := branch.Predicate(); ok {
			result.branches[index].predicate = &predicate
		}
		if input, ok := branch.Input(); ok {
			result.branches[index].input = &input
		}
	}
	return result
}

type runtimeFrozenMutationTarget[M any] struct{ target FrozenMutationTarget }

func (value runtimeFrozenMutationTarget[M]) mutationTarget(M) mutationTargetNode[M] {
	return mutationTargetNode[M]{}
}
func (value runtimeFrozenMutationTarget[M]) runtimeFrozenTarget() FrozenMutationTarget {
	return cloneFrozenMutationTarget(value.target)
}

func thawPredicate[M any](value FrozenPredicate) Predicate[M] {
	return Predicate[M]{node: thawPredicateNode(value.root)}
}
func thawPredicateNode(value *frozenCondition) *predicateNode {
	if value == nil {
		return nil
	}
	result := &predicateNode{kind: value.kind, operator: value.operator, mode: value.mode, truth: value.truth, field: value.field, relation: value.relation, operand: cloneFrozenOperand(value.operand), path: cloneJSONPath(value.path), children: make([]*predicateNode, len(value.children))}
	for index, child := range value.children {
		result.children[index] = thawPredicateNode(child)
	}
	return result
}
