package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/operationargs"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/subscription"
)

type invalidationStream struct {
	stream *subscription.Stream[any]
	cancel context.CancelFunc
}

func (stream *invalidationStream) Wait(ctx context.Context) error {
	_, err := stream.stream.Recv(ctx)
	if err == nil {
		return events.Failure(events.CodeSubscriptionSourceClosed)
	}
	return err
}
func (stream *invalidationStream) Close() error { stream.cancel(); return stream.stream.Close() }

type callerInvalidation[P any] struct {
	context   context.Context
	principal P
	operation compilerir.CustomOperationContractIR
	request   operationargs.Request
	canonical []byte
	initial   invalidationPlan
	// Rebinds are serialized across model hubs and the periodic check. No actor
	// or database truth survives a check; only the admitted contract is retained.
	mu sync.Mutex
}

// CallerInvalidations opens a terminal, operation-owned dependency sink. Its
// only executable binders are the exact generated bindings admitted at Open.
// It never emits row packets, identities, payload values, or continuation data.
func CallerInvalidations[P, A any](ctx context.Context, caller *Caller[P, A], operationID string, arguments map[string]any) (golem.InvalidationStream, error) {
	if ctx == nil || caller == nil || caller.app == nil || caller.policies == nil || caller.execution == 0 || caller.app.invalidations == nil {
		return nil, fmt.Errorf("P7_INVALIDATION_OPERATION: an admitted caller and registered operation are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app := caller.app
	if app.eventTransport == nil {
		return nil, events.Failure(events.CodeEventConfig)
	}
	operation, request, canonical, err := app.invalidations.prepare(operationID, arguments)
	if err != nil {
		return nil, err
	}
	principal, err := snapshotEventPrincipal(caller.principal, app.snapshotPrincipal)
	if err != nil {
		return nil, err
	}
	fresh, err := app.ForPrincipal(ctx, principal)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	initial, err := app.planInvalidations(ctx, fresh, operation, request, canonical)
	if err != nil {
		return nil, err
	}
	release, err := app.reserveSubscription(principal)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	state := &callerInvalidation[P]{context: lifetime, principal: principal, operation: operation, request: request, canonical: canonical, initial: initial}
	dependents := &eventDependents{}
	var terminal *subscription.Stream[any]
	var releaseOnce sync.Once
	cleanup := func() { releaseOnce.Do(func() { cancel(); release(); dependents.closeAll() }) }
	failed := true
	defer func() {
		if failed {
			cleanup()
		}
	}()
	for index, planned := range initial.members {
		metadata, present := app.eventRegistry.Lookup(planned.dependency.model)
		if !present || validateEventMetadata(app.registry, metadata) != nil {
			return nil, events.Failure(events.CodeEventConfig)
		}
		if planned.judge != nil {
			tree, err := eventDependencyTree(planned.judge)
			if err != nil {
				return nil, events.Failure(events.CodeSubscriptionInvalid)
			}
			if _, err := app.eventSnapshotPlan(planned.dependency.model, metadata, tree, planned.dependency.fields); err != nil {
				return nil, events.Failure(events.CodeSubscriptionInvalid)
			}
		}
		hub, err := app.eventHub(planned.dependency.model)
		if err != nil {
			return nil, err
		}
		key, err := app.eventSubscriberKey(planned.dependency.model, metadata, nil, readplan.Plan{})
		if err != nil {
			return nil, err
		}
		memberIndex := index
		member := &eventDependencyMember{metadata: metadata, watch: planned.dependency.fields, membership: planned.dependency.membership, owner: dependents, physical: planned.historical, judge: planned.judge}
		member.fact = &eventFactDependency{evaluate: func(noticeContext context.Context, member *eventDependencyMember, shared *eventNoticeShared) (bool, error) {
			checkContext, done := subscriptionCheckContext(noticeContext, state.context)
			defer done()
			state.mu.Lock()
			defer state.mu.Unlock()
			fresh, current, err := app.rebindInvalidations(checkContext, state)
			if err != nil {
				return false, err
			}
			planned := current.members[memberIndex]
			if planned.historical {
				return app.historicalFactDependencyChanged(checkContext, fresh, planned.source, shared, planned.facts)
			}
			return app.readableInvalidationChanged(member, shared)
		}}
		stream, err := hub.SubscribeWithState(lifetime, key, member, func(_ any, failure error) {
			dependents.end(failure)
			// Ending any member cancels the whole operation, including setup races.
			cancel()
		})
		if err != nil {
			return nil, err
		}
		dependents.mu.Lock()
		dependents.streams = append(dependents.streams, stream)
		dependents.mu.Unlock()
		if terminal == nil {
			terminal = stream
			dependents.attach(stream)
		}
	}
	if err := lifetime.Err(); err != nil {
		return nil, events.Failure(events.CodeSubscriptionCancelled)
	}
	// The operation has no public parent-model stream: its first private member
	// is only a terminal channel. Every member's evaluator always suppresses data.
	context.AfterFunc(lifetime, cleanup)
	every := app.eventLimits.RevalidateEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	terminal.Recheck(every, func(checkContext context.Context) error {
		derived, done := subscriptionCheckContext(checkContext, lifetime)
		defer done()
		state.mu.Lock()
		defer state.mu.Unlock()
		_, _, err := app.rebindInvalidations(derived, state)
		return err
	})
	failed = false
	app.reportSubscriberWithoutPublisher(ctx)
	return &invalidationStream{stream: terminal, cancel: cancel}, nil
}

func (app *App[P, A]) rebindInvalidations(ctx context.Context, state *callerInvalidation[P]) (*Caller[P, A], invalidationPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, invalidationPlan{}, events.Failure(events.CodeSubscriptionCancelled)
	}
	fresh, err := app.ForPrincipal(ctx, state.principal)
	if err != nil {
		return nil, invalidationPlan{}, events.Failure(events.CodeSubscriptionRevalidation)
	}
	current, err := app.planInvalidations(ctx, fresh, state.operation, state.request, state.canonical)
	if err != nil {
		return nil, invalidationPlan{}, events.Failure(events.CodeSubscriptionResync)
	}
	if current.fingerprint != state.initial.fingerprint || len(current.members) != len(state.initial.members) {
		return nil, invalidationPlan{}, events.Failure(events.CodeSubscriptionResync)
	}
	return fresh, current, nil
}

// Unknown authority never becomes an attributed invalidation. A known true
// row/field domain may require generic revalidation if its narrowing filter is
// unknown; outside/unknown domains remain silent. This is deliberately stricter
// than ordinary event dependencyChanged's conservative unknown != false rule.
func (app *App[P, A]) readableInvalidationChanged(member *eventDependencyMember, shared *eventNoticeShared) (bool, error) {
	if member.judge == nil {
		return false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	// Missing source evidence is an operational failure, independent of the
	// current row's audience. Current nonmembership cannot prove old absence.
	// Reuse the event owner's exact active snapshot inventory validation, not
	// just the encoded state or whichever fields happen to prove today's guard.
	if shared.envelope.Action() == golem.EventUpdated || shared.envelope.Action() == golem.EventDeleted {
		if _, complete := app.eventBeforeImage(policyir.ModelID(member.metadata.ModelID()), shared.envelope.Fact()); !complete {
			return false, events.Failure(events.CodeSubscriptionSourceClosed)
		}
	}
	before, after := (*eventSnapshot)(nil), (*eventSnapshot)(nil)
	watchedChanged := true
	switch shared.envelope.Action() {
	case golem.EventCreated:
		if !member.membership {
			return false, nil
		}
		after = shared.snapshot
	case golem.EventUpdated:
		before = app.beforeSnapshot(shared, policyir.ModelID(member.metadata.ModelID()))
		after = shared.snapshot
		if before != nil {
			watchedChanged = dependencyFieldsDiffer(member.watch, before.evaluator.cells, after)
		}
	case golem.EventDeleted:
		if !member.membership {
			return false, nil
		}
		evaluator, _ := app.dependencyBeforeImage(member, shared.envelope.Fact())
		if evaluator != nil {
			before = &eventSnapshot{found: true, evaluator: evaluator}
		}
	default:
		return false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	if shared.snapshotErr != nil {
		return false, shared.snapshotErr
	}
	return readableInvalidationVerdict(member.judge, before, after, watchedChanged)
}

func readableInvalidationVerdict(judge *eventJudgeSpec, before, after *eventSnapshot, watchedChanged bool) (bool, error) {
	if !watchedChanged && before != nil && after != nil {
		old, current := before.judge(judge), after.judge(judge)
		if old == current && old != deleteUnknown {
			return false, nil
		}
	}
	unknownWithinDomain := false
	for _, snapshot := range []*eventSnapshot{before, after} {
		if snapshot == nil || !snapshot.found || snapshot.evaluator == nil {
			continue
		}
		if snapshot.evaluator.truth(judge.row) != deleteTrue {
			continue
		}
		verdict := snapshot.judge(judge)
		if verdict == deleteTrue {
			return true, nil
		}
		unknownWithinDomain = unknownWithinDomain || verdict == deleteUnknown
	}
	if unknownWithinDomain {
		return false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	return false, nil
}
