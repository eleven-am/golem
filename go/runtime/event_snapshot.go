package runtime

import (
	"context"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type eventJudgeSpec struct {
	row    policyir.Condition
	filter *policyir.Condition
}

type eventNoticeShared struct {
	envelope    eventcodec.Envelope
	failed      bool
	snapshot    *eventSnapshot
	snapshotErr error
}

type eventSnapshot struct {
	found     bool
	evaluator *deleteEvaluator
}

func (spec eventJudgeSpec) conditions() []policyir.Condition {
	if spec.filter == nil {
		return []policyir.Condition{spec.row}
	}
	return []policyir.Condition{spec.row, *spec.filter}
}

func (app *App[P, A]) newEventJudgeSpec(initial *Caller[P, A], model golem.ModelID, metadata golem.EventModelMetadata, where *policyir.Condition) *eventJudgeSpec {
	row, filter, err := readplan.CallerReach(initial.policies, policyir.ModelID(model), where, app.readLimits.plan.MaxRelationDepth)
	if err != nil {
		return nil
	}
	spec := &eventJudgeSpec{row: row, filter: filter}
	tree, err := eventDependencyTree(spec)
	if err != nil {
		return nil
	}
	if _, err := app.eventSnapshotPlan(model, metadata, tree); err != nil {
		return nil
	}
	return spec
}

func eventDependencyTree(specs ...*eventJudgeSpec) (dependency.Tree, error) {
	var conditions []policyir.Condition
	for _, spec := range specs {
		conditions = append(conditions, spec.conditions()...)
	}
	collected, err := dependency.Collect(conditions...)
	if err != nil {
		return dependency.Tree{}, err
	}
	return collected.Dependencies(), nil
}

func (app *App[P, A]) eventSnapshotPlan(model golem.ModelID, metadata golem.EventModelMetadata, tree dependency.Tree) (readplan.Plan, error) {
	selection := make([]readir.Selection, len(metadata.IdentityFields()))
	for index, field := range metadata.IdentityFields() {
		scalar, err := readir.NewScalarSelection(policyir.FieldID(field))
		if err != nil {
			return readplan.Plan{}, err
		}
		selection[index] = scalar
	}
	request, err := readir.NewRequest(readir.RequestInput{
		Operation: readir.FindMany, Model: policyir.ModelID(model), Selection: selection, Projection: readir.ProjectionSelect,
	})
	if err != nil {
		return readplan.Plan{}, err
	}
	return readplan.SystemSnapshot(request, app.registry, app.readLimits.plan, tree)
}

func (app *App[P, A]) prepareEventNotice(ctx context.Context, notice events.Notice, metadata golem.EventModelMetadata, retained []any) any {
	shared := &eventNoticeShared{}
	envelope, err := eventcodec.Decode(notice.Encoded(), app.eventSchemas, eventcodec.Limits{MaxEncodedBytes: app.eventLimits.MaxEncodedEventBytes})
	if err != nil || !eventNoticeMatchesEnvelope(notice, envelope) {
		shared.failed = true
		return shared
	}
	shared.envelope = envelope
	if envelope.Action() != golem.EventCreated && envelope.Action() != golem.EventUpdated {
		return shared
	}
	specs := make([]*eventJudgeSpec, 0, len(retained))
	for _, item := range retained {
		if state, ok := item.(*callerEventSubscription[P]); ok && state != nil && state.judge != nil {
			specs = append(specs, state.judge)
		}
	}
	if len(specs) == 0 {
		return shared
	}
	identity, ok := envelope.Fact().AfterIdentity()
	if !ok || !eventIdentityMatches(identity, metadata.IdentityFields()) {
		shared.failed = true
		return shared
	}
	shared.snapshot, shared.snapshotErr = app.readEventSnapshot(ctx, metadata, specs, identity)
	return shared
}

func (app *App[P, A]) readEventSnapshot(ctx context.Context, metadata golem.EventModelMetadata, specs []*eventJudgeSpec, identity mutationdecode.Identity) (*eventSnapshot, error) {
	planned, err := app.eventSnapshotUnionPlan(metadata, specs, identity)
	if err != nil {
		return nil, nil
	}
	rows, err := executePlan(ctx, app, app.System().executor, golem.ReadFindMany, planned)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if len(rows) == 0 {
		return &eventSnapshot{}, nil
	}
	return app.newEventSnapshot(metadata.ModelID(), rows[0]), nil
}

func (app *App[P, A]) eventSnapshotUnionPlan(metadata golem.EventModelMetadata, specs []*eventJudgeSpec, identity mutationdecode.Identity) (readplan.Plan, error) {
	tree, err := eventDependencyTree(specs...)
	if err != nil {
		return readplan.Plan{}, err
	}
	planned, err := app.eventSnapshotPlan(metadata.ModelID(), metadata, tree)
	if err != nil {
		return readplan.Plan{}, err
	}
	identityCondition, err := eventIdentityCondition(app, policyir.ModelID(metadata.ModelID()), identity)
	if err != nil {
		return readplan.Plan{}, err
	}
	planned, err = readplan.WithAdditionalWhere(planned, identityCondition)
	if err != nil {
		return readplan.Plan{}, err
	}
	return readplan.WithMaximumTake(planned, 1)
}

func (app *App[P, A]) newEventSnapshot(model golem.ModelID, row executedRow) *eventSnapshot {
	cells := make(map[policyir.FieldID]mutationdecode.Cell, len(row.values))
	for field, cell := range row.values {
		if cell.IsNull() {
			cells[field] = mutationdecode.Null(field)
		} else if value, present := cell.PolicyValue(); present {
			cells[field] = mutationdecode.Value(field, value)
		}
	}
	evaluator := &deleteEvaluator{
		model: policyir.ModelID(model), cells: cells, record: row.record,
		shared: &deleteEvaluation{providers: app.providers, fromRecord: true},
	}
	return &eventSnapshot{found: true, evaluator: evaluator}
}

func (shared *eventNoticeShared) verdict(spec *eventJudgeSpec) (deleteTruth, error) {
	if shared.snapshotErr != nil {
		return deleteUnknown, shared.snapshotErr
	}
	if shared.snapshot == nil {
		return deleteUnknown, nil
	}
	return shared.snapshot.judge(spec), nil
}

func (snapshot *eventSnapshot) judge(spec *eventJudgeSpec) deleteTruth {
	if !snapshot.found {
		return deleteFalse
	}
	row := snapshot.evaluator.truth(spec.row)
	if row == deleteFalse {
		return deleteFalse
	}
	if spec.filter == nil {
		return row
	}
	filter := snapshot.evaluator.truth(*spec.filter)
	switch {
	case filter == deleteFalse:
		return deleteFalse
	case row == deleteTrue && filter == deleteTrue:
		return deleteTrue
	default:
		return deleteUnknown
	}
}
