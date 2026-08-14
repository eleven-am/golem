package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestOperatorResumeAndRetireAreCausationSpecificAndAudited(t *testing.T) {
	first := golem.CausationID{0: 1, 15: 11}
	second := golem.CausationID{0: 2, 15: 22}
	coordinator := &p7CausationSpecificCoordinator{}
	var audits []events.OperatorAuditRecord
	operator, err := newRuntimeEventOperator(coordinator, func(_ context.Context, record events.OperatorAuditRecord) {
		audits = append(audits, record)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := operator.Resume(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := operator.Retire(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if len(coordinator.resumed) != 1 || coordinator.resumed[0] != eventCausationText(first) {
		t.Fatalf("resume targets=%v want only %q", coordinator.resumed, eventCausationText(first))
	}
	if len(coordinator.retired) != 1 || coordinator.retired[0] != eventCausationText(second) {
		t.Fatalf("retire targets=%v want only %q", coordinator.retired, eventCausationText(second))
	}
	if len(audits) != 2 {
		t.Fatalf("audit count=%d want=2", len(audits))
	}
	if audits[0].Action() != events.OperatorAuditResume || audits[0].Outcome() != events.OperatorAuditSucceeded || audits[0].CausationID() != first {
		t.Fatalf("resume audit=%#v", audits[0])
	}
	if audits[1].Action() != events.OperatorAuditRetire || audits[1].Outcome() != events.OperatorAuditSucceeded || audits[1].CausationID() != second {
		t.Fatalf("retire audit=%#v", audits[1])
	}
}

type p7CausationSpecificCoordinator struct {
	operatorTestCoordinator
	resumed []string
	retired []string
}

func (coordinator *p7CausationSpecificCoordinator) Resume(_ context.Context, causation string) (bool, error) {
	coordinator.resumed = append(coordinator.resumed, causation)
	return true, nil
}

func (coordinator *p7CausationSpecificCoordinator) Retire(_ context.Context, causation string) (bool, error) {
	coordinator.retired = append(coordinator.retired, causation)
	return true, nil
}
