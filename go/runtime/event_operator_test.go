package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

func TestRuntimeOperatorSanitizesDeliveryAndAuditsDecisions(t *testing.T) {
	cause := golem.CausationID{15: 7}
	coordinator := &operatorTestCoordinator{delivery: eventprovider.Delivery{
		CausationID: eventCausationText(cause), Status: eventprovider.StatusBlocked,
		AttemptCount: 3, LastFailureCode: "schema-unavailable", ImmutableFactRows: 4,
		LeaseToken: "must-never-escape", UpdatedAt: time.Unix(100, 999).UTC(),
	}, resumeChanged: true, retireChanged: true, retention: eventprovider.RetentionResult{Causations: 2, Facts: 5}}
	var audits []events.OperatorAuditRecord
	operator, err := newRuntimeEventOperator(coordinator, func(_ context.Context, record events.OperatorAuditRecord) {
		audits = append(audits, record)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := operator.Inspect(context.Background(), cause)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.CausationID() != cause || delivery.Status() != events.DeliveryBlocked || delivery.AttemptCount() != 3 || delivery.FailureCode() != "schema-unavailable" || delivery.FactRows() != 4 {
		t.Fatalf("sanitized delivery=%#v", delivery)
	}
	if err := operator.Resume(context.Background(), cause); err != nil {
		t.Fatal(err)
	}
	if err := operator.Retire(context.Background(), cause); err != nil {
		t.Fatal(err)
	}
	policy, err := events.NewRetentionPolicy(time.Unix(200, 0), 9)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := operator.RunRetention(context.Background(), policy)
	if err != nil || retained.Causations() != 2 || retained.Facts() != 5 {
		t.Fatalf("retention=%#v error=%v", retained, err)
	}
	if len(audits) != 3 || audits[0].Action() != events.OperatorAuditResume || audits[1].Action() != events.OperatorAuditRetire || audits[2].Action() != events.OperatorAuditRetention {
		t.Fatalf("audits=%#v", audits)
	}
	for _, audit := range audits {
		if audit.Outcome() != events.OperatorAuditSucceeded {
			t.Fatalf("audit outcome=%q", audit.Outcome())
		}
	}
}

func TestRuntimeOperatorSanitizesFailuresAndAuditPanicIsNeutral(t *testing.T) {
	cause := golem.CausationID{15: 8}
	coordinator := &operatorTestCoordinator{resumeErr: errors.New("driver secret"), retireChanged: true}
	operator, err := newRuntimeEventOperator(coordinator, func(context.Context, events.OperatorAuditRecord) { panic("audit panic") }, observerPanic{})
	if err != nil {
		t.Fatal(err)
	}
	if err := operator.Resume(context.Background(), cause); eventErrorCode(err) != events.CodeEventSourceClosed {
		t.Fatalf("resume error=%v", err)
	}
	if err := operator.Retire(context.Background(), cause); err != nil {
		t.Fatalf("audit panic altered successful retire: %v", err)
	}
	policy, _ := events.NewRetentionPolicy(time.Unix(200, 0), 1)
	if _, err := operator.RunRetention(context.Background(), policy); err != nil {
		t.Fatalf("observer/audit panic altered retention: %v", err)
	}
}

type observerPanic struct{}

func (observerPanic) ObserveEvent(context.Context, events.Observation) { panic("observer panic") }

type operatorTestCoordinator struct {
	delivery      eventprovider.Delivery
	inspectErr    error
	resumeChanged bool
	resumeErr     error
	retireChanged bool
	retireErr     error
	retention     eventprovider.RetentionResult
	retentionErr  error
}

func (*operatorTestCoordinator) Claim(context.Context, eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	return nil, nil
}
func (*operatorTestCoordinator) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}
func (*operatorTestCoordinator) Acknowledge(context.Context, string, string) (bool, error) {
	return false, nil
}
func (*operatorTestCoordinator) Retry(context.Context, string, string, time.Duration, string) (bool, error) {
	return false, nil
}
func (*operatorTestCoordinator) Block(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (*operatorTestCoordinator) Release(context.Context, string, string) (bool, error) {
	return false, nil
}
func (coordinator *operatorTestCoordinator) Inspect(context.Context, string) (eventprovider.Delivery, error) {
	return coordinator.delivery, coordinator.inspectErr
}
func (coordinator *operatorTestCoordinator) Resume(context.Context, string) (bool, error) {
	return coordinator.resumeChanged, coordinator.resumeErr
}
func (coordinator *operatorTestCoordinator) Retire(context.Context, string) (bool, error) {
	return coordinator.retireChanged, coordinator.retireErr
}
func (coordinator *operatorTestCoordinator) RunRetention(context.Context, eventprovider.RetentionPolicy) (eventprovider.RetentionResult, error) {
	return coordinator.retention, coordinator.retentionErr
}

func eventErrorCode(err error) events.ErrorCode {
	code, _ := events.CodeOf(err)
	return code
}
