package events

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/event/value"
)

type ObservationKind string
type Outcome string
type SuppressionReason string

const (
	ObservationPublisherClaim     ObservationKind = "publisher_claim"
	ObservationPublisherAttempt   ObservationKind = "publisher_attempt"
	ObservationPublisherAck       ObservationKind = "publisher_ack"
	ObservationPublisherRetry     ObservationKind = "publisher_retry"
	ObservationPublisherBlock     ObservationKind = "publisher_block"
	ObservationRetention          ObservationKind = "retention"
	ObservationTransportReceive   ObservationKind = "transport_receive"
	ObservationTransportReconnect ObservationKind = "transport_reconnect"
	ObservationHubMembership      ObservationKind = "hub_membership"
	ObservationEvaluation         ObservationKind = "evaluation"
	ObservationDelivery           ObservationKind = "delivery"
	ObservationSuppression        ObservationKind = "suppression"
	ObservationOverflow           ObservationKind = "overflow"
	ObservationCancellation       ObservationKind = "cancellation"
	ObservationCDCReceive         ObservationKind = "cdc_receive"
	ObservationCDCAck             ObservationKind = "cdc_ack"
	ObservationLifecycleFailure   ObservationKind = "lifecycle_failure"
)

const (
	OutcomeSuccess    Outcome = "success"
	OutcomeFailure    Outcome = "failure"
	OutcomeSuppressed Outcome = "suppressed"
	OutcomeCancelled  Outcome = "cancelled"
)

const (
	SuppressionFiltered             SuppressionReason = "filtered"
	SuppressionUnauthorized         SuppressionReason = "unauthorized"
	SuppressionDeletionUnverifiable SuppressionReason = "deletion-unverifiable"
	SuppressionEntityAbsent         SuppressionReason = "entity_absent"
	SuppressionDeleteFiltered       SuppressionReason = "delete_filtered"
	SuppressionCorrelatedGolemWrite SuppressionReason = "correlated_golem_write"
)

type Observation struct{ value internalvalue.Observation }

func (observation Observation) ModelID() golem.ModelID    { return observation.value.ModelID() }
func (observation Observation) Action() golem.EventAction { return observation.value.Action() }
func (observation Observation) Kind() ObservationKind {
	return ObservationKind(observation.value.Kind())
}
func (observation Observation) Outcome() Outcome { return Outcome(observation.value.Outcome()) }
func (observation Observation) SuppressionReason() SuppressionReason {
	return SuppressionReason(observation.value.SuppressionReason())
}
func (observation Observation) Attempt() int            { return observation.value.Attempt() }
func (observation Observation) QueueDepth() int         { return observation.value.QueueDepth() }
func (observation Observation) QueueLimit() int         { return observation.value.QueueLimit() }
func (observation Observation) Duration() time.Duration { return observation.value.Duration() }
func (observation Observation) AggregateCount() int64   { return observation.value.AggregateCount() }

type Observer interface {
	ObserveEvent(context.Context, Observation)
}

// Observe invokes untrusted instrumentation without allowing a panic to alter
// transport, publisher, or subscription correctness.
func Observe(observer Observer, ctx context.Context, model golem.ModelID, action golem.EventAction, kind ObservationKind, outcome Outcome, reason SuppressionReason, attempt, depth, limit int, duration time.Duration, count int64) {
	if observer == nil {
		return
	}
	// Observation is a deliberately closed, label-only telemetry surface. Drop
	// malformed producer input instead of forwarding attacker-controlled strings
	// or nonsensical counters into an application observer.
	if !validObservationKind(kind) || !validOutcome(outcome) || !validSuppressionReason(reason) || !validObservationAction(action) || attempt < 0 || depth < 0 || limit < 0 || duration < 0 || count < 0 {
		return
	}
	observation := Observation{value: internalvalue.NewObservation(model, action, string(kind), string(outcome), string(reason), attempt, depth, limit, duration, count)}
	func() {
		defer func() { _ = recover() }()
		observer.ObserveEvent(ctx, observation)
	}()
}

func validObservationKind(kind ObservationKind) bool {
	switch kind {
	case ObservationPublisherClaim, ObservationPublisherAttempt, ObservationPublisherAck,
		ObservationPublisherRetry, ObservationPublisherBlock, ObservationRetention,
		ObservationTransportReceive, ObservationTransportReconnect, ObservationHubMembership,
		ObservationEvaluation, ObservationDelivery, ObservationSuppression, ObservationOverflow,
		ObservationCancellation, ObservationCDCReceive, ObservationCDCAck, ObservationLifecycleFailure:
		return true
	default:
		return false
	}
}

func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeSuccess, OutcomeFailure, OutcomeSuppressed, OutcomeCancelled:
		return true
	default:
		return false
	}
}

func validSuppressionReason(reason SuppressionReason) bool {
	switch reason {
	case "", SuppressionFiltered, SuppressionUnauthorized, SuppressionDeletionUnverifiable,
		SuppressionEntityAbsent, SuppressionDeleteFiltered, SuppressionCorrelatedGolemWrite:
		return true
	default:
		return false
	}
}

func validObservationAction(action golem.EventAction) bool {
	switch action {
	case "", golem.EventCreated, golem.EventUpdated, golem.EventDeleted:
		return true
	default:
		return false
	}
}
