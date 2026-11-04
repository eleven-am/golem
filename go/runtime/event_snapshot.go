package runtime

import (
	"context"
	"sync"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type eventJudgeSpec struct {
	row    policyir.Condition
	filter *policyir.Condition
	watch  []golem.FieldID
}

type eventJudged interface {
	judgeSpec() *eventJudgeSpec
}

type eventNoticeShared struct {
	envelope    eventcodec.Envelope
	failed      bool
	snapshot    *eventSnapshot
	snapshotErr error
	beforeOnce  sync.Once
	before      *eventSnapshot
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

func (app *App[P, A]) newEventJudgeSpec(initial *Caller[P, A], model golem.ModelID, metadata golem.EventModelMetadata, where *policyir.Condition, watch ...golem.FieldID) *eventJudgeSpec {
	row, filter, err := readplan.CallerReach(initial.policies, policyir.ModelID(model), where, app.readLimits.plan.MaxRelationDepth)
	if err != nil {
		return nil
	}
	spec := &eventJudgeSpec{row: row, filter: filter, watch: watch}
	tree, err := eventDependencyTree(spec)
	if err != nil {
		return nil
	}
	if _, err := app.eventSnapshotPlan(model, metadata, tree, watch); err != nil {
		return nil
	}
	return spec
}

func eventWatchFields(specs []*eventJudgeSpec) []golem.FieldID {
	var watch []golem.FieldID
	for _, spec := range specs {
		watch = append(watch, spec.watch...)
	}
	return watch
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

func (app *App[P, A]) eventSnapshotPlan(model golem.ModelID, metadata golem.EventModelMetadata, tree dependency.Tree, watch []golem.FieldID) (readplan.Plan, error) {
	selected := append(append([]golem.FieldID(nil), metadata.IdentityFields()...), watch...)
	selection := make([]readir.Selection, 0, len(selected))
	seen := make(map[golem.FieldID]struct{}, len(selected))
	for _, field := range selected {
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		scalar, err := readir.NewScalarSelection(policyir.FieldID(field))
		if err != nil {
			return readplan.Plan{}, err
		}
		selection = append(selection, scalar)
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
		if judged, ok := item.(eventJudged); ok && judged.judgeSpec() != nil {
			specs = append(specs, judged.judgeSpec())
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
	planned, err := app.eventSnapshotPlan(metadata.ModelID(), metadata, tree, eventWatchFields(specs))
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

func (app *App[P, A]) exitUnseen(shared *eventNoticeShared, state *callerEventSubscription[P]) bool {
	before := app.beforeSnapshot(shared, policyir.ModelID(state.model))
	return before != nil && before.judge(state.judge) == deleteFalse
}

func (app *App[P, A]) beforeSnapshot(shared *eventNoticeShared, model policyir.ModelID) *eventSnapshot {
	shared.beforeOnce.Do(func() { shared.before = app.newExitSnapshot(shared, model) })
	return shared.before
}

func (app *App[P, A]) newExitSnapshot(shared *eventNoticeShared, model policyir.ModelID) *eventSnapshot {
	fact := shared.envelope.Fact()
	if fact.DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars {
		return nil
	}
	identity, present := fact.AfterIdentity()
	if !present {
		return nil
	}
	evaluator, err := app.newDeleteEvaluator(model, fact, identity)
	if err != nil {
		return nil
	}
	exact := &eventSnapshot{found: true, evaluator: evaluator}
	after := shared.snapshot
	if after == nil || !after.found {
		return exact
	}
	hops := app.unchangedHops(model, evaluator.cells, after.evaluator.cells)
	record := evaluator.record.WithRelations(after.evaluator.record, hops)
	rebased := &deleteEvaluator{model: model, cells: evaluator.cells, record: record, shared: &deleteEvaluation{providers: app.providers, fromRecord: true}}
	return &eventSnapshot{found: true, evaluator: rebased}
}

func (app *App[P, A]) unchangedHops(model policyir.ModelID, before, after map[policyir.FieldID]mutationdecode.Cell) map[policyir.FieldID]struct{} {
	metadata, present := app.registry.Model(golem.ModelID(model))
	if !present {
		return nil
	}
	kept := make(map[policyir.FieldID]struct{})
	for _, fieldID := range metadata.Fields() {
		field, found := app.registry.Field(golem.ModelID(model), fieldID)
		if !found || field.Kind() != compilerir.FieldRelation {
			continue
		}
		relationID, found := field.RelationID()
		if !found {
			continue
		}
		endpoint, forward := app.registry.ForwardToOneRelation(golem.ModelID(model), relationID)
		if !forward || endpoint.FieldID() != fieldID || len(endpoint.Correlation()) == 0 {
			continue
		}
		unchanged := true
		for _, pair := range endpoint.Correlation() {
			parent := policyir.FieldID(pair.ParentFieldID())
			left, leftFound := before[parent]
			right, rightFound := after[parent]
			if !leftFound || !rightFound || !mutationdecode.EqualCell(left, right) {
				unchanged = false
				break
			}
		}
		if unchanged {
			kept[policyir.FieldID(fieldID)] = struct{}{}
		}
	}
	return kept
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
