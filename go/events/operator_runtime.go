package events

import (
	"context"
	"regexp"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

var deliveryFailureCode = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type runtimeDelivery struct {
	causation golem.CausationID
	status    DeliveryStatus
	attempts  int64
	failure   DeliveryFailureCode
	facts     int
	updatedAt time.Time
}

// RuntimeDelivery constructs the sanitized public operator view from an
// already provider-validated delivery row. Lease tokens, fact bytes, record
// identities, private snapshots, and provider errors are intentionally absent.
func RuntimeDelivery(causation golem.CausationID, status DeliveryStatus, attempts int64, failure DeliveryFailureCode, facts int, updatedAt time.Time) (Delivery, error) {
	if causation == (golem.CausationID{}) || attempts < 0 || facts < 0 || updatedAt.IsZero() {
		return nil, Failure(CodeEventSourceClosed)
	}
	switch status {
	case DeliveryPending, DeliveryLeased, DeliveryDelivered, DeliveryBlocked, DeliveryRetired:
	default:
		return nil, Failure(CodeEventSourceClosed)
	}
	if failure != "" && !deliveryFailureCode.MatchString(string(failure)) {
		return nil, Failure(CodeEventSourceClosed)
	}
	return runtimeDelivery{
		causation: causation,
		status:    status,
		attempts:  attempts,
		failure:   failure,
		facts:     facts,
		updatedAt: updatedAt.UTC().Truncate(time.Microsecond),
	}, nil
}

func (delivery runtimeDelivery) CausationID() golem.CausationID { return delivery.causation }
func (delivery runtimeDelivery) Status() DeliveryStatus         { return delivery.status }
func (delivery runtimeDelivery) AttemptCount() int64            { return delivery.attempts }
func (delivery runtimeDelivery) FailureCode() DeliveryFailureCode {
	return delivery.failure
}
func (delivery runtimeDelivery) FactRows() int        { return delivery.facts }
func (delivery runtimeDelivery) UpdatedAt() time.Time { return delivery.updatedAt }

type OperatorAuditAction string
type OperatorAuditOutcome string

const (
	OperatorAuditResume    OperatorAuditAction = "resume"
	OperatorAuditRetire    OperatorAuditAction = "retire"
	OperatorAuditRetention OperatorAuditAction = "retention"

	OperatorAuditSucceeded OperatorAuditOutcome = "succeeded"
	OperatorAuditRejected  OperatorAuditOutcome = "rejected"
	OperatorAuditFailed    OperatorAuditOutcome = "failed"
)

// OperatorAuditRecord is an immutable, sanitized decision record. Retention
// has a zero causation; resume/retire never expose delivery contents.
type OperatorAuditRecord struct {
	action     OperatorAuditAction
	outcome    OperatorAuditOutcome
	causation  golem.CausationID
	causations int
	facts      int
}

func RuntimeOperatorAuditRecord(action OperatorAuditAction, outcome OperatorAuditOutcome, causation golem.CausationID, causations, facts int) OperatorAuditRecord {
	return OperatorAuditRecord{action: action, outcome: outcome, causation: causation, causations: causations, facts: facts}
}

func (record OperatorAuditRecord) Action() OperatorAuditAction   { return record.action }
func (record OperatorAuditRecord) Outcome() OperatorAuditOutcome { return record.outcome }
func (record OperatorAuditRecord) CausationID() golem.CausationID {
	return record.causation
}
func (record OperatorAuditRecord) Causations() int { return record.causations }
func (record OperatorAuditRecord) Facts() int      { return record.facts }

// OperatorAudit is a dedicated trusted callback, separate from metrics and
// principal/scoped-read audit. Panic cannot alter the operator decision.
type OperatorAudit func(context.Context, OperatorAuditRecord)

func ReportOperatorAudit(report OperatorAudit, ctx context.Context, record OperatorAuditRecord) {
	if report == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		report(ctx, record)
	}()
}

var _ Delivery = runtimeDelivery{}
