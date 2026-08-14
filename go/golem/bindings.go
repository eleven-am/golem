package golem

import (
	"bytes"
	"context"
	"fmt"
	"slices"
)

type actorContextKey struct{}

// ActorFrom is the typed context access point used by hook source. A value is
// present only while a caller-bound hook is executing; system reads bypass
// hooks and never install an actor.
func ActorFrom[A any](ctx context.Context) A {
	if ctx != nil {
		if actor, ok := ctx.Value(actorContextKey{}).(A); ok {
			return actor
		}
	}
	var actor A
	return actor
}

// RuntimeContextWithActor is the narrow runtime bridge used immediately around
// generated hook invocation.
func RuntimeContextWithActor[A any](ctx context.Context, actor A) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// Hook request and result types retain the package-level generated aliases and
// generic model ownership established in P1. P3 fills the read payloads and P4
// fills the mutation payloads without renaming either ABI.
type FindOneHookRequest[M any] struct {
	selector UniqueSelectorValue[M]
	options  []ReadOption[M]
}
type FindOneHookResult[M any] struct{ row Row[M] }
type FindFirstHookRequest[M any] struct{ options []ReadOption[M] }
type FindFirstHookResult[M any] struct {
	row   Row[M]
	found bool
}
type FindManyHookRequest[M any] struct{ options []ReadOption[M] }
type FindManyHookResult[M any] struct{ rows []Row[M] }
type CreateHookRequest[M any] struct{ input CreateInput[M] }
type CreateHookResult[M any] struct {
	row      Row[M]
	executor HookExecutor
}
type UpdateHookRequest[M any] struct {
	target       mutationTargetNode[M]
	frozenTarget *FrozenMutationTarget
	input        UpdateInput[M]
}
type UpdateHookResult[M any] struct {
	before   Row[M]
	after    Row[M]
	executor HookExecutor
}
type DeleteHookRequest[M any] struct {
	target       mutationTargetNode[M]
	frozenTarget *FrozenMutationTarget
}
type DeleteHookResult[M any] struct {
	before   Row[M]
	executor HookExecutor
}
type UpdateManyHookRequest[M any] struct {
	where Predicate[M]
	input UpdateManyInput[M]
}
type UpdateManyHookResult[M any] struct {
	count    int64
	executor HookExecutor
}
type DeleteManyHookRequest[M any] struct{ where Predicate[M] }
type DeleteManyHookResult[M any] struct {
	count    int64
	executor HookExecutor
}

func RuntimeFindOneHookRequest[M any](selector UniqueSelectorValue[M], options []ReadOption[M]) *FindOneHookRequest[M] {
	return &FindOneHookRequest[M]{selector: cloneUniqueSelectorValue(selector), options: cloneReadOptions(options)}
}
func RuntimeFindFirstHookRequest[M any](options []ReadOption[M]) *FindFirstHookRequest[M] {
	return &FindFirstHookRequest[M]{options: cloneReadOptions(options)}
}
func RuntimeFindManyHookRequest[M any](options []ReadOption[M]) *FindManyHookRequest[M] {
	return &FindManyHookRequest[M]{options: cloneReadOptions(options)}
}
func (request *FindOneHookRequest[M]) Selector() UniqueSelectorValue[M] {
	if request == nil {
		return UniqueSelectorValue[M]{}
	}
	return cloneUniqueSelectorValue(request.selector)
}
func (request *FindOneHookRequest[M]) Options() []ReadOption[M] {
	if request == nil {
		return nil
	}
	return cloneReadOptions(request.options)
}
func (request *FindFirstHookRequest[M]) Options() []ReadOption[M] {
	if request == nil {
		return nil
	}
	return cloneReadOptions(request.options)
}
func (request *FindManyHookRequest[M]) Options() []ReadOption[M] {
	if request == nil {
		return nil
	}
	return cloneReadOptions(request.options)
}
func (request *FindOneHookRequest[M]) ReplaceOptions(options ...ReadOption[M]) {
	if request != nil {
		request.options = cloneReadOptions(options)
	}
}
func (request *FindOneHookRequest[M]) ReplaceSelector(selector UniqueSelectorValue[M]) {
	if request != nil {
		request.selector = cloneUniqueSelectorValue(selector)
	}
}
func (request *FindFirstHookRequest[M]) ReplaceOptions(options ...ReadOption[M]) {
	if request != nil {
		request.options = cloneReadOptions(options)
	}
}
func (request *FindManyHookRequest[M]) ReplaceOptions(options ...ReadOption[M]) {
	if request != nil {
		request.options = cloneReadOptions(options)
	}
}
func (request *FindOneHookRequest[M]) AppendOptions(options ...ReadOption[M]) {
	if request != nil {
		request.options = append(request.options, cloneReadOptions(options)...)
	}
}
func (request *FindFirstHookRequest[M]) AppendOptions(options ...ReadOption[M]) {
	if request != nil {
		request.options = append(request.options, cloneReadOptions(options)...)
	}
}
func (request *FindManyHookRequest[M]) AppendOptions(options ...ReadOption[M]) {
	if request != nil {
		request.options = append(request.options, cloneReadOptions(options)...)
	}
}

func RuntimeFindOneHookResult[M any](row Row[M]) FindOneHookResult[M] {
	return FindOneHookResult[M]{row: cloneRow(row)}
}
func RuntimeFindFirstHookResult[M any](row Row[M], found bool) FindFirstHookResult[M] {
	result := FindFirstHookResult[M]{found: found}
	if found {
		result.row = cloneRow(row)
	}
	return result
}
func RuntimeFindManyHookResult[M any](rows []Row[M]) FindManyHookResult[M] {
	return FindManyHookResult[M]{rows: cloneRows(rows)}
}
func (result FindOneHookResult[M]) Row() Row[M] { return cloneRow(result.row) }
func (result FindFirstHookResult[M]) Row() (Row[M], bool) {
	if !result.found {
		return Row[M]{}, false
	}
	return cloneRow(result.row), true
}
func (result FindManyHookResult[M]) Rows() []Row[M] { return cloneRows(result.rows) }

func cloneReadOptions[M any](options []ReadOption[M]) []ReadOption[M] {
	result := make([]ReadOption[M], len(options))
	var witness M
	for index, option := range options {
		if option != nil {
			result[index] = readOptionValue[M]{node: cloneReadOption(option.readOption(witness))}
		}
	}
	return result
}
func cloneUniqueSelectorValue[M any](selector UniqueSelectorValue[M]) UniqueSelectorValue[M] {
	return GeneratedUniqueSelectorValue[M](selector.model, selector.key, selector.components...)
}
func cloneRows[M any](rows []Row[M]) []Row[M] {
	result := make([]Row[M], len(rows))
	for index, row := range rows {
		result[index] = cloneRow(row)
	}
	return result
}

func RuntimeCreateHookRequest[M any](input CreateInput[M]) *CreateHookRequest[M] {
	return &CreateHookRequest[M]{input: cloneCreateInput(input)}
}

func RuntimeUpdateHookRequest[M any](target MutationTarget[M], input UpdateInput[M]) *UpdateHookRequest[M] {
	if frozen, err := RuntimeFreezeMutationTarget(target); err == nil {
		return &UpdateHookRequest[M]{frozenTarget: &frozen, input: cloneUpdateInput(input)}
	}
	var node mutationTargetNode[M]
	if target != nil {
		var witness M
		node = target.mutationTarget(witness)
	}
	return &UpdateHookRequest[M]{target: cloneMutationTargetNode(node), input: cloneUpdateInput(input)}
}

func RuntimeDeleteHookRequest[M any](target MutationTarget[M]) *DeleteHookRequest[M] {
	if frozen, err := RuntimeFreezeMutationTarget(target); err == nil {
		return &DeleteHookRequest[M]{frozenTarget: &frozen}
	}
	var node mutationTargetNode[M]
	if target != nil {
		var witness M
		node = target.mutationTarget(witness)
	}
	return &DeleteHookRequest[M]{target: cloneMutationTargetNode(node)}
}

func RuntimeUpdateManyHookRequest[M any](where Predicate[M], input UpdateManyInput[M]) *UpdateManyHookRequest[M] {
	return &UpdateManyHookRequest[M]{where: where, input: cloneUpdateManyInput(input)}
}

func RuntimeDeleteManyHookRequest[M any](where Predicate[M]) *DeleteManyHookRequest[M] {
	return &DeleteManyHookRequest[M]{where: where}
}

func (request *CreateHookRequest[M]) Input() CreateInput[M] {
	if request == nil {
		return CreateInput[M]{}
	}
	return cloneCreateInput(request.input)
}

func (request *UpdateHookRequest[M]) Target() MutationTarget[M] {
	if request == nil {
		return nil
	}
	if request.frozenTarget != nil {
		return runtimeFrozenMutationTarget[M]{target: cloneFrozenMutationTarget(*request.frozenTarget)}
	}
	return mutationTargetFromNode(request.target)
}

func (request *UpdateHookRequest[M]) Input() UpdateInput[M] {
	if request == nil {
		return UpdateInput[M]{}
	}
	return cloneUpdateInput(request.input)
}

func (request *DeleteHookRequest[M]) Target() MutationTarget[M] {
	if request == nil {
		return nil
	}
	if request.frozenTarget != nil {
		return runtimeFrozenMutationTarget[M]{target: cloneFrozenMutationTarget(*request.frozenTarget)}
	}
	return mutationTargetFromNode(request.target)
}

func (request *UpdateManyHookRequest[M]) Where() Predicate[M] {
	if request == nil {
		return Predicate[M]{}
	}
	return request.where
}

func (request *UpdateManyHookRequest[M]) Input() UpdateManyInput[M] {
	if request == nil {
		return UpdateManyInput[M]{}
	}
	return cloneUpdateManyInput(request.input)
}

func (request *DeleteManyHookRequest[M]) Where() Predicate[M] {
	if request == nil {
		return Predicate[M]{}
	}
	return request.where
}

func (request *CreateHookRequest[M]) ReplaceInput(input CreateInput[M]) {
	if request != nil {
		request.input = cloneCreateInput(input)
	}
}

func (request *UpdateHookRequest[M]) ReplaceInput(input UpdateInput[M]) {
	if request != nil {
		request.input = cloneUpdateInput(input)
	}
}

func (request *UpdateHookRequest[M]) ReplaceTarget(target MutationTarget[M]) {
	if request == nil {
		return
	}
	if target == nil {
		request.target = mutationTargetNode[M]{}
		request.frozenTarget = nil
		return
	}
	if frozen, err := RuntimeFreezeMutationTarget(target); err == nil {
		request.frozenTarget = &frozen
		request.target = mutationTargetNode[M]{}
		return
	}
	request.frozenTarget = nil
	var witness M
	request.target = cloneMutationTargetNode(target.mutationTarget(witness))
}

func (request *DeleteHookRequest[M]) ReplaceTarget(target MutationTarget[M]) {
	if request == nil {
		return
	}
	if target == nil {
		request.target = mutationTargetNode[M]{}
		request.frozenTarget = nil
		return
	}
	if frozen, err := RuntimeFreezeMutationTarget(target); err == nil {
		request.frozenTarget = &frozen
		request.target = mutationTargetNode[M]{}
		return
	}
	request.frozenTarget = nil
	var witness M
	request.target = cloneMutationTargetNode(target.mutationTarget(witness))
}

func (request *UpdateManyHookRequest[M]) ReplaceInput(input UpdateManyInput[M]) {
	if request != nil {
		request.input = cloneUpdateManyInput(input)
	}
}

func (request *UpdateManyHookRequest[M]) ReplaceWhere(where Predicate[M]) {
	if request != nil {
		request.where = where
	}
}

func (request *DeleteManyHookRequest[M]) ReplaceWhere(where Predicate[M]) {
	if request != nil {
		request.where = where
	}
}

// SetCreate preserves model and value type identity at compile time and
// replaces the field's prior create assignment in the owned hook facade. The
// P4 binder still verifies generated identity and write exposure afterward.
func SetCreate[M, V any](request *CreateHookRequest[M], field CreateFieldCapability[M, V], value V) error {
	if request == nil || field == nil {
		return invalidMutation("create", ModelID{}, FieldID{}, "create hook request or field is invalid")
	}
	var modelWitness M
	var valueWitness V
	model, column := field.generatedCreateFieldCapability(modelWitness, valueWitness)
	if column == nil {
		return invalidMutation("create", request.input.model, FieldID{}, "create hook request or field is invalid")
	}
	if request.input.model == (ModelID{}) && model == (ModelID{}) && column.fieldIdentity() == (FieldID{}) {
		// Preserve the P1 signature-probe behavior. Runtime-created P4 hook
		// requests always carry a non-zero model identity and never take this
		// compatibility-only zero-shell path.
		return nil
	}
	if request.input.model == (ModelID{}) || model != request.input.model || column.fieldIdentity() == (FieldID{}) {
		return invalidMutation("create", request.input.model, column.fieldIdentity(), "create hook request or field is invalid")
	}
	var witness M
	node := GeneratedCreateFieldValue(request.input.model, column, value).createMutationValue(witness)
	values := make([]mutationValueNode, 0, len(request.input.values)+1)
	replaced := false
	for _, existing := range request.input.values {
		if existing.field == node.field {
			if !replaced {
				values = append(values, node.clone())
				replaced = true
			}
			continue
		}
		values = append(values, existing.clone())
	}
	if !replaced {
		values = append(values, node.clone())
	}
	request.input.values = values
	return nil
}

func RuntimeUpdateManyHookResult[M any](count int64) UpdateManyHookResult[M] {
	return UpdateManyHookResult[M]{count: count}
}

func RuntimeDeleteManyHookResult[M any](count int64) DeleteManyHookResult[M] {
	return DeleteManyHookResult[M]{count: count}
}

func runtimeCreateHookResultWithExecutor[M any](row Row[M], executor HookExecutor) CreateHookResult[M] {
	return CreateHookResult[M]{row: cloneRow(row), executor: executor}
}
func runtimeUpdateHookResultWithExecutor[M any](before, after Row[M], executor HookExecutor) UpdateHookResult[M] {
	return UpdateHookResult[M]{before: cloneRow(before), after: cloneRow(after), executor: executor}
}
func runtimeDeleteHookResultWithExecutor[M any](before Row[M], executor HookExecutor) DeleteHookResult[M] {
	return DeleteHookResult[M]{before: cloneRow(before), executor: executor}
}
func runtimeUpdateManyHookResultWithExecutor[M any](count int64, executor HookExecutor) UpdateManyHookResult[M] {
	return UpdateManyHookResult[M]{count: count, executor: executor}
}
func runtimeDeleteManyHookResultWithExecutor[M any](count int64, executor HookExecutor) DeleteManyHookResult[M] {
	return DeleteManyHookResult[M]{count: count, executor: executor}
}

func (result CreateHookResult[M]) Row() Row[M]                { return cloneRow(result.row) }
func (result UpdateHookResult[M]) Before() Row[M]             { return cloneRow(result.before) }
func (result UpdateHookResult[M]) After() Row[M]              { return cloneRow(result.after) }
func (result DeleteHookResult[M]) Before() Row[M]             { return cloneRow(result.before) }
func (result UpdateManyHookResult[M]) Count() int64           { return result.count }
func (result DeleteManyHookResult[M]) Count() int64           { return result.count }
func (result CreateHookResult[M]) Executor() HookExecutor     { return result.executor }
func (result UpdateHookResult[M]) Executor() HookExecutor     { return result.executor }
func (result DeleteHookResult[M]) Executor() HookExecutor     { return result.executor }
func (result UpdateManyHookResult[M]) Executor() HookExecutor { return result.executor }
func (result DeleteManyHookResult[M]) Executor() HookExecutor { return result.executor }

// AfterCommitFailure reports trusted post-commit hook failures without
// changing an already committed mutation into an operation error.
type AfterCommitFailure struct {
	operation HookOperation
	model     ModelID
	cause     error
}

func RuntimeAfterCommitFailure(operation HookOperation, model ModelID, cause error) AfterCommitFailure {
	return AfterCommitFailure{operation: operation, model: model, cause: cause}
}

func (failure AfterCommitFailure) Operation() HookOperation { return failure.operation }
func (failure AfterCommitFailure) Model() ModelID           { return failure.model }
func (failure AfterCommitFailure) Cause() error             { return failure.cause }

type HookOperation string
type HookPhase string

const (
	HookFindOne    HookOperation = "findOne"
	HookFindFirst  HookOperation = "findFirst"
	HookFindMany   HookOperation = "findMany"
	HookCreate     HookOperation = "create"
	HookUpdate     HookOperation = "update"
	HookDelete     HookOperation = "delete"
	HookUpdateMany HookOperation = "updateMany"
	HookDeleteMany HookOperation = "deleteMany"

	HookBefore      HookPhase = "before"
	HookAfter       HookPhase = "after"
	HookAfterCommit HookPhase = "afterCommit"
)

// PolicyFactory is called once per execution with the freshly resolved actor.
type PolicyFactory[A any] func(A) (FrozenPolicy, error)

// PolicyBinding and HookBinding are immutable generated descriptors. Their
// fields remain opaque so P1 exposes no policy or hook execution semantics.
type PolicyBinding[A any] struct {
	model ModelID
	build PolicyFactory[A]
}

type HookBinding[A any] struct {
	model          ModelID
	operation      HookOperation
	phase          HookPhase
	invoke         func(context.Context, any) error
	readBefore     readBeforeBridge
	readResult     readResultBridge
	mutationBefore mutationBeforeBridge
	mutationResult mutationResultBridge
	_              func() A
}

type PackageBindings[A any] struct {
	generation SchemaDigest
	policies   []PolicyBinding[A]
	hooks      []HookBinding[A]
}

type ApplicationBindings[A any] struct {
	generation SchemaDigest
	packages   []PackageBindings[A]
}

func GeneratedPolicyBinding[A, M any](model ModelID, build PolicyFactory[A]) PolicyBinding[A] {
	return PolicyBinding[A]{model: model, build: build}
}

func GeneratedBeforeHookBinding[A, M, Request any](model ModelID, operation HookOperation, invoke func(context.Context, *Request) error) HookBinding[A] {
	return HookBinding[A]{model: model, operation: operation, phase: HookBefore, readBefore: generatedReadBeforeBridge[M](operation, invoke), mutationBefore: generatedMutationBeforeBridge[M](operation, invoke), invoke: func(ctx context.Context, value any) error {
		request, ok := value.(*Request)
		if !ok {
			return errGeneratedBindingType
		}
		return invoke(ctx, request)
	}}
}

func GeneratedAfterHookBinding[A, M, Result any](model ModelID, operation HookOperation, invoke func(context.Context, Result) error) HookBinding[A] {
	return HookBinding[A]{model: model, operation: operation, phase: HookAfter, readResult: generatedReadResultBridge[M](operation, invoke), mutationResult: generatedMutationResultBridge[M](operation, invoke), invoke: func(ctx context.Context, value any) error {
		result, ok := value.(Result)
		if !ok {
			return errGeneratedBindingType
		}
		return invoke(ctx, result)
	}}
}

func GeneratedAfterCommitHookBinding[A, M, Result any](model ModelID, operation HookOperation, invoke func(context.Context, Result) error) HookBinding[A] {
	binding := GeneratedAfterHookBinding[A, M](model, operation, invoke)
	binding.phase = HookAfterCommit
	return binding
}

func GeneratedPackageBindings[A any](policies []PolicyBinding[A], hooks []HookBinding[A]) PackageBindings[A] {
	return PackageBindings[A]{policies: append([]PolicyBinding[A](nil), policies...), hooks: append([]HookBinding[A](nil), hooks...)}
}

func GeneratedStampedPackageBindings[A any](generation SchemaDigest, policies []PolicyBinding[A], hooks []HookBinding[A]) PackageBindings[A] {
	result := GeneratedPackageBindings(policies, hooks)
	result.generation = generation
	return result
}

func (bindings PackageBindings[A]) GenerationDigest() SchemaDigest { return bindings.generation }

func GeneratedApplicationBindings[A any](expected SchemaDigest, packages ...PackageBindings[A]) (ApplicationBindings[A], error) {
	digests := make([]SchemaDigest, len(packages))
	for index, pkg := range packages {
		digests[index] = pkg.generation
	}
	if err := validateGenerationDigests("bindings", expected, digests); err != nil {
		return ApplicationBindings[A]{}, err
	}
	return ApplicationBindings[A]{generation: expected, packages: append([]PackageBindings[A](nil), packages...)}, nil
}

func (bindings ApplicationBindings[A]) GenerationDigest() SchemaDigest { return bindings.generation }

// PolicyInventory returns the statically attached model identities without
// executing application policy code. The returned slice is detached from the
// generated binding graph.
func (bindings ApplicationBindings[A]) PolicyInventory() []ModelID {
	result := make([]ModelID, 0)
	for _, pkg := range bindings.packages {
		for _, binding := range pkg.policies {
			result = append(result, binding.model)
		}
	}
	slices.SortFunc(result, func(left, right ModelID) int { return bytes.Compare(left[:], right[:]) })
	return result
}

type RuntimeHookMetadata struct {
	Model     ModelID
	Operation HookOperation
	Phase     HookPhase
}

func invokeGeneratedHookSafely(invoke func() error) (err error) {
	defer func() {
		if recover() != nil {
			// Panic payloads are arbitrary application data. Transactional/read
			// hooks have no trusted panic-reporting callback, so collapse them to
			// a fixed cause that the runtime wraps in its existing closed hook
			// error category.
			err = fmt.Errorf("generated hook panicked")
		}
	}()
	return invoke()
}

func (bindings ApplicationBindings[A]) RuntimeHookInventory() []RuntimeHookMetadata {
	result := make([]RuntimeHookMetadata, 0)
	for _, pkg := range bindings.packages {
		for _, binding := range pkg.hooks {
			result = append(result, RuntimeHookMetadata{Model: binding.model, Operation: binding.operation, Phase: binding.phase})
		}
	}
	return result
}

// RuntimeInvokeHooks executes matching generated hooks in deterministic package
// and declaration order. The opaque payload type is checked by each generated
// bridge before application code runs.
func RuntimeInvokeHooks[A any](ctx context.Context, bindings ApplicationBindings[A], model ModelID, operation HookOperation, phase HookPhase, payload any) error {
	if ctx == nil || model == (ModelID{}) || payload == nil {
		return fmt.Errorf("generated hooks: context, model, and payload are required")
	}
	for packageIndex, pkg := range bindings.packages {
		if pkg.generation != bindings.generation {
			return fmt.Errorf("generated hooks: package %d generation mismatch", packageIndex)
		}
		for _, binding := range pkg.hooks {
			if binding.model != model || binding.operation != operation || binding.phase != phase {
				continue
			}
			if binding.invoke == nil {
				return fmt.Errorf("generated hooks: matching hook is incomplete")
			}
			if err := invokeGeneratedHookSafely(func() error { return binding.invoke(ctx, payload) }); err != nil {
				return err
			}
		}
	}
	return nil
}

// RuntimeInvokeMutationBeforeHooks runs generated hooks one at a time. The
// validator is invoked after every transformation so a later hook can never
// observe an unbound or unauthorized request produced by an earlier hook.
func RuntimeInvokeMutationBeforeHooks[A any](ctx context.Context, bindings ApplicationBindings[A], request RuntimeMutationHookRequest, validate func(RuntimeMutationHookRequest) error) (RuntimeMutationHookRequest, error) {
	if ctx == nil || request.model == (ModelID{}) || validate == nil {
		return RuntimeMutationHookRequest{}, fmt.Errorf("generated mutation hooks: context, request, and validator are required")
	}
	current := request
	for packageIndex, pkg := range bindings.packages {
		if pkg.generation != bindings.generation {
			return RuntimeMutationHookRequest{}, fmt.Errorf("generated mutation hooks: package %d generation mismatch", packageIndex)
		}
		for _, binding := range pkg.hooks {
			if binding.model != request.model || binding.operation != request.operation || binding.phase != HookBefore {
				continue
			}
			if binding.mutationBefore == nil {
				return RuntimeMutationHookRequest{}, fmt.Errorf("generated mutation hooks: matching before hook has no mutation bridge")
			}
			var err error
			err = invokeGeneratedHookSafely(func() error {
				var invokeErr error
				current, invokeErr = binding.mutationBefore(ctx, current)
				return invokeErr
			})
			if err != nil {
				return RuntimeMutationHookRequest{}, err
			}
			if err := validate(current); err != nil {
				return RuntimeMutationHookRequest{}, err
			}
		}
	}
	return current, nil
}

func RuntimeInvokeMutationResultHooks[A any](ctx context.Context, bindings ApplicationBindings[A], result RuntimeMutationHookResult, phase HookPhase) error {
	if ctx == nil || result.model == (ModelID{}) || (phase != HookAfter && phase != HookAfterCommit) {
		return fmt.Errorf("generated mutation hooks: invalid result invocation")
	}
	for packageIndex, pkg := range bindings.packages {
		if pkg.generation != bindings.generation {
			return fmt.Errorf("generated mutation hooks: package %d generation mismatch", packageIndex)
		}
		for _, binding := range pkg.hooks {
			if binding.model != result.model || binding.operation != result.operation || binding.phase != phase {
				continue
			}
			if binding.mutationResult == nil {
				return fmt.Errorf("generated mutation hooks: matching result hook has no mutation bridge")
			}
			if err := invokeGeneratedHookSafely(func() error { return binding.mutationResult(ctx, result) }); err != nil {
				return err
			}
		}
	}
	return nil
}

// RuntimeInvokeReadBeforeHooks restores the generated concrete model type for
// each matching hook, then freezes and validates every transformation before a
// later hook may observe it. No SQL is performed by this boundary.
func RuntimeInvokeReadBeforeHooks[A any](ctx context.Context, bindings ApplicationBindings[A], request RuntimeReadHookRequest, validate func(RuntimeReadHookRequest) error) (RuntimeReadHookRequest, error) {
	if ctx == nil || request.model == (ModelID{}) || validate == nil {
		return RuntimeReadHookRequest{}, fmt.Errorf("generated read hooks: context, request, and validator are required")
	}
	current := request
	for packageIndex, pkg := range bindings.packages {
		if pkg.generation != bindings.generation {
			return RuntimeReadHookRequest{}, fmt.Errorf("generated read hooks: package %d generation mismatch", packageIndex)
		}
		for _, binding := range pkg.hooks {
			if binding.model != request.model || binding.operation != request.operation || binding.phase != HookBefore {
				continue
			}
			if binding.readBefore == nil {
				return RuntimeReadHookRequest{}, fmt.Errorf("generated read hooks: matching before hook has no read bridge")
			}
			var err error
			err = invokeGeneratedHookSafely(func() error {
				var invokeErr error
				current, invokeErr = binding.readBefore(ctx, current)
				return invokeErr
			})
			if err != nil {
				return RuntimeReadHookRequest{}, err
			}
			if err := validate(current); err != nil {
				return RuntimeReadHookRequest{}, err
			}
		}
	}
	return current, nil
}

func RuntimeInvokeReadResultHooks[A any](ctx context.Context, bindings ApplicationBindings[A], result RuntimeReadHookResult) error {
	if ctx == nil || result.model == (ModelID{}) {
		return fmt.Errorf("generated read hooks: invalid result invocation")
	}
	for packageIndex, pkg := range bindings.packages {
		if pkg.generation != bindings.generation {
			return fmt.Errorf("generated read hooks: package %d generation mismatch", packageIndex)
		}
		for _, binding := range pkg.hooks {
			if binding.model != result.model || binding.operation != result.operation || binding.phase != HookAfter {
				continue
			}
			if binding.readResult == nil {
				return fmt.Errorf("generated read hooks: matching result hook has no read bridge")
			}
			if err := invokeGeneratedHookSafely(func() error { return binding.readResult(ctx, result) }); err != nil {
				return err
			}
		}
	}
	return nil
}

// GeneratedPolicySet is one actor-specific, execution-scoped factory result.
// It is immutable and intentionally carries no actor value.
type GeneratedPolicySet struct {
	generation SchemaDigest
	policies   []FrozenPolicy
}

func (set GeneratedPolicySet) GenerationDigest() SchemaDigest { return set.generation }

func (set GeneratedPolicySet) Policies() []FrozenPolicy {
	result := make([]FrozenPolicy, len(set.policies))
	for index, policy := range set.policies {
		result[index] = FrozenPolicy{model: policy.model, rules: make([]frozenRule, len(policy.rules)), canonical: append([]byte(nil), policy.canonical...)}
		for ruleIndex, rule := range policy.rules {
			result[index].rules[ruleIndex] = cloneFrozenRule(rule)
		}
	}
	return result
}

// BuildGeneratedPolicySet invokes every generated policy factory exactly once
// for this call. No result is cached in package, application, or engine state.
func BuildGeneratedPolicySet[A any](bindings ApplicationBindings[A], actor A) (GeneratedPolicySet, error) {
	if bindings.generation == (SchemaDigest{}) {
		return GeneratedPolicySet{}, fmt.Errorf("generated policy set: application bindings are unstamped")
	}
	// Validate the complete static graph before executing any application code.
	// This keeps malformed generated artifacts from producing a partially
	// observed execution where an earlier factory ran before a later duplicate,
	// missing factory, or generation mismatch was discovered.
	seen := make(map[ModelID]struct{})
	for packageIndex, pkg := range bindings.packages {
		if pkg.generation != bindings.generation {
			return GeneratedPolicySet{}, fmt.Errorf("generated policy set: package %d generation mismatch", packageIndex)
		}
		for bindingIndex, binding := range pkg.policies {
			if binding.model == (ModelID{}) || binding.build == nil {
				return GeneratedPolicySet{}, fmt.Errorf("generated policy set: package %d binding %d is incomplete", packageIndex, bindingIndex)
			}
			if _, duplicate := seen[binding.model]; duplicate {
				return GeneratedPolicySet{}, fmt.Errorf("generated policy set: duplicate policy binding for model %x", binding.model)
			}
			seen[binding.model] = struct{}{}
		}
	}

	policies := make([]FrozenPolicy, 0, len(seen))
	for _, pkg := range bindings.packages {
		for _, binding := range pkg.policies {
			policy, err := binding.build(actor)
			if err != nil {
				return GeneratedPolicySet{}, fmt.Errorf("generated policy set: model %x factory: %w", binding.model, err)
			}
			if policy.View().ModelID() != binding.model {
				return GeneratedPolicySet{}, fmt.Errorf("generated policy set: model %x factory returned policy for %x", binding.model, policy.View().ModelID())
			}
			policies = append(policies, policy)
		}
	}
	return GeneratedPolicySet{generation: bindings.generation, policies: policies}, nil
}

type generatedBindingError string

func (err generatedBindingError) Error() string { return string(err) }

const errGeneratedBindingType generatedBindingError = "generated binding received an incompatible opaque value"
