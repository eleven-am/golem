package observeexec

import (
	"context"
	"sync/atomic"
	"testing"

	internalvalue "github.com/eleven-am/golem/go/internal/observation"
	"github.com/eleven-am/golem/go/observe"
)

type countObserver struct{ count atomic.Int64 }

func (observer *countObserver) ObserveGolem(context.Context, observe.Observation) {
	observer.count.Add(1)
}

func TestDeferredObserverIsCappedAndCountsDrops(t *testing.T) {
	target := &countObserver{}
	deferred := NewDeferredObserver(target)
	before := DroppedObservationCount()
	value := internalvalue.Value{KindValue: string(observe.KindRead), PhaseValue: string(observe.PhaseFinish), OutcomeValue: string(observe.OutcomeSuccess), OperationValue: string(observe.OperationReadFindMany)}
	for index := 0; index < DeferredObservationLimit+73; index++ {
		internalvalue.Emit(deferred, value)
	}
	deferred.mu.Lock()
	retained := len(deferred.values)
	deferred.mu.Unlock()
	if retained != DeferredObservationLimit {
		t.Fatalf("retained=%d want=%d", retained, DeferredObservationLimit)
	}
	if dropped := DroppedObservationCount() - before; dropped != 73 {
		t.Fatalf("dropped=%d want=73", dropped)
	}
	deferred.Flush()
	if got := target.count.Load(); got != DeferredObservationLimit {
		t.Fatalf("delivered=%d want=%d", got, DeferredObservationLimit)
	}
	internalvalue.Emit(deferred, value)
	deferred.Flush()
	if got := target.count.Load(); got != DeferredObservationLimit {
		t.Fatalf("closed deferred observer delivered again: %d", got)
	}
}

func TestStatementCountsAggregateOnceThroughNestedSpans(t *testing.T) {
	target := &countObserver{}
	ctx, root := Begin(context.Background(), target, "", [16]byte{}, observe.KindGraphQL, observe.OperationGraphQLQuery, observe.PhaseFinish)
	ctx, child := BeginChild(ctx, [16]byte{}, observe.KindRead, observe.OperationReadFindMany, observe.PhaseFinish)
	RecordStatement(ctx, root)
	if child.StatementCount() != 1 || root.StatementCount() != 1 {
		t.Fatalf("child=%d root=%d", child.StatementCount(), root.StatementCount())
	}
}
