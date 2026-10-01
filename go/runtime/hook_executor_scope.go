package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/eleven-am/golem/go/golem"
)

var errHookExecutorExpired = errors.New("P4_RUNTIME_HOOK_EXECUTOR: executor used after its hook returned")

var errTransactionCallEnded = errors.New("P4_RUNTIME_TRANSACTION: transaction used after its callback returned")

type usageGate struct {
	mu       sync.Mutex
	idle     sync.Cond
	closed   bool
	inFlight int
}

func (gate *usageGate) enter() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.closed {
		return false
	}
	gate.inFlight++
	return true
}

func (gate *usageGate) leave() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.inFlight--
	if gate.inFlight == 0 {
		gate.idle.Broadcast()
	}
}

func (gate *usageGate) close() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.closed = true
	if gate.idle.L == nil {
		gate.idle.L = &gate.mu
	}
	for gate.inFlight > 0 {
		gate.idle.Wait()
	}
}

func (gate *usageGate) init() *usageGate {
	gate.idle.L = &gate.mu
	return gate
}

type hookExecutorGate struct {
	usage *usageGate
	held  *heldWrite
}

type heldWrite struct {
	scope   *heldWrite
	binding *executionBinding
	owner   *heldWrite
	nested  sync.Mutex
	ended   atomic.Bool
	hook    *usageGate
}

type heldWriteKey struct{}

func innermostHeldWrite(ctx context.Context) *heldWrite {
	if ctx == nil {
		return nil
	}
	held, _ := ctx.Value(heldWriteKey{}).(*heldWrite)
	return held
}

func heldWriteFor(ctx context.Context, binding *executionBinding) *heldWrite {
	for held := innermostHeldWrite(ctx); held != nil; held = held.scope {
		if held.binding == binding {
			return held
		}
	}
	return nil
}

func (held *heldWrite) descendsFrom(ancestor *heldWrite) bool {
	if ancestor == nil {
		return true
	}
	for current := held; current != nil; current = current.owner {
		if current == ancestor {
			return true
		}
	}
	return false
}

func newHookExecutorGate(ctx context.Context, binding *executionBinding) *hookExecutorGate {
	return &hookExecutorGate{usage: (&usageGate{}).init(), held: heldWriteFor(ctx, binding)}
}

func (binding *executionBinding) beginCall(ctx context.Context) (context.Context, func(), error) {
	if binding == nil || !binding.scoped {
		return ctx, func() {}, nil
	}
	return binding.acquire(ctx, heldWriteFor(ctx, binding), nil)
}

func (binding *executionBinding) beginHookExecutorCall(ctx context.Context, captured *heldWrite) (context.Context, func(), error) {
	if binding == nil || !binding.scoped {
		return ctx, func() {}, nil
	}
	start := captured
	if held := heldWriteFor(ctx, binding); held != nil && held.descendsFrom(captured) {
		start = held
	}
	return binding.acquire(ctx, start, captured)
}

func (binding *executionBinding) acquire(ctx context.Context, start, floor *heldWrite) (context.Context, func(), error) {
	owner := start
	for owner != nil {
		if owner.hook != nil {
			if owner.hook.enter() {
				owner.nested.Lock()
				break
			}
			if owner == floor {
				return ctx, nil, errHookExecutorExpired
			}
			if owner = owner.owner; owner != nil {
				owner = owner.owner
			}
			continue
		}
		owner.nested.Lock()
		if !owner.ended.Load() {
			break
		}
		owner.nested.Unlock()
		if owner == floor {
			return ctx, nil, errHookExecutorExpired
		}
		owner = owner.owner
	}
	if owner == nil {
		if !binding.calls.enter() {
			return ctx, nil, errTransactionCallEnded
		}
		binding.writeLock.Lock()
	}
	held := &heldWrite{scope: innermostHeldWrite(ctx), binding: binding, owner: owner}
	if ctx != nil {
		ctx = context.WithValue(ctx, heldWriteKey{}, held)
	}
	return ctx, held.end, nil
}

func (held *heldWrite) end() {
	held.nested.Lock()
	held.ended.Store(true)
	held.nested.Unlock()
	if held.owner != nil {
		held.owner.nested.Unlock()
		if held.owner.hook != nil {
			held.owner.hook.leave()
		}
		return
	}
	held.binding.writeLock.Unlock()
	held.binding.calls.leave()
}

func (binding *executionBinding) closeCalls() {
	if binding != nil {
		binding.calls.close()
	}
}

func openHookScope(ctx context.Context) (context.Context, func()) {
	var scopes []*heldWrite
	seen := map[*executionBinding]bool{}
	for held := innermostHeldWrite(ctx); held != nil; held = held.scope {
		if seen[held.binding] {
			continue
		}
		seen[held.binding] = true
		if held.hook != nil || held.ended.Load() {
			continue
		}
		scope := &heldWrite{scope: innermostHeldWrite(ctx), binding: held.binding, owner: held, hook: (&usageGate{}).init()}
		ctx = context.WithValue(ctx, heldWriteKey{}, scope)
		scopes = append(scopes, scope)
	}
	return ctx, func() {
		for _, scope := range scopes {
			scope.closeHookScope()
		}
	}
}

func (scope *heldWrite) closeHookScope() {
	scope.hook.close()
	scope.nested.Lock()
	scope.ended.Store(true)
	scope.nested.Unlock()
}

func withinHookScope[R any](ctx context.Context, run func(context.Context) (R, error)) (R, error) {
	ctx, closeScope := openHookScope(ctx)
	defer closeScope()
	return run(ctx)
}

func invokeMutationBeforeHooks[A any](ctx context.Context, bindings golem.ApplicationBindings[A], request golem.RuntimeMutationHookRequest, validate func(golem.RuntimeMutationHookRequest) error) (golem.RuntimeMutationHookRequest, error) {
	return withinHookScope(ctx, func(ctx context.Context) (golem.RuntimeMutationHookRequest, error) {
		return golem.RuntimeInvokeMutationBeforeHooks(ctx, bindings, request, validate)
	})
}

func invokeReadBeforeHooks[A any](ctx context.Context, bindings golem.ApplicationBindings[A], request golem.RuntimeReadHookRequest, validate func(golem.RuntimeReadHookRequest) error) (golem.RuntimeReadHookRequest, error) {
	return withinHookScope(ctx, func(ctx context.Context) (golem.RuntimeReadHookRequest, error) {
		return golem.RuntimeInvokeReadBeforeHooks(ctx, bindings, request, validate)
	})
}

func invokeReadResultHooks[A any](ctx context.Context, bindings golem.ApplicationBindings[A], result golem.RuntimeReadHookResult) error {
	_, err := withinHookScope(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, golem.RuntimeInvokeReadResultHooks(ctx, bindings, result)
	})
	return err
}

func invokeHooks[A any](ctx context.Context, bindings golem.ApplicationBindings[A], model golem.ModelID, operation golem.HookOperation, phase golem.HookPhase, payload any) error {
	_, err := withinHookScope(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, golem.RuntimeInvokeHooks(ctx, bindings, model, operation, phase, payload)
	})
	return err
}
