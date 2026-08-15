package events

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type p7ObservationCapture struct{ values []Observation }

func (capture *p7ObservationCapture) ObserveEvent(_ context.Context, observation Observation) {
	capture.values = append(capture.values, observation)
}

func TestObserverShapeContainsOnlyClosedSafeLabels(t *testing.T) {
	kinds := []ObservationKind{
		ObservationPublisherClaim, ObservationPublisherAttempt, ObservationPublisherAck,
		ObservationPublisherRetry, ObservationPublisherBlock, ObservationRetention,
		ObservationTransportReceive, ObservationTransportReconnect, ObservationHubMembership,
		ObservationEvaluation, ObservationDelivery, ObservationSuppression, ObservationOverflow,
		ObservationCancellation, ObservationCDCReceive, ObservationCDCAck, ObservationLifecycleFailure,
	}
	outcomes := []Outcome{OutcomeSuccess, OutcomeFailure, OutcomeSuppressed, OutcomeCancelled}
	reasons := []SuppressionReason{"", SuppressionFiltered, SuppressionUnauthorized, SuppressionDeletionUnverifiable, SuppressionEntityAbsent, SuppressionDeleteFiltered, SuppressionCorrelatedGolemWrite}
	capture := &p7ObservationCapture{}
	for index, kind := range kinds {
		outcome := outcomes[index%len(outcomes)]
		reason := reasons[index%len(reasons)]
		Observe(capture, context.Background(), golem.ModelID{15: byte(index + 1)}, golem.EventUpdated, kind, outcome, reason, index, index+1, index+2, time.Duration(index)*time.Microsecond, int64(index+1))
	}
	if len(capture.values) != len(kinds) {
		t.Fatalf("closed observations=%d want=%d", len(capture.values), len(kinds))
	}
	for index, observation := range capture.values {
		if observation.Kind() != kinds[index] || observation.Outcome() != outcomes[index%len(outcomes)] || observation.SuppressionReason() != reasons[index%len(reasons)] {
			t.Fatalf("observation %d changed closed labels: %#v", index, observation)
		}
	}

	before := len(capture.values)
	invalid := []struct {
		kind    ObservationKind
		outcome Outcome
		reason  SuppressionReason
		action  golem.EventAction
		attempt int
	}{
		{kind: ObservationKind("driver-secret"), outcome: OutcomeSuccess},
		{kind: ObservationDelivery, outcome: Outcome("sql-secret")},
		{kind: ObservationSuppression, outcome: OutcomeSuppressed, reason: SuppressionReason("principal-secret")},
		{kind: ObservationDelivery, outcome: OutcomeSuccess, action: golem.EventAction("row-secret")},
		{kind: ObservationDelivery, outcome: OutcomeSuccess, attempt: -1},
	}
	for _, input := range invalid {
		Observe(capture, context.Background(), golem.ModelID{}, input.action, input.kind, input.outcome, input.reason, input.attempt, 0, 0, 0, 0)
	}
	if len(capture.values) != before {
		t.Fatalf("invalid producer input reached observer: before=%d after=%d", before, len(capture.values))
	}

	typeOfObservation := reflect.TypeOf(Observation{})
	for index := 0; index < typeOfObservation.NumField(); index++ {
		if typeOfObservation.Field(index).IsExported() {
			t.Fatalf("observation exposes mutable field %q", typeOfObservation.Field(index).Name)
		}
	}
}
