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
	usage  *usageGate
	writes *sync.Mutex
}

type heldHookWrite struct {
	parent *heldHookWrite
	held   *sync.Mutex
	child  *sync.Mutex
}

type heldHookWriteKey struct{}

func heldHookWrites(ctx context.Context) *heldHookWrite {
	if ctx == nil {
		return nil
	}
	held, _ := ctx.Value(heldHookWriteKey{}).(*heldHookWrite)
	return held
}

func newHookExecutorGate(ctx context.Context, binding *executionBinding) *hookExecutorGate {
	writes := &binding.hookWrites
	if held := heldHookWrites(ctx); held != nil {
		writes = held.child
	}
	return &hookExecutorGate{usage: (&usageGate{}).init(), writes: writes}
}

func (gate *hookExecutorGate) writeLock(ctx context.Context) *sync.Mutex {
	held := heldHookWrites(ctx)
	for current := held; current != nil; current = current.parent {
		if current.held == gate.writes {
			return held.child
		}
	}
	return gate.writes
}

func withinHookWriteLock[R any](ctx context.Context, gate *hookExecutorGate, run func(context.Context) (R, error)) (R, error) {
	lock := gate.writeLock(ctx)
	lock.Lock()
	defer lock.Unlock()
	return run(context.WithValue(ctx, heldHookWriteKey{}, &heldHookWrite{parent: heldHookWrites(ctx), held: lock, child: &sync.Mutex{}}))
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
