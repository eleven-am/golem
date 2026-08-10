package observe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const (
	DefaultQueueCapacity = 1024
	MaximumQueueCapacity = 65536
)

type DispatcherConfig struct {
	QueueCapacity int
}

// Dispatcher is a nonblocking, bounded, single-worker Observer. Shutdown
// closes intake immediately and drops records that have not begun delivery.
type Dispatcher struct {
	target  Observer
	queue   chan Observation
	stop    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
	dropped atomic.Uint64
}

func NewDispatcher(target Observer, config DispatcherConfig) (*Dispatcher, error) {
	if target == nil {
		return nil, errors.New("GOLEM_OBSERVE_CONFIG: target observer is required")
	}
	capacity := config.QueueCapacity
	if capacity == 0 {
		capacity = DefaultQueueCapacity
	}
	if capacity < 1 || capacity > MaximumQueueCapacity {
		return nil, fmt.Errorf("GOLEM_OBSERVE_CONFIG: queue capacity must be between 1 and %d", MaximumQueueCapacity)
	}
	dispatcher := &Dispatcher{
		target: target,
		queue:  make(chan Observation, capacity),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go dispatcher.run()
	return dispatcher, nil
}

func (dispatcher *Dispatcher) ObserveGolem(_ context.Context, value Observation) {
	if dispatcher == nil || !valid(value.value) {
		return
	}
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if dispatcher.closed {
		dispatcher.dropped.Add(1)
		return
	}
	select {
	case dispatcher.queue <- value:
	default:
		dispatcher.dropped.Add(1)
	}
}

func (dispatcher *Dispatcher) Dropped() uint64 {
	if dispatcher == nil {
		return 0
	}
	return dispatcher.dropped.Load()
}

func (dispatcher *Dispatcher) Shutdown(ctx context.Context) error {
	if dispatcher == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("GOLEM_OBSERVE_SHUTDOWN: context is required")
	}
	dispatcher.mu.Lock()
	if !dispatcher.closed {
		dispatcher.closed = true
		close(dispatcher.stop)
		dispatcher.dropPending(nil)
	}
	dispatcher.mu.Unlock()
	select {
	case <-dispatcher.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (dispatcher *Dispatcher) run() {
	defer close(dispatcher.done)
	for {
		select {
		case <-dispatcher.stop:
			dispatcher.dropPending(nil)
			return
		default:
		}
		select {
		case <-dispatcher.stop:
			dispatcher.dropPending(nil)
			return
		case value := <-dispatcher.queue:
			dispatcher.mu.Lock()
			closed := dispatcher.closed
			dispatcher.mu.Unlock()
			if closed {
				dispatcher.dropPending(&value)
				return
			}
			dispatcher.deliver(value)
		}
	}
}

func (dispatcher *Dispatcher) deliver(value Observation) {
	ctx, cancel := context.WithTimeout(context.Background(), observationDeadline)
	defer cancel()
	defer func() { _ = recover() }()
	dispatcher.target.ObserveGolem(ctx, value)
}

func (dispatcher *Dispatcher) dropPending(first *Observation) {
	if first != nil {
		dispatcher.dropped.Add(1)
	}
	for {
		select {
		case <-dispatcher.queue:
			dispatcher.dropped.Add(1)
		default:
			return
		}
	}
}
