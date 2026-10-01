package runtime

import (
	"context"
	"errors"
	"sync"
)

var errHookExecutorExpired = errors.New("P4_RUNTIME_HOOK_EXECUTOR: executor used after its hook returned")

var errTransactionWriteEnded = errors.New("P4_RUNTIME_TRANSACTION: transaction write after its callback returned")

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
	ended   bool
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

func newHookExecutorGate(ctx context.Context, binding *executionBinding) *hookExecutorGate {
	return &hookExecutorGate{usage: (&usageGate{}).init(), held: heldWriteFor(ctx, binding)}
}

func (binding *executionBinding) lockWrites(ctx context.Context, within *heldWrite) (context.Context, func()) {
	if binding == nil || !binding.scoped {
		return ctx, func() {}
	}
	if held := heldWriteFor(ctx, binding); held != nil {
		within = held
	}
	owner := within
	for ; owner != nil; owner = owner.owner {
		owner.nested.Lock()
		if !owner.ended {
			break
		}
		owner.nested.Unlock()
	}
	if owner == nil {
		binding.writeLock.Lock()
	}
	held := &heldWrite{scope: innermostHeldWrite(ctx), binding: binding, owner: owner}
	if ctx != nil {
		ctx = context.WithValue(ctx, heldWriteKey{}, held)
	}
	return ctx, held.end
}

func (held *heldWrite) end() {
	held.nested.Lock()
	held.ended = true
	held.nested.Unlock()
	if held.owner == nil {
		held.binding.writeLock.Unlock()
		return
	}
	held.owner.nested.Unlock()
}

func (binding *executionBinding) enterWrite() bool {
	if binding == nil {
		return true
	}
	return binding.writes.enter()
}

func (binding *executionBinding) leaveWrite() {
	if binding != nil {
		binding.writes.leave()
	}
}

func (binding *executionBinding) closeWrites() {
	if binding != nil {
		binding.writes.close()
	}
}
