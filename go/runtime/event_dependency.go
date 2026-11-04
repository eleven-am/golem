package runtime

import (
	"bytes"
	"context"
	"sort"
	"sync"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/subscription"
)

type eventDependency struct {
	model      golem.ModelID
	fields     []golem.FieldID
	membership bool
}

type eventDependents struct {
	mu      sync.Mutex
	parent  *subscription.Stream[any]
	pending error
	streams []*subscription.Stream[any]
}

type eventDependencyMember struct {
	metadata   golem.EventModelMetadata
	watch      []golem.FieldID
	membership bool
	judge      *eventJudgeSpec
	owner      *eventDependents
}

func (state *callerEventSubscription[P]) judgeSpec() *eventJudgeSpec {
	if state == nil {
		return nil
	}
	return state.judge
}

func (member *eventDependencyMember) judgeSpec() *eventJudgeSpec {
	if member == nil {
		return nil
	}
	return member.judge
}

func (dependents *eventDependents) end(failure error) {
	dependents.mu.Lock()
	parent := dependents.parent
	if parent == nil && dependents.pending == nil {
		dependents.pending = failure
	}
	dependents.mu.Unlock()
	parent.End(failure)
}

func (dependents *eventDependents) attach(parent *subscription.Stream[any]) {
	dependents.mu.Lock()
	dependents.parent = parent
	pending := dependents.pending
	dependents.mu.Unlock()
	if pending != nil {
		parent.End(pending)
	}
}

func (dependents *eventDependents) closeAll() {
	dependents.mu.Lock()
	streams := dependents.streams
	dependents.streams = nil
	dependents.mu.Unlock()
	for _, stream := range streams {
		_ = stream.Close()
	}
}

func (member *eventDependencyMember) dropped(_ any, failure error) {
	member.owner.end(failure)
}

func (app *App[P, A]) eventDependencies(root golem.ModelID, conditions ...policyir.Condition) ([]eventDependency, error) {
	collected, err := dependency.Collect(conditions...)
	if err != nil {
		return nil, err
	}
	fields := make(map[golem.ModelID]map[golem.FieldID]struct{})
	membership := make(map[golem.ModelID]bool)
	var order []golem.ModelID
	track := func(model golem.ModelID) map[golem.FieldID]struct{} {
		if model == root {
			return nil
		}
		set, present := fields[model]
		if !present {
			set = make(map[golem.FieldID]struct{})
			fields[model] = set
			order = append(order, model)
		}
		return set
	}
	var walk func(dependency.Tree, bool) error
	walk = func(tree dependency.Tree, collection bool) error {
		model := golem.ModelID(tree.ModelID())
		for _, entry := range tree.Entries() {
			field := golem.FieldID(entry.FieldID())
			if entry.Kind() == dependency.Scalar {
				if set := track(model); set != nil {
					set[field] = struct{}{}
				}
				continue
			}
			target, _ := entry.TargetModel()
			endpoint, err := app.eventRelationEndpoint(model, field)
			if err != nil {
				return err
			}
			nested := collection || endpoint.Cardinality() != compilerir.RelationOne
			if nested && golem.ModelID(target) != root {
				track(golem.ModelID(target))
				membership[golem.ModelID(target)] = true
			}
			for _, pair := range endpoint.Correlation() {
				if set := track(model); set != nil {
					set[pair.ParentFieldID()] = struct{}{}
				}
				if set := track(golem.ModelID(target)); set != nil {
					set[pair.ChildFieldID()] = struct{}{}
				}
			}
			track(golem.ModelID(target))
			if err := walk(entry.Children(), nested); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(collected.Dependencies(), false); err != nil {
		return nil, err
	}
	result := make([]eventDependency, 0, len(order))
	for _, model := range order {
		list := make([]golem.FieldID, 0, len(fields[model]))
		for field := range fields[model] {
			list = append(list, field)
		}
		sort.Slice(list, func(i, j int) bool { return bytes.Compare(list[i][:], list[j][:]) < 0 })
		result = append(result, eventDependency{model: model, fields: list, membership: membership[model]})
	}
	return result, nil
}

func (app *App[P, A]) eventRelationEndpoint(model golem.ModelID, field golem.FieldID) (schema.RelationEndpoint, error) {
	relationField, present := app.registry.Field(model, field)
	if !present {
		return schema.RelationEndpoint{}, events.Failure(events.CodeSubscriptionInvalid)
	}
	relationID, present := relationField.RelationID()
	if !present {
		return schema.RelationEndpoint{}, events.Failure(events.CodeSubscriptionInvalid)
	}
	endpoint, present := app.registry.RelationEndpoint(model, field, relationID)
	if !present {
		return schema.RelationEndpoint{}, events.Failure(events.CodeSubscriptionInvalid)
	}
	return endpoint, nil
}

func (app *App[P, A]) eventDependencyConditions(ctx context.Context, initial *Caller[P, A], state *callerEventSubscription[P]) ([]policyir.Condition, error) {
	_, prepared, err := app.eventHookedRead(ctx, initial, state)
	if err != nil {
		return nil, err
	}
	var hooked *policyir.Condition
	if where, present := prepared.request.Where(); present {
		hooked = &where
	}
	row, filter, err := readplan.CallerReach(initial.policies, policyir.ModelID(state.model), hooked, app.readLimits.plan.MaxRelationDepth)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	conditions := []policyir.Condition{row}
	if filter != nil {
		conditions = append(conditions, *filter)
	}
	return conditions, nil
}

func (app *App[P, A]) openEventDependencies(ctx context.Context, initial *Caller[P, A], state *callerEventSubscription[P]) (*eventDependents, error) {
	conditions, err := app.eventDependencyConditions(ctx, initial, state)
	if err != nil {
		return nil, err
	}
	found, err := app.eventDependencies(state.model, conditions...)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	for _, item := range found {
		model, present := app.registry.Model(item.model)
		if !present || !model.SubscriptionsEnabled() {
			return nil, events.FailureWithReason(events.CodeSubscriptionInvalid, events.ReasonDependencyUnobservable, model.LogicalName())
		}
	}
	dependents := &eventDependents{}
	for _, item := range found {
		metadata, present := app.eventRegistry.Lookup(item.model)
		if !present {
			dependents.closeAll()
			return nil, events.Failure(events.CodeEventConfig)
		}
		hub, err := app.eventHub(item.model)
		if err != nil {
			dependents.closeAll()
			return nil, err
		}
		key, err := app.eventSubscriberKey(item.model, metadata, nil, readplan.Plan{})
		if err != nil {
			dependents.closeAll()
			return nil, events.Failure(events.CodeSubscriptionInvalid)
		}
		member := &eventDependencyMember{
			metadata: metadata, watch: item.fields, membership: item.membership, owner: dependents,
			judge: app.newEventJudgeSpec(initial, item.model, metadata, nil, item.fields...),
		}
		stream, err := hub.SubscribeWithState(ctx, key, member, member.dropped)
		if err != nil {
			dependents.closeAll()
			return nil, err
		}
		dependents.mu.Lock()
		dependents.streams = append(dependents.streams, stream)
		dependents.mu.Unlock()
	}
	return dependents, nil
}

func (app *App[P, A]) evaluateDependency(member *eventDependencyMember, sharedValue any) (subscription.Evaluation[any], error) {
	shared, ok := sharedValue.(*eventNoticeShared)
	if !ok || shared == nil || shared.failed {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	envelope := shared.envelope
	if envelope.ResolvedEventSchemaDigest() != member.metadata.EventSchemaDigest() || envelope.ModelID() != member.metadata.ModelID() {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	changed, err := app.dependencyChanged(member, shared)
	if err != nil {
		return subscription.Evaluation[any]{}, err
	}
	if changed {
		member.owner.end(events.FailureWithReason(events.CodeSubscriptionResync, events.ReasonDependencyChanged, ""))
	}
	return subscription.Suppress[any](events.SuppressionFiltered), nil
}

func (app *App[P, A]) dependencyChanged(member *eventDependencyMember, shared *eventNoticeShared) (bool, error) {
	fact := shared.envelope.Fact()
	before, after := deleteFalse, deleteFalse
	switch shared.envelope.Action() {
	case golem.EventCreated:
		if !member.membership {
			return false, nil
		}
	case golem.EventUpdated:
		evaluator, imaged := app.dependencyBeforeImage(member, fact)
		if imaged && !dependencyFieldsDiffer(member.watch, evaluator.cells, shared.snapshot) {
			return false, nil
		}
		before = dependencyVerdict(member, evaluator)
	case golem.EventDeleted:
		if !member.membership {
			return false, nil
		}
		evaluator, _ := app.dependencyBeforeImage(member, fact)
		before = dependencyVerdict(member, evaluator)
	default:
		return false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	if shared.envelope.Action() != golem.EventDeleted {
		var err error
		if member.judge == nil {
			after = deleteUnknown
		} else if after, err = shared.verdict(member.judge); err != nil {
			return false, err
		}
	}
	return before != deleteFalse || after != deleteFalse, nil
}

func dependencyFieldsDiffer(watch []golem.FieldID, before map[policyir.FieldID]mutationdecode.Cell, snapshot *eventSnapshot) bool {
	if snapshot == nil || !snapshot.found {
		return true
	}
	for _, field := range watch {
		previous, present := before[policyir.FieldID(field)]
		current, currentPresent := snapshot.evaluator.cells[policyir.FieldID(field)]
		if !present || !currentPresent || !mutationdecode.EqualCell(previous, current) {
			return true
		}
	}
	return false
}

func (app *App[P, A]) eventBeforeImage(model policyir.ModelID, fact mutationfact.Envelope) (mutationdecode.Row, bool) {
	if fact.DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars {
		return mutationdecode.Row{}, false
	}
	row, present := fact.PrivateDeleteSnapshot()
	if !present || row.ModelID() != model || !app.deleteSnapshotDeclared(row) {
		return mutationdecode.Row{}, false
	}
	return row, true
}

func (app *App[P, A]) dependencyBeforeImage(member *eventDependencyMember, fact mutationfact.Envelope) (*deleteEvaluator, bool) {
	identity, present := fact.BeforeIdentity()
	if !present {
		return nil, false
	}
	model := policyir.ModelID(member.metadata.ModelID())
	evaluator, err := app.newDeleteEvaluator(model, fact, identity)
	if err != nil {
		return nil, false
	}
	_, imaged := app.eventBeforeImage(model, fact)
	return evaluator, imaged
}

func dependencyVerdict(member *eventDependencyMember, evaluator *deleteEvaluator) deleteTruth {
	if member.judge == nil || evaluator == nil {
		return deleteUnknown
	}
	return evaluator.truth(member.judge.row)
}
