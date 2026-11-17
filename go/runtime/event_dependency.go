package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/subscription"
)

type eventDependency struct {
	model      golem.ModelID
	fields     []golem.FieldID
	membership bool
	physical   bool
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
	fact       *eventFactDependency
	physical   bool
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
	collected, err := dependency.CollectReadable(conditions...)
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
	planned, err := app.eventDependencyPlan(ctx, initial, state)
	return planned.conditions, err
}

type eventDependencyPlan struct {
	conditions []policyir.Condition
	facts      observation.Plan
	selection  readplan.Plan
}

// The immutable observation contract includes expanded related policies,
// caller/hook filters and selected masks. It fingerprints bound predicates,
// never current database truth. A changed contract needs a new stream graph.
func (plan eventDependencyPlan) fingerprint() (policyir.Fingerprint, error) {
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:fact-observation:v1\x00"))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(plan.conditions)))
	_, _ = hash.Write(count[:])
	writeCondition := func(condition policyir.Condition) error {
		fingerprint, err := policyir.ConditionFingerprint(condition)
		if err != nil {
			return err
		}
		_, _ = hash.Write(fingerprint[:])
		return nil
	}
	for _, condition := range plan.conditions {
		if err := writeCondition(condition); err != nil {
			return policyir.Fingerprint{}, err
		}
	}
	writeCount := func(count int) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(count))
		_, _ = hash.Write(encoded[:])
	}
	writeFlag := func(value bool) {
		flag := byte(0)
		if value {
			flag = 1
		}
		_, _ = hash.Write([]byte{flag})
	}
	writeMask := func(condition policyir.Condition, present bool) error {
		writeFlag(present)
		if present {
			return writeCondition(condition)
		}
		return nil
	}
	var writeSelection func(readplan.Plan) error
	writeSelection = func(selected readplan.Plan) error {
		model := selected.ModelID()
		_, _ = hash.Write(model[:])
		if err := writeCondition(selected.Where()); err != nil {
			return err
		}
		fields := selected.Fields()
		writeCount(len(fields))
		for _, field := range fields {
			id := field.FieldID()
			_, _ = hash.Write(id[:])
			writeFlag(field.Public())
			if err := writeMask(field.SQLMask()); err != nil {
				return err
			}
		}
		relations := selected.Relations()
		writeCount(len(relations))
		for _, relation := range relations {
			field, id := relation.FieldID(), relation.RelationID()
			_, _ = hash.Write(field[:])
			_, _ = hash.Write(id[:])
			writeFlag(relation.Public())
			if err := writeMask(relation.SQLMask()); err != nil {
				return err
			}
			if err := writeSelection(relation.Child()); err != nil {
				return err
			}
		}
		counts := selected.RelationCounts()
		writeCount(len(counts))
		for _, count := range counts {
			field, id := count.FieldID(), count.RelationID()
			_, _ = hash.Write(field[:])
			_, _ = hash.Write(id[:])
			if err := writeMask(count.SQLMask()); err != nil {
				return err
			}
			if err := writeSelection(count.Child()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := writeSelection(plan.selection); err != nil {
		return policyir.Fingerprint{}, err
	}
	var result policyir.Fingerprint
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func (app *App[P, A]) recheckFactObservation(ctx context.Context, fresh *Caller[P, A], state *callerEventSubscription[P]) (eventDependencyPlan, error) {
	current, err := app.eventDependencyPlan(ctx, fresh, state)
	if err != nil {
		return eventDependencyPlan{}, err
	}
	fingerprint, err := current.fingerprint()
	if err != nil {
		return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if state.factObservation == nil || fingerprint != *state.factObservation {
		// This is an authorization/observation change, not evidence that a
		// hidden dependency changed within the actor's current authority.
		return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionResync)
	}
	return current, nil
}

func (app *App[P, A]) eventDependencyPlan(ctx context.Context, initial *Caller[P, A], state *callerEventSubscription[P]) (eventDependencyPlan, error) {
	_, prepared, err := app.eventHookedRead(ctx, initial, state)
	if err != nil {
		return eventDependencyPlan{}, err
	}
	var hooked *policyir.Condition
	if where, present := prepared.request.Where(); present {
		hooked = &where
	}
	row, filter, err := readplan.CallerReach(initial.policies, policyir.ModelID(state.model), hooked, app.readLimits.plan.MaxRelationDepth)
	if err != nil {
		return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionRevalidation)
	}
	conditions := []policyir.Condition{row}
	if filter != nil {
		conditions = append(conditions, *filter)
	}
	selected, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionRevalidation)
	}
	masks, err := app.eventSelectionFactConditions(initial, selected, false)
	if err != nil {
		return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionInvalid)
	}
	conditions = append(conditions, masks...)
	// Shared ancestors are charged for every lifted question. A collection of
	// individually bounded masks must not multiply the total planning budget.
	if len(masks) != 0 || policyir.HasFacts(row) {
		combined := conditions[0]
		if len(conditions) > 1 {
			combined, err = policyir.NewLogical(row.ModelID(), policyir.LogicalOr, conditions)
			if err != nil {
				return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionInvalid)
			}
		}
		if policyir.CheckFactExpansion(combined) != nil {
			return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionInvalid)
		}
	}
	newObservation := observation.New
	if mutationHistoryWriterRequired(app.registry) {
		newObservation = observation.NewHistorical
	}
	facts, err := newObservation(row, filter, masks...)
	if err != nil {
		return eventDependencyPlan{}, events.Failure(events.CodeSubscriptionInvalid)
	}
	return eventDependencyPlan{conditions: conditions, facts: facts, selection: selected}, nil
}

// A full event selection can depend on facts in a child row policy, scalar
// mask or correlated count. Lift each question through its selected relation,
// retaining the child's own fact scope and the parent's authorized context.
// Private hydration is already represented by the masks that required it.
func (app *App[P, A]) eventSelectionFactConditions(caller *Caller[P, A], selected readplan.Plan, includeWhere bool) ([]policyir.Condition, error) {
	policy, present := caller.policies.Policy(selected.ModelID())
	if !present {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	var conditions []policyir.Condition
	add := func(condition policyir.Condition, present bool) error {
		if present && policyir.HasFacts(condition) {
			guarded, err := policyir.NewLogical(selected.ModelID(), policyir.LogicalAnd, []policyir.Condition{selected.Where(), condition})
			if err != nil {
				return err
			}
			if err := policyir.CheckFactExpansion(guarded); err != nil {
				return err
			}
			conditions = append(conditions, guarded)
		}
		return nil
	}
	if includeWhere && policyir.HasFacts(selected.Where()) {
		conditions = append(conditions, selected.Where())
	} else if includeWhere && len(policy.Facts()) != 0 {
		witness, err := policyir.NewFactScopeWitness(policy.FactScope())
		if err != nil {
			return nil, err
		}
		if err := add(witness, true); err != nil {
			return nil, err
		}
	}
	for _, field := range selected.Fields() {
		if field.Public() {
			if err := add(field.SQLMask()); err != nil {
				return nil, err
			}
		}
	}
	lift := func(field policyir.FieldID, relation policyir.RelationID, child readplan.Plan, many bool, mask policyir.Condition, hasMask bool) error {
		if err := add(mask, hasMask); err != nil {
			return err
		}
		questions, err := app.eventSelectionFactConditions(caller, child, true)
		if err != nil {
			return err
		}
		cardinality, operation := policyir.RelationToOne, policyir.OperatorRelationIs
		if many {
			cardinality, operation = policyir.RelationToMany, policyir.OperatorRelationSome
		}
		providers, err := policyir.NewProviderSet(app.provider)
		if err != nil {
			return err
		}
		requirements, err := operator.ValidateShape(operation, operator.Shape{Node: policyir.ConditionRelation, Cardinality: cardinality, HasChild: true, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Providers: providers})
		if err != nil {
			return err
		}
		for _, question := range questions {
			path, err := policyir.NewRelation(selected.ModelID(), field, relation, child.ModelID(), cardinality, operation, &question, requirements)
			if err != nil {
				return err
			}
			context := []policyir.Condition{selected.Where(), path}
			if hasMask {
				context = append(context, mask)
			}
			condition, err := policyir.NewLogical(selected.ModelID(), policyir.LogicalAnd, context)
			if err != nil {
				return err
			}
			if err := policyir.CheckFactExpansion(condition); err != nil {
				return err
			}
			conditions = append(conditions, condition)
		}
		return nil
	}
	for _, relation := range selected.Relations() {
		if !relation.Public() {
			continue
		}
		mask, present := relation.SQLMask()
		if err := lift(relation.FieldID(), relation.RelationID(), relation.Child(), relation.ToMany(), mask, present); err != nil {
			return nil, err
		}
	}
	for _, count := range selected.RelationCounts() {
		mask, present := count.SQLMask()
		if err := lift(count.FieldID(), count.RelationID(), count.Child(), true, mask, present); err != nil {
			return nil, err
		}
	}
	return conditions, nil
}

func (app *App[P, A]) openEventDependencies(ctx context.Context, initial *Caller[P, A], state *callerEventSubscription[P]) (*eventDependents, error) {
	planned, err := app.eventDependencyPlan(ctx, initial, state)
	if err != nil {
		return nil, err
	}
	found, err := app.eventDependencies(state.model, planned.conditions...)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	physical, err := factEventDependencies(planned.facts, app.registry)
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	found = append(found, physical...)
	for _, item := range found {
		model, present := app.registry.Model(item.model)
		if !present || !model.SubscriptionsEnabled() {
			return nil, events.FailureWithReason(events.CodeSubscriptionInvalid, events.ReasonDependencyUnobservable, model.LogicalName())
		}
	}
	dependents := &eventDependents{}
	// Selected child owners have physical scope paths, even with unused
	// definitions. Unused root-local definitions need this explicit fallback.
	observesFacts := len(physical) != 0
	if policy, present := initial.policies.Policy(policyir.ModelID(state.model)); present {
		observesFacts = observesFacts || len(policy.Facts()) != 0
	}
	if observesFacts {
		fingerprint, err := planned.fingerprint()
		if err != nil {
			return nil, events.Failure(events.CodeSubscriptionRevalidation)
		}
		state.factObservation = &fingerprint
	}
	facts := &eventFactDependency{evaluate: func(ctx context.Context, member *eventDependencyMember, shared *eventNoticeShared) (bool, error) {
		checkContext, release := subscriptionCheckContext(ctx, state.context)
		defer release()
		if state.context.Err() != nil {
			return false, events.Failure(events.CodeSubscriptionCancelled)
		}
		fresh, err := app.ForPrincipal(checkContext, state.principal)
		if err != nil {
			return false, events.Failure(events.CodeSubscriptionRevalidation)
		}
		current, err := app.recheckFactObservation(checkContext, fresh, state)
		if err != nil {
			return false, err
		}
		if !member.physical {
			return app.dependencyChanged(member, shared)
		}
		// Physical policy facts have one evidence contract. Retained V3/CDC
		// notices lack that history and are refused by the loader before SQL;
		// today's relationships must never reconstruct their old audience.
		return app.historicalFactDependencyChanged(checkContext, fresh, state.model, shared, current.facts)
	}}
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
			metadata: metadata, watch: item.fields, membership: item.membership, owner: dependents, physical: item.physical,
		}
		if state.factObservation != nil {
			// Ordinary readable and physical members remain separate, but a
			// fact-owning stream has one shared fresh-authority boundary.
			member.fact = facts
		}
		// Physical members evaluate committed graph evidence and exact current
		// authority probes, never a current snapshot of the changed target.
		if !item.physical {
			member.judge = app.newEventJudgeSpec(initial, item.model, metadata, nil, item.fields...)
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

func (app *App[P, A]) evaluateDependency(ctx context.Context, member *eventDependencyMember, sharedValue any) (subscription.Evaluation[any], error) {
	shared, ok := sharedValue.(*eventNoticeShared)
	if !ok || shared == nil || shared.failed {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	envelope := shared.envelope
	if envelope.ResolvedEventSchemaDigest() != member.metadata.EventSchemaDigest() || envelope.ModelID() != member.metadata.ModelID() {
		return subscription.Evaluation[any]{}, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	var changed bool
	var err error
	if member.fact != nil {
		changed, err = member.fact.evaluate(ctx, member, shared)
	} else {
		changed, err = app.dependencyChanged(member, shared)
	}
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
		image := app.beforeSnapshot(shared, policyir.ModelID(member.metadata.ModelID()))
		if image != nil && !dependencyFieldsDiffer(member.watch, image.evaluator.cells, shared.snapshot) {
			return false, nil
		}
		before = deleteUnknown
		if image != nil && member.judge != nil {
			before = image.judge(member.judge)
		}
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
