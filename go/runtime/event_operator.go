package runtime

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

type runtimeEventOperator struct {
	coordinator eventprovider.Coordinator
	audit       events.OperatorAudit
	observer    events.Observer
}

func newRuntimeEventOperator(coordinator eventprovider.Coordinator, audit events.OperatorAudit, observer events.Observer) (events.Operator, error) {
	if coordinator == nil || audit == nil {
		return nil, events.Failure(events.CodeEventConfig)
	}
	return &runtimeEventOperator{coordinator: coordinator, audit: audit, observer: observer}, nil
}

func (operator *runtimeEventOperator) Inspect(ctx context.Context, causation golem.CausationID) (events.Delivery, error) {
	if !validEventOperationContext(ctx) || causation == (golem.CausationID{}) {
		return nil, events.Failure(events.CodeEventSourceClosed)
	}
	state, err := operator.coordinator.Inspect(ctx, eventCausationText(causation))
	if err != nil {
		return nil, events.Failure(events.CodeEventSourceClosed)
	}
	parsed, err := parseEventCausation(state.CausationID)
	if err != nil || parsed != causation {
		return nil, events.Failure(events.CodeEventSourceClosed)
	}
	delivery, err := events.RuntimeDelivery(
		parsed,
		events.DeliveryStatus(state.Status),
		state.AttemptCount,
		events.DeliveryFailureCode(state.LastFailureCode),
		state.ImmutableFactRows,
		state.UpdatedAt,
	)
	if err != nil {
		return nil, events.Failure(events.CodeEventSourceClosed)
	}
	return delivery, nil
}

func (operator *runtimeEventOperator) Resume(ctx context.Context, causation golem.CausationID) error {
	return operator.transition(ctx, events.OperatorAuditResume, causation, operator.coordinator.Resume)
}

func (operator *runtimeEventOperator) Retire(ctx context.Context, causation golem.CausationID) error {
	return operator.transition(ctx, events.OperatorAuditRetire, causation, operator.coordinator.Retire)
}

func (operator *runtimeEventOperator) transition(ctx context.Context, action events.OperatorAuditAction, causation golem.CausationID, apply func(context.Context, string) (bool, error)) error {
	outcome := events.OperatorAuditFailed
	defer func() {
		events.ReportOperatorAudit(operator.audit, ctx, events.RuntimeOperatorAuditRecord(action, outcome, causation, 0, 0))
	}()
	if !validEventOperationContext(ctx) || causation == (golem.CausationID{}) {
		outcome = events.OperatorAuditRejected
		return events.Failure(events.CodeEventSourceClosed)
	}
	changed, err := apply(ctx, eventCausationText(causation))
	if err != nil {
		return events.Failure(events.CodeEventSourceClosed)
	}
	if !changed {
		outcome = events.OperatorAuditRejected
		return events.Failure(events.CodeEventSourceClosed)
	}
	outcome = events.OperatorAuditSucceeded
	return nil
}

func (operator *runtimeEventOperator) RunRetention(ctx context.Context, policy events.RetentionPolicy) (events.RetentionResult, error) {
	started := time.Now()
	outcome := events.OperatorAuditFailed
	causations, facts := 0, 0
	defer func() {
		events.ReportOperatorAudit(operator.audit, ctx, events.RuntimeOperatorAuditRecord(events.OperatorAuditRetention, outcome, golem.CausationID{}, causations, facts))
		observerOutcome := events.OutcomeFailure
		if outcome == events.OperatorAuditSucceeded {
			observerOutcome = events.OutcomeSuccess
		}
		events.Observe(operator.observer, ctx, golem.ModelID{}, "", events.ObservationRetention, observerOutcome, "", 0, 0, policy.MaxRows(), time.Since(started), int64(facts))
	}()
	if !validEventOperationContext(ctx) || policy.OlderThan().IsZero() || policy.MaxRows() <= 0 {
		outcome = events.OperatorAuditRejected
		return events.RetentionResult{}, events.Failure(events.CodeEventSourceClosed)
	}
	result, err := operator.coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: policy.OlderThan().UTC().Truncate(time.Microsecond), MaxRows: policy.MaxRows()})
	if err != nil || result.Causations < 0 || result.Facts < 0 {
		return events.RetentionResult{}, events.Failure(events.CodeEventSourceClosed)
	}
	causations, facts = result.Causations, result.Facts
	outcome = events.OperatorAuditSucceeded
	return events.NewRetentionResult(causations, facts), nil
}

func validEventOperationContext(ctx context.Context) bool {
	return ctx != nil && ctx.Err() == nil
}

func eventCausationText(causation golem.CausationID) string {
	return golem.NewUUID([16]byte(causation)).String()
}

func parseEventCausation(value string) (golem.CausationID, error) {
	parsed, err := golem.ParseUUID(value)
	return golem.CausationID(parsed.Bytes()), err
}

var _ events.Operator = (*runtimeEventOperator)(nil)
