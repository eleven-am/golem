package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	DefaultMaxBatchSize uint32 = 256
	DefaultMaxPending   uint32 = 4096
)

var (
	ErrExecutionClosed  = errors.New("GraphQL execution is closed")
	ErrWriteInvalidated = errors.New("computed loaders invalidated by write")
	ErrDispatchActive   = errors.New("computed loader dispatch is already active")
	ErrPendingLimit     = errors.New("computed loader pending-key limit exceeded")
)

// Execution is the isolation boundary for computed loaders. A server creates
// exactly one per GraphQL operation/caller/principal. It intentionally carries
// no globally reusable identity.
type Execution struct {
	mu          sync.Mutex
	closed      bool
	nextLoader  uint64
	invalidator map[uint64]func(error)
}

func NewExecution() *Execution {
	return &Execution{invalidator: map[uint64]func(error){}}
}

func (execution *Execution) register(invalidate func(error)) (uint64, error) {
	if execution == nil {
		return 0, ErrExecutionClosed
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	if execution.closed {
		return 0, ErrExecutionClosed
	}
	execution.nextLoader++
	execution.invalidator[execution.nextLoader] = invalidate
	return execution.nextLoader, nil
}

// InvalidateAfterWrite clears every cache and pending/in-flight waiter in this
// execution. Subsequent reads may queue fresh work; stale in-flight results can
// never repopulate the cache.
func (execution *Execution) InvalidateAfterWrite() {
	execution.invalidate(ErrWriteInvalidated, false)
}

// Close permanently rejects new work and unwinds every waiter. It is safe to
// call more than once during cancellation cleanup.
func (execution *Execution) Close() {
	execution.invalidate(ErrExecutionClosed, true)
}

func (execution *Execution) invalidate(cause error, closeExecution bool) {
	if execution == nil {
		return
	}
	execution.mu.Lock()
	if closeExecution && execution.closed {
		execution.mu.Unlock()
		return
	}
	if closeExecution {
		execution.closed = true
	}
	callbacks := make([]func(error), 0, len(execution.invalidator))
	for _, callback := range execution.invalidator {
		callbacks = append(callbacks, callback)
	}
	execution.mu.Unlock()
	for _, callback := range callbacks {
		callback(cause)
	}
}

// CanonicalArgument is one already-coerced argument value. Value comes from the
// exact GraphQL scalar/input codec, so Decimal, BigInt, JSON numbers, and other
// logical scalars never round-trip through reflection or float64 here.
type CanonicalArgument struct {
	Name  string
	Value json.RawMessage
}

// CanonicalArguments provides the deterministic JSON portion of a computed
// batch key. Names are sorted and duplicate/invalid values fail closed. Value
// bytes must already be canonical output from the GraphQL coercion layer.
func CanonicalArguments(arguments ...CanonicalArgument) (string, error) {
	values := append([]CanonicalArgument(nil), arguments...)
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	var result bytes.Buffer
	result.WriteByte('{')
	for index, argument := range values {
		if argument.Name == "" || index > 0 && argument.Name == values[index-1].Name {
			return "", errors.New("canonical computed arguments require unique non-empty names")
		}
		if !json.Valid(argument.Value) {
			return "", fmt.Errorf("canonical computed argument %q is not valid JSON", argument.Name)
		}
		name, _ := json.Marshal(argument.Name)
		if index != 0 {
			result.WriteByte(',')
		}
		result.Write(name)
		result.WriteByte(':')
		result.Write(argument.Value)
	}
	result.WriteByte('}')
	return result.String(), nil
}

// RequestKey contains every key component beneath the execution boundary. The
// field identity belongs to LoaderConfig; the remaining typed cache key and
// canonical arguments prevent accidental coalescing across argument values.
type RequestKey[K comparable] struct {
	Arguments string
	CacheKey  K
}

type BatchValue[V any] struct {
	Value V
	Err   error
}

type BatchFunc[K comparable, V any] func(context.Context, []RequestKey[K]) (map[RequestKey[K]]BatchValue[V], error)

type LoaderConfig struct {
	FieldID      ir.ExtensionID
	MaxBatchSize uint32
	MaxPending   uint32
}

// Future is a multi-await safe result. Await cancellation never creates a
// goroutine and does not poison other callers waiting for the same key.
type Future[V any] struct {
	done  chan struct{}
	once  sync.Once
	value BatchValue[V]
}

func newFuture[V any]() *Future[V] { return &Future[V]{done: make(chan struct{})} }

func completedFuture[V any](value BatchValue[V]) *Future[V] {
	result := newFuture[V]()
	result.complete(value)
	return result
}

func (future *Future[V]) complete(value BatchValue[V]) {
	if future == nil {
		return
	}
	future.once.Do(func() {
		future.value = value
		close(future.done)
	})
}

func (future *Future[V]) Await(ctx context.Context) (V, error) {
	var zero V
	if future == nil {
		return zero, errors.New("nil computed future")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-future.done:
		return future.value.Value, future.value.Err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

type pendingValue[V any] struct {
	waiters []*Future[V]
}

// Loader is bound to one Execution and one computed field. It has no timer and
// starts no goroutine: the GraphQL executor queues selections, calls Dispatch,
// and then awaits their futures.
type Loader[K comparable, V any] struct {
	execution  *Execution
	fieldID    ir.ExtensionID
	batch      BatchFunc[K, V]
	maxBatch   int
	maxPending int

	mu          sync.Mutex
	closed      bool
	generation  uint64
	dispatching bool
	pending     map[RequestKey[K]]*pendingValue[V]
	order       []RequestKey[K]
	inflight    map[RequestKey[K]]*pendingValue[V]
	cache       map[RequestKey[K]]BatchValue[V]
}

func NewLoader[K comparable, V any](execution *Execution, config LoaderConfig, batch BatchFunc[K, V]) (*Loader[K, V], error) {
	if execution == nil {
		return nil, ErrExecutionClosed
	}
	if config.FieldID == "" {
		return nil, errors.New("computed loader requires a field identity")
	}
	if batch == nil {
		return nil, errors.New("computed loader requires a batch function")
	}
	if config.MaxBatchSize == 0 {
		config.MaxBatchSize = DefaultMaxBatchSize
	}
	if config.MaxPending == 0 {
		config.MaxPending = DefaultMaxPending
	}
	if config.MaxBatchSize > HardMaxBatchSize || config.MaxPending > DefaultMaxPending || config.MaxBatchSize > config.MaxPending {
		return nil, fmt.Errorf("computed loader limits require 1 <= batch <= pending <= %d", DefaultMaxPending)
	}
	loader := &Loader[K, V]{
		execution: execution, fieldID: config.FieldID, batch: batch,
		maxBatch: int(config.MaxBatchSize), maxPending: int(config.MaxPending),
		pending: map[RequestKey[K]]*pendingValue[V]{}, inflight: map[RequestKey[K]]*pendingValue[V]{}, cache: map[RequestKey[K]]BatchValue[V]{},
	}
	if _, err := execution.register(loader.invalidate); err != nil {
		return nil, err
	}
	return loader, nil
}

// Queue returns the cached/coalesced future for a full request key. It never
// calls the batch function; Dispatch is the only execution point.
func (loader *Loader[K, V]) Queue(key RequestKey[K]) (*Future[V], error) {
	if loader == nil {
		return nil, ErrExecutionClosed
	}
	loader.mu.Lock()
	defer loader.mu.Unlock()
	if loader.closed {
		return nil, ErrExecutionClosed
	}
	if value, exists := loader.cache[key]; exists {
		return completedFuture(value), nil
	}
	future := newFuture[V]()
	if pending := loader.pending[key]; pending != nil {
		pending.waiters = append(pending.waiters, future)
		return future, nil
	}
	if pending := loader.inflight[key]; pending != nil {
		pending.waiters = append(pending.waiters, future)
		return future, nil
	}
	if len(loader.pending)+len(loader.inflight) >= loader.maxPending {
		return nil, ErrPendingLimit
	}
	loader.pending[key] = &pendingValue[V]{waiters: []*Future[V]{future}}
	loader.order = append(loader.order, key)
	return future, nil
}

func (loader *Loader[K, V]) Pending() int {
	if loader == nil {
		return 0
	}
	loader.mu.Lock()
	defer loader.mu.Unlock()
	return len(loader.pending)
}

// Dispatch executes at most MaxBatchSize unique keys. Duplicate queued keys
// share one batch entry. The returned map must contain every requested key and
// no other key; otherwise the complete batch fails closed.
func (loader *Loader[K, V]) Dispatch(ctx context.Context) error {
	if loader == nil {
		return ErrExecutionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	loader.mu.Lock()
	if loader.closed {
		loader.mu.Unlock()
		return ErrExecutionClosed
	}
	if loader.dispatching {
		loader.mu.Unlock()
		return ErrDispatchActive
	}
	count := len(loader.order)
	if count > loader.maxBatch {
		count = loader.maxBatch
	}
	if count == 0 {
		loader.mu.Unlock()
		return nil
	}
	keys := append([]RequestKey[K](nil), loader.order[:count]...)
	loader.order = append([]RequestKey[K](nil), loader.order[count:]...)
	for _, key := range keys {
		loader.inflight[key] = loader.pending[key]
		delete(loader.pending, key)
	}
	generation := loader.generation
	loader.dispatching = true
	loader.mu.Unlock()

	values, batchErr := loader.batch(ctx, keys)
	if batchErr == nil {
		batchErr = exactBatchKeys(keys, values)
	}
	if batchErr == nil {
		batchErr = ctx.Err()
	}

	loader.mu.Lock()
	loader.dispatching = false
	stale := generation != loader.generation || loader.closed
	entries := make(map[RequestKey[K]]*pendingValue[V], len(keys))
	for _, key := range keys {
		entries[key] = loader.inflight[key]
		delete(loader.inflight, key)
		if !stale && batchErr == nil {
			loader.cache[key] = values[key]
		}
	}
	loader.mu.Unlock()

	if stale {
		return ErrWriteInvalidated
	}
	if batchErr != nil {
		for _, entry := range entries {
			completeEntry(entry, BatchValue[V]{Err: batchErr})
		}
		return batchErr
	}
	for key, entry := range entries {
		completeEntry(entry, values[key])
	}
	return nil
}

func exactBatchKeys[K comparable, V any](requested []RequestKey[K], values map[RequestKey[K]]BatchValue[V]) error {
	if len(values) != len(requested) {
		return fmt.Errorf("computed batch returned %d keys for %d requests", len(values), len(requested))
	}
	for _, key := range requested {
		if _, exists := values[key]; !exists {
			return fmt.Errorf("computed batch omitted a requested key")
		}
	}
	return nil
}

func (loader *Loader[K, V]) invalidate(cause error) {
	loader.mu.Lock()
	loader.generation++
	if errors.Is(cause, ErrExecutionClosed) {
		loader.closed = true
	}
	entries := make([]*pendingValue[V], 0, len(loader.pending)+len(loader.inflight))
	for _, entry := range loader.pending {
		entries = append(entries, entry)
	}
	for _, entry := range loader.inflight {
		entries = append(entries, entry)
	}
	loader.pending = map[RequestKey[K]]*pendingValue[V]{}
	loader.inflight = map[RequestKey[K]]*pendingValue[V]{}
	loader.order = nil
	loader.cache = map[RequestKey[K]]BatchValue[V]{}
	loader.mu.Unlock()
	for _, entry := range entries {
		completeEntry(entry, BatchValue[V]{Err: cause})
	}
}

func completeEntry[V any](entry *pendingValue[V], value BatchValue[V]) {
	if entry == nil {
		return
	}
	for _, waiter := range entry.waiters {
		waiter.complete(value)
	}
}
