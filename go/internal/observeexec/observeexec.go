// Package observeexec owns request-local observation spans. It is shared by
// runtime and GraphQL so nested operations have one deterministic statement
// count without exposing a tracing provider or SQL text through public APIs.
package observeexec

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/observation"
	"github.com/eleven-am/golem/go/observe"
)

type spanContextKey struct{}

type Span struct {
	observer  observe.Observer
	provider  golem.Provider
	model     golem.ModelID
	kind      observe.Kind
	operation observe.Operation
	phase     observe.Phase
	started   time.Time
	parent    *Span

	statements atomic.Int64
	attempt    atomic.Int64
	aggregate  atomic.Int64
	finishOnce sync.Once
}

// DeferredObserver buffers already-validated immutable observations while an
// application transaction owns a connection. Flush is called only after the
// transaction has committed or rolled back and the execution binding closed.
type DeferredObserver struct {
	target observe.Observer
	mu     sync.Mutex
	values []internalvalue.Value
	closed bool
}

const DeferredObservationLimit = 1024

var droppedObservations atomic.Uint64

func DroppedObservationCount() uint64 { return droppedObservations.Load() }

func NewDeferredObserver(target observe.Observer) *DeferredObserver {
	return &DeferredObserver{target: target}
}

func (observer *DeferredObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	if observer == nil || observer.target == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.closed {
		return
	}
	if len(observer.values) >= DeferredObservationLimit {
		droppedObservations.Add(1)
		return
	}
	observer.values = append(observer.values, internalvalue.Value{
		KindValue:           string(value.Kind()),
		PhaseValue:          string(value.Phase()),
		OutcomeValue:        string(value.Outcome()),
		ReasonValue:         string(value.Reason()),
		ProviderValue:       value.Provider(),
		ModelIDValue:        value.ModelID(),
		OperationValue:      string(value.Operation()),
		DurationValue:       value.Duration(),
		StatementCountValue: value.StatementCount(),
		AttemptValue:        value.Attempt(),
		QueueDepthValue:     len(observer.values) + 1,
		QueueLimitValue:     DeferredObservationLimit,
		AggregateCountValue: value.AggregateCount(),
		QueueTypeValue:      value.QueueType(),
	})
}

func (observer *DeferredObserver) Flush() {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	values := append([]internalvalue.Value(nil), observer.values...)
	observer.values = nil
	observer.closed = true
	target := observer.target
	observer.mu.Unlock()
	if target == nil {
		return
	}
	for _, value := range values {
		internalvalue.Emit(target, value)
	}
}

func Begin(ctx context.Context, observer observe.Observer, provider golem.Provider, model golem.ModelID, kind observe.Kind, operation observe.Operation, phase observe.Phase) (context.Context, *Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	span := &Span{observer: observer, provider: provider, model: model, kind: kind, operation: operation, phase: phase, started: time.Now()}
	if parent, ok := ctx.Value(spanContextKey{}).(*Span); ok {
		span.parent = parent
	}
	return context.WithValue(ctx, spanContextKey{}, span), span
}

func BeginChild(ctx context.Context, model golem.ModelID, kind observe.Kind, operation observe.Operation, phase observe.Phase) (context.Context, *Span) {
	if ctx == nil {
		return ctx, nil
	}
	parent, ok := ctx.Value(spanContextKey{}).(*Span)
	if !ok || parent == nil {
		return ctx, nil
	}
	return Begin(ctx, parent.observer, parent.provider, model, kind, operation, phase)
}

// RecordStatement counts one application SQL Query/Exec at the point it is
// handed to the provider, including calls that return provider errors. A child
// operation contributes once to every ancestor operation. Additional spans are
// used by transaction bindings whose callback API cannot replace caller-owned
// contexts; duplicates are suppressed.
func RecordStatement(ctx context.Context, additional ...*Span) {
	seen := make(map[*Span]struct{}, len(additional)+4)
	if ctx != nil {
		if span, ok := ctx.Value(spanContextKey{}).(*Span); ok {
			incrementChain(span, seen)
		}
	}
	for _, span := range additional {
		incrementChain(span, seen)
	}
}

func incrementChain(span *Span, seen map[*Span]struct{}) {
	for current := span; current != nil; current = current.parent {
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		incrementBounded(&current.statements)
	}
}

func incrementBounded(value *atomic.Int64) {
	const maximum = int64(^uint32(0) >> 1)
	for {
		current := value.Load()
		if current >= maximum || value.CompareAndSwap(current, current+1) {
			return
		}
	}
}

func (span *Span) SetAttempt(value int) {
	if span != nil && value >= 0 {
		span.attempt.Store(int64(value))
	}
}

func (span *Span) SetAggregateCount(value int64) {
	if span != nil && value >= 0 {
		span.aggregate.Store(value)
	}
}

func (span *Span) SetOperation(value observe.Operation) {
	if span != nil {
		span.operation = value
	}
}

func (span *Span) StatementCount() int {
	if span == nil {
		return 0
	}
	return int(span.statements.Load())
}

func Finish(span *Span, outcome observe.Outcome, reason observe.Reason) {
	if span == nil {
		return
	}
	span.finishOnce.Do(func() {
		internalvalue.Emit(span.observer, internalvalue.Value{
			KindValue:           string(span.kind),
			PhaseValue:          string(span.phase),
			OutcomeValue:        string(outcome),
			ReasonValue:         string(reason),
			ProviderValue:       span.provider,
			ModelIDValue:        span.model,
			OperationValue:      string(span.operation),
			DurationValue:       time.Since(span.started),
			StatementCountValue: int(span.statements.Load()),
			AttemptValue:        int(span.attempt.Load()),
			AggregateCountValue: span.aggregate.Load(),
		})
	})
}

// EmitQueue emits one validated payload-free durable job lifecycle record.
func EmitQueue(observer observe.Observer, provider golem.Provider, jobType string, phase observe.Phase, outcome observe.Outcome, reason observe.Reason, attempt int, duration time.Duration) {
	internalvalue.Emit(observer, internalvalue.Value{
		KindValue: string(observe.KindQueue), PhaseValue: string(phase), OutcomeValue: string(outcome),
		ReasonValue: string(reason), ProviderValue: provider, OperationValue: string(observe.OperationQueueExecute),
		AttemptValue: attempt, DurationValue: duration, QueueTypeValue: jobType,
	})
}
