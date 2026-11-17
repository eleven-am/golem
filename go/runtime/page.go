package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/continuation"
	"github.com/eleven-am/golem/go/internal/operationargs"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
)

// PageConfig supplies durable infrastructure for registered custom pages.
// Scope must identify the admitted actor's account/tenant/profile and
// authorization revision across processes. It is not a policy-cache key.
type PageConfig[A any] struct {
	ActiveKey string
	Keys      []golem.ContinuationKey
	MaxAge    time.Duration
	Scope     func(context.Context, A) (string, error)
}

type pageRuntime[A any] struct {
	codec       *continuation.Codec
	registry    *operationargs.Registry
	operations  map[string]compilerir.CustomOperationContractIR
	contract    string
	incarnation string
	scope       func(context.Context, A) (string, error)
}

func newPageRuntime[A any](config *PageConfig[A], bundle golem.SchemaBundle, bindings golem.ApplicationBindings[A]) (*pageRuntime[A], error) {
	compilation, err := policyschema.DecodeActive(bundle)
	if err != nil {
		return nil, err
	}
	operations := make(map[string]compilerir.CustomOperationContractIR)
	for _, operation := range compilation.Contract.CustomOperations {
		if operation.Pagination != nil {
			operations[string(operation.ExtensionID)] = operation
		}
	}
	inventory := bindings.PageQueryInventory()
	if len(operations) != len(inventory) {
		return nil, fmt.Errorf("P6_PAGE_CONFIG: page resolver inventory differs from the contract")
	}
	for _, id := range inventory {
		if _, ok := operations[id]; !ok {
			return nil, fmt.Errorf("P6_PAGE_CONFIG: page resolver identity is not declared")
		}
	}
	if len(operations) == 0 {
		if config != nil {
			return nil, fmt.Errorf("P6_PAGE_CONFIG: Pages requires a registered PageQuery")
		}
		return nil, nil
	}
	if config == nil || config.Scope == nil || compilation.Contract.DataIncarnation == nil {
		return nil, fmt.Errorf("P6_PAGE_CONFIG: keyring, stable scope callback and data incarnation are required")
	}
	keys := make([]continuation.Key, len(config.Keys))
	for index, key := range config.Keys {
		keys[index] = continuation.Key{ID: key.ID, Material: key.Material}
	}
	codec, err := continuation.New(continuation.Config{ActiveKey: config.ActiveKey, Keys: keys, MaxAge: config.MaxAge})
	if err != nil {
		return nil, fmt.Errorf("P6_PAGE_CONFIG: %w", err)
	}
	registry, err := operationargs.New(compilation)
	if err != nil {
		return nil, fmt.Errorf("P6_PAGE_CONFIG: %w", err)
	}
	return &pageRuntime[A]{codec: codec, registry: registry, operations: operations, contract: fmt.Sprintf("%x", bundle.Contract().Fingerprint()), scope: config.Scope}, nil
}

// DispatchCallerPage is the shared generated-glue page boundary. Only an exact
// registered page resolver may receive authenticated state. Arguments are
// prepared once, then the same owned values are hashed and passed to run.
// No Within grants or transaction capabilities are installed here.
func DispatchCallerPage[P, A any](ctx context.Context, caller *Caller[P, A], extensionID string, resolver any, arguments map[string]any, input golem.PageInput, run func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error)) (golem.RuntimePageResult, error) {
	var zero golem.RuntimePageResult
	if ctx == nil || caller == nil || caller.app == nil || caller.policies == nil || caller.execution == 0 || run == nil {
		return zero, fmt.Errorf("P6_PAGE_OPERATION: caller, context and page dispatch are required")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	registered, ok := caller.app.bindings.PageQuery(resolver)
	if !ok || registered != extensionID || caller.app.pages == nil {
		return zero, fmt.Errorf("P6_PAGE_OPERATION: resolver is not this registered page query")
	}
	pages := caller.app.pages
	operation, ok := pages.operations[extensionID]
	if !ok || operation.Pagination == nil {
		return zero, fmt.Errorf("P6_PAGE_OPERATION: page contract is unavailable")
	}
	limits := operation.Pagination
	if len(input.After) > continuation.MaxTokenBytes {
		return zero, pageContinuationError(operation.Name, continuation.ErrInvalid)
	}
	take := input.Take
	if take == 0 {
		take = limits.DefaultTake
	}
	if take == 0 || take > limits.MaxTake || caller.app.readLimits.plan.MaxTake > 0 && uint64(take) > uint64(caller.app.readLimits.plan.MaxTake) {
		return zero, golem.InputError(golem.ReasonInvalidValue, operation.Name, "take", "page take exceeds its declared or runtime maximum", nil)
	}
	if len(arguments) > len(operation.Arguments)-2 || len(arguments) > operationargs.MaxCanonicalArgumentNodes {
		return zero, golem.InputError(golem.ReasonLimitExceeded, operation.Name, "arguments", "page arguments exceed the declared input fields", nil)
	}
	supplied := make(map[string]any, len(arguments)+2)
	nameBytesLeft := operationargs.MaxCanonicalArgumentBytes
	for name, value := range arguments {
		if len(name) > nameBytesLeft {
			return zero, golem.InputError(golem.ReasonLimitExceeded, operation.Name, "arguments", "page argument names exceed the input bound", nil)
		}
		nameBytesLeft -= len(name)
		if name == "take" || name == "after" {
			return zero, golem.InputError(golem.ReasonInvalidValue, operation.Name, name, "page arguments cannot override runtime paging inputs", nil)
		}
		supplied[name] = value
	}
	supplied["take"] = int32(take)
	if input.After != "" {
		supplied["after"] = input.After
	}
	request, err := pages.registry.PrepareCanonical(compilerir.CustomOperationQuery, operation.Name, supplied)
	if err != nil {
		return zero, err
	}
	canonical, err := pages.registry.CanonicalArguments(request, "after")
	if err != nil {
		return zero, err
	}
	ctx = CallerOperationContext(ctx, caller)
	scope, _, err := callerPageScope(ctx, caller, operation, canonical)
	if err != nil {
		return zero, err
	}
	var cursor continuation.Cursor
	if input.After == "" {
		cursor, err = pages.codec.Begin(scope)
	} else {
		cursor, err = pages.codec.Resume(scope, input.After)
	}
	if err != nil {
		return zero, pageContinuationError(operation.Name, err)
	}
	owned := make(map[string]any, len(arguments))
	for _, argument := range request.Arguments() {
		if argument.Name() != "take" && argument.Name() != "after" && argument.Present() {
			owned[argument.Name()] = argument.Value()
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	state := golem.RuntimePageStateValue(take, limits.MaxCandidates, cursor.Consumed(), cursor.Position(), cursor.Expires())
	batch, err := run(ctx, state, owned)
	if err != nil {
		return zero, err
	}
	items, consumed, position, more := golem.RuntimePageBatchParts(batch)
	if len(items) > int(take) || consumed > limits.MaxCandidates || uint64(len(items)) > uint64(consumed) {
		return zero, fmt.Errorf("P6_PAGE_RESULT: page exceeds its item or candidate budget")
	}
	// Validate progress before an adapter can expose any result. Encryption is
	// deferred until after hydration, so an invalid result never issues a token.
	if more {
		if err := pages.codec.CheckAdvance(cursor, position, consumed); err != nil {
			if errors.Is(err, continuation.ErrExpired) {
				return zero, pageContinuationError(operation.Name, err)
			}
			return zero, fmt.Errorf("P6_PAGE_RESULT: nonterminal checkpoint does not advance: %w", err)
		}
	}
	var once sync.Once
	var next string
	var finishErr error
	return golem.RuntimePageResultValue(items, func() (string, error) {
		once.Do(func() {
			if finishErr = ctx.Err(); finishErr != nil {
				return
			}
			if !time.Now().Before(cursor.Expires()) {
				finishErr = pageContinuationError(operation.Name, continuation.ErrExpired)
				return
			}
			if more {
				next, finishErr = pages.codec.Advance(cursor, position, consumed)
				if finishErr != nil {
					finishErr = pageContinuationError(operation.Name, finishErr)
				}
			}
		})
		return next, finishErr
	}), nil
}

// callerPageScope is shared by page traversal and its invalidation audience.
// It pins the admitted actor and validates every binding component through the
// continuation owner; invalidation never serializes arbitrary actor values.
func callerPageScope[P, A any](ctx context.Context, caller *Caller[P, A], operation compilerir.CustomOperationContractIR, canonical []byte) (continuation.Scope, string, error) {
	if caller == nil || caller.app == nil || caller.app.pages == nil || caller.app.pages.scope == nil || operation.Pagination == nil {
		return continuation.Scope{}, "", fmt.Errorf("P6_PAGE_SCOPE: registered page scope is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return continuation.Scope{}, "", err
	}
	pages := caller.app.pages
	audience, err := pages.scope(CallerOperationContext(ctx, caller), caller.actor)
	if err != nil || audience == "" {
		return continuation.Scope{}, "", golem.RuntimeOperationError(golem.CodeUnauthenticated, operation.Name, golem.ModelID{}, golem.FieldID{}, "page scope is unavailable", err)
	}
	if err := ctx.Err(); err != nil {
		return continuation.Scope{}, "", err
	}
	scope, err := continuation.Bind(continuation.Binding{Contract: pages.contract, Operation: string(operation.ExtensionID), Revision: operation.Pagination.Revision, Incarnation: pages.incarnation, Arguments: canonical, Actor: []byte(audience)})
	if err != nil {
		return continuation.Scope{}, "", fmt.Errorf("P6_PAGE_SCOPE: stable page scope is invalid")
	}
	return scope, audience, nil
}

func pageContinuationError(operation string, cause error) error {
	if errors.Is(cause, continuation.ErrExpired) {
		return golem.InputError(golem.ReasonContinuationExpired, operation, "after", "continuation expired; restart traversal", cause)
	}
	return golem.InputError(golem.ReasonInvalidValue, operation, "after", "invalid continuation for this page request", cause)
}
