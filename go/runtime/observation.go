package runtime

import (
	"context"
	"errors"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	internalvalue "github.com/eleven-am/golem/go/internal/observation"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/observe"
)

func beginObservation[P, A any](ctx context.Context, app *App[P, A], model golem.ModelID, kind observe.Kind, operation observe.Operation) (context.Context, *observeexec.Span) {
	return beginExecutionObservation(ctx, app, nil, model, kind, operation)
}

func beginExecutionObservation[P, A any](ctx context.Context, app *App[P, A], execution *executionBinding, model golem.ModelID, kind observe.Kind, operation observe.Operation) (context.Context, *observeexec.Span) {
	return beginExecutionObservationPhase(ctx, app, execution, model, kind, operation, observe.PhaseFinish)
}

func beginExecutionObservationPhase[P, A any](ctx context.Context, app *App[P, A], execution *executionBinding, model golem.ModelID, kind observe.Kind, operation observe.Operation, phase observe.Phase) (context.Context, *observeexec.Span) {
	if app == nil {
		return ctx, nil
	}
	observer := app.observer
	if execution != nil && execution.observer != nil {
		observer = execution.observer
	}
	return observeexec.Begin(ctx, observer, app.eventProvider, model, kind, operation, phase)
}

func beginDeferredExecutionObservation[P, A any](ctx context.Context, app *App[P, A], execution *executionBinding, model golem.ModelID, kind observe.Kind, operation observe.Operation) (context.Context, *observeexec.Span, *observeexec.DeferredObserver) {
	if app == nil {
		return ctx, nil, nil
	}
	target := app.observer
	if execution != nil && execution.observer != nil {
		target = execution.observer
	}
	deferred := observeexec.NewDeferredObserver(target)
	ctx, span := observeexec.Begin(ctx, deferred, app.eventProvider, model, kind, operation, observe.PhaseFinish)
	return ctx, span, deferred
}

func finishDeferredObservation(span *observeexec.Span, deferred *observeexec.DeferredObserver, err error) {
	finishObservation(span, err)
	deferred.Flush()
}

func finishObservation(span *observeexec.Span, err error) {
	outcome, reason := observationResult(err)
	observeexec.Finish(span, outcome, reason)
}

func readObservationOperation(operation golem.ReadOperation) observe.Operation {
	switch operation {
	case golem.ReadFindUnique:
		return observe.OperationReadFindUnique
	case golem.ReadFindFirst:
		return observe.OperationReadFindFirst
	case golem.ReadCount:
		return observe.OperationReadCount
	default:
		return observe.OperationReadFindMany
	}
}

func analyticsObservationOperation(operation golem.AnalyticsOperation) observe.Operation {
	switch operation {
	case golem.AnalyticsGroupBy:
		return observe.OperationAnalyticsGroupBy
	case golem.AnalyticsRelationGroupBy:
		return observe.OperationAnalyticsRelationGroupBy
	default:
		return observe.OperationAnalyticsAggregate
	}
}

func mutationObservationOperation(operation mutationir.Operation) observe.Operation {
	switch operation {
	case mutationir.Create:
		return observe.OperationMutationCreate
	case mutationir.Update:
		return observe.OperationMutationUpdate
	case mutationir.Upsert:
		return observe.OperationMutationUpsert
	case mutationir.Delete:
		return observe.OperationMutationDelete
	case mutationir.UpdateMany:
		return observe.OperationMutationUpdateMany
	case mutationir.DeleteMany:
		return observe.OperationMutationDeleteMany
	case mutationir.Connect:
		return observe.OperationMutationConnect
	case mutationir.Disconnect:
		return observe.OperationMutationDisconnect
	case mutationir.SetRelation:
		return observe.OperationMutationSetRelation
	default:
		return observe.OperationMutationUpdate
	}
}

func hookObservationOperation(operation golem.HookOperation) observe.Operation {
	switch operation {
	case golem.HookFindOne:
		return observe.OperationHookFindOne
	case golem.HookFindFirst:
		return observe.OperationHookFindFirst
	case golem.HookFindMany:
		return observe.OperationHookFindMany
	case golem.HookCreate:
		return observe.OperationHookCreate
	case golem.HookUpdate:
		return observe.OperationHookUpdate
	case golem.HookDelete:
		return observe.OperationHookDelete
	case golem.HookUpdateMany:
		return observe.OperationHookUpdateMany
	case golem.HookDeleteMany:
		return observe.OperationHookDeleteMany
	default:
		panic("golem: unsupported hook observation operation")
	}
}

func hookObservationPhase(phase golem.HookPhase) observe.Phase {
	switch phase {
	case golem.HookBefore:
		return observe.PhaseBefore
	case golem.HookAfterCommit:
		return observe.PhaseAfterCommit
	default:
		return observe.PhaseAfter
	}
}

type unifiedEventObserver struct {
	observer observe.Observer
	provider golem.Provider
}

func adaptEventObserver(observer observe.Observer, provider golem.Provider) events.Observer {
	if observer == nil {
		return nil
	}
	return unifiedEventObserver{observer: observer, provider: provider}
}

func (adapter unifiedEventObserver) ObserveEvent(_ context.Context, event events.Observation) {
	kind, operation, phase := mapEventObservation(event.Kind())
	statementCount := 0
	if event.Kind() == events.ObservationDepthPending {
		// One provider conditional-aggregate statement produces the pending,
		// blocked, and retired snapshot. Charge it once to the first closed
		// status record so aggregation never fabricates three statements.
		statementCount = 1
	}
	outcome := observe.OutcomeFailure
	switch event.Outcome() {
	case events.OutcomeSuccess:
		outcome = observe.OutcomeSuccess
	case events.OutcomeSuppressed:
		outcome = observe.OutcomeSuppressed
	case events.OutcomeCancelled:
		outcome = observe.OutcomeCancelled
	}
	reason := observe.ReasonNone
	switch event.SuppressionReason() {
	case events.SuppressionFiltered, events.SuppressionDeleteFiltered:
		reason = observe.ReasonFiltered
	case events.SuppressionUnauthorized:
		reason = observe.ReasonAuthorization
	case events.SuppressionDeletionUnverifiable:
		reason = observe.ReasonDeletionUnverifiable
	case events.SuppressionEntityAbsent:
		reason = observe.ReasonEntityAbsent
	case events.SuppressionCorrelatedGolemWrite:
		reason = observe.ReasonCorrelatedGolemWrite
	}
	internalvalue.Emit(adapter.observer, internalvalue.Value{
		KindValue:           string(kind),
		PhaseValue:          string(phase),
		OutcomeValue:        string(outcome),
		ReasonValue:         string(reason),
		ProviderValue:       adapter.provider,
		ModelIDValue:        event.ModelID(),
		OperationValue:      string(operation),
		DurationValue:       event.Duration(),
		StatementCountValue: statementCount,
		AttemptValue:        event.Attempt(),
		QueueDepthValue:     event.QueueDepth(),
		QueueLimitValue:     event.QueueLimit(),
		AggregateCountValue: event.AggregateCount(),
	})
}

func mapEventObservation(kind events.ObservationKind) (observe.Kind, observe.Operation, observe.Phase) {
	switch kind {
	case events.ObservationPublisherClaim:
		return observe.KindEvent, observe.OperationEventPublisherClaim, observe.PhaseClaim
	case events.ObservationPublisherAttempt:
		return observe.KindEvent, observe.OperationEventPublisherAttempt, observe.PhaseAttempt
	case events.ObservationPublisherAck:
		return observe.KindEvent, observe.OperationEventPublisherAcknowledge, observe.PhaseAcknowledge
	case events.ObservationPublisherRetry:
		return observe.KindEvent, observe.OperationEventPublisherRetry, observe.PhaseRetry
	case events.ObservationPublisherBlock:
		return observe.KindEvent, observe.OperationEventPublisherBlock, observe.PhaseFinish
	case events.ObservationDepthPending:
		return observe.KindEvent, observe.OperationEventDepthPending, observe.PhaseFinish
	case events.ObservationDepthBlocked:
		return observe.KindEvent, observe.OperationEventDepthBlocked, observe.PhaseFinish
	case events.ObservationDepthRetired:
		return observe.KindEvent, observe.OperationEventDepthRetired, observe.PhaseFinish
	case events.ObservationRetention:
		return observe.KindEvent, observe.OperationEventRetention, observe.PhaseFinish
	case events.ObservationTransportReceive:
		return observe.KindEvent, observe.OperationEventTransportReceive, observe.PhaseFinish
	case events.ObservationTransportReconnect:
		return observe.KindEvent, observe.OperationEventTransportReconnect, observe.PhaseRetry
	case events.ObservationHubMembership:
		return observe.KindSubscription, observe.OperationSubscriptionMembership, observe.PhaseFinish
	case events.ObservationEvaluation:
		return observe.KindSubscription, observe.OperationSubscriptionEvaluation, observe.PhaseFinish
	case events.ObservationDelivery:
		return observe.KindSubscription, observe.OperationSubscriptionDelivery, observe.PhaseDeliver
	case events.ObservationSuppression:
		return observe.KindSubscription, observe.OperationSubscriptionSuppression, observe.PhaseSuppress
	case events.ObservationOverflow:
		return observe.KindSubscription, observe.OperationSubscriptionOverflow, observe.PhaseOverflow
	case events.ObservationCancellation, events.ObservationLifecycleFailure:
		return observe.KindSubscription, observe.OperationSubscriptionCancellation, observe.PhaseCancel
	case events.ObservationCDCReceive:
		return observe.KindCDC, observe.OperationCDCReceive, observe.PhaseFinish
	case events.ObservationCDCAck:
		return observe.KindCDC, observe.OperationCDCAcknowledge, observe.PhaseAcknowledge
	default:
		return observe.KindEvent, observe.OperationEventPublisherAttempt, observe.PhaseFinish
	}
}

func observationResult(err error) (observe.Outcome, observe.Reason) {
	if err == nil {
		return observe.OutcomeSuccess, observe.ReasonNone
	}
	if errors.Is(err, context.Canceled) {
		return observe.OutcomeCancelled, observe.ReasonNone
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return observe.OutcomeCancelled, observe.ReasonTimeout
	}
	var failure *golem.Error
	if errors.As(err, &failure) {
		switch failure.Code {
		case golem.CodeForbidden, golem.CodeUnauthenticated:
			return observe.OutcomeRefused, observe.ReasonAuthorization
		case golem.CodeNotFound:
			return observe.OutcomeRefused, observe.ReasonNotFound
		case golem.CodeConflict:
			return observe.OutcomeRefused, observe.ReasonConflict
		case golem.CodeBadUserInput:
			return observe.OutcomeRefused, observe.ReasonInvalidInput
		}
	}
	return observe.OutcomeFailure, observe.ReasonProvider
}
