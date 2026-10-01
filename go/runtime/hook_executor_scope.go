package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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
