package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policynormalize "github.com/eleven-am/golem/go/internal/policy/normalize"
	policyresolve "github.com/eleven-am/golem/go/internal/policy/resolve"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/subscription"
)

type deleteTruth uint8

const (
	deleteUnknown deleteTruth = iota
	deleteFalse
	deleteTrue
)

func deleteTruthOf(value bool) deleteTruth {
	if value {
		return deleteTrue
	}
	return deleteFalse
}

func (truth deleteTruth) not() deleteTruth {
	switch truth {
	case deleteTrue:
		return deleteFalse
	case deleteFalse:
		return deleteTrue
	default:
		return deleteUnknown
	}
}

type deleteProbe struct {
	target    golem.ModelID
	condition policyir.Condition
	answered  bool
	exists    bool
}

type deleteFactCheck struct {
	target   policyir.ModelID
	identity []byte
	depth    int
	answered bool
	present  bool
	rows     []mutationfact.OutboxRow
	node     *deleteEvaluator
}

type deleteEvaluation struct {
	causation  string
	generation golem.SchemaDigest
	maxDepth   int
	providers  policyir.ProviderSet
	forward    func(policyir.ModelID, policyir.FieldID, policyir.RelationID) (deleteForwardHop, bool)
	equality   func(policyir.ModelID, []mutationdecode.IdentityComponent) (policyir.Condition, error)
	identity   func(policyir.ModelID, []mutationdecode.IdentityComponent) ([]byte, bool)
	subscribed func(policyir.ModelID) bool
	snapshot   func(*deleteEvaluation, policyir.ModelID, mutationfact.OutboxRow, int) (*deleteEvaluator, bool)
	probes     map[string]*deleteProbe
	order      []*deleteProbe
	checks     map[string]*deleteFactCheck
	checkOrder []*deleteFactCheck
}

type deleteEvaluator struct {
	model  policyir.ModelID
	cells  map[policyir.FieldID]mutationdecode.Cell
	record evaluate.Record
	depth  int
	shared *deleteEvaluation
}

type deleteForwardHop struct {
	target      golem.ModelID
	parentField []golem.FieldID
	childField  []golem.FieldID
}

type deleteVerdict struct {
	policy deleteTruth
	filter deleteTruth
}

func (app *App[P, A]) evaluateDeletedEvent(ctx context.Context, caller *Caller[P, A], state *callerEventSubscription[P], fact mutationfact.Envelope, identity mutationdecode.Identity) (subscription.Evaluation[any], bool, error) {
	model := policyir.ModelID(state.model)
	policy, present := caller.policies.Policy(model)
	if !present {
		return subscription.Suppress[any](events.SuppressionUnauthorized), false, nil
	}
	raw, err := policyresolve.RowConstraint(policy, policyir.ActionRead, model)
	if err != nil {
		return app.unverifiableDelete(ctx, state)
	}
	allowed, err := policyresolve.ActionAllowed(raw)
	if err != nil {
		return app.unverifiableDelete(ctx, state)
	}
	if !allowed {
		return subscription.Suppress[any](events.SuppressionUnauthorized), false, nil
	}
	evaluator, err := app.newDeleteEvaluator(model, fact, identity)
	if err != nil {
		return subscription.Evaluation[any]{}, false, events.Failure(events.CodeSubscriptionSourceClosed)
	}
	row, _, err := readplan.CallerReach(caller.policies, model, nil, app.readLimits.plan.MaxRelationDepth)
	if errors.Is(err, policyruntime.ErrPolicyUnavailable) {
		return subscription.Suppress[any](events.SuppressionPolicyUnavailable), false, nil
	}
	if err != nil {
		return app.unverifiableDelete(ctx, state)
	}
	verdict := deleteVerdict{policy: evaluator.truth(row)}
	if verdict.policy == deleteFalse {
		return subscription.Suppress[any](events.SuppressionUnauthorized), false, nil
	}
	where, err := app.eventDeleteFilter(ctx, caller, state)
	if err != nil {
		return subscription.Evaluation[any]{}, false, err
	}
	var filter *policyir.Condition
	if where != nil {
		_, filter, err = readplan.CallerReach(caller.policies, model, where, app.readLimits.plan.MaxRelationDepth)
		if errors.Is(err, policyruntime.ErrPolicyUnavailable) {
			return subscription.Suppress[any](events.SuppressionPolicyUnavailable), false, nil
		}
		if err != nil {
			filter = nil
			verdict.filter = deleteUnknown
		}
	}
	if filter != nil {
		verdict.filter = evaluator.truth(*filter)
	} else if where == nil {
		verdict.filter = deleteTrue
	}
	pending := make([]policyir.Condition, 0, 2)
	if verdict.policy == deleteUnknown {
		pending = append(pending, row)
	}
	if verdict.filter == deleteUnknown && filter != nil {
		pending = append(pending, *filter)
	}
	if len(pending) != 0 {
		if err := app.resolveDeleteEvidence(ctx, caller, evaluator, pending); err != nil {
			return subscription.Evaluation[any]{}, false, events.Failure(events.CodeSubscriptionRevalidation)
		}
		if verdict.policy == deleteUnknown {
			verdict.policy = evaluator.truth(row)
		}
		if verdict.filter == deleteUnknown && filter != nil {
			verdict.filter = evaluator.truth(*filter)
		}
	}
	switch verdict.policy {
	case deleteFalse:
		return subscription.Suppress[any](events.SuppressionUnauthorized), false, nil
	case deleteTrue:
		switch verdict.filter {
		case deleteTrue:
			return subscription.Evaluation[any]{}, true, nil
		case deleteFalse:
			return subscription.Suppress[any](events.SuppressionDeleteFiltered), false, nil
		default:
			events.Observe(app.eventObserver, ctx, state.model, golem.EventDeleted, events.ObservationDelivery, events.OutcomeSuccess, events.SuppressionDeleteOverDelivered, 0, 0, 0, 0, 1)
			return subscription.Evaluation[any]{}, true, nil
		}
	default:
		if verdict.filter == deleteFalse {
			return subscription.Suppress[any](events.SuppressionDeleteFiltered), false, nil
		}
		return app.unverifiableDelete(ctx, state)
	}
}

func (app *App[P, A]) resolveDeleteEvidence(ctx context.Context, caller *Caller[P, A], evaluator *deleteEvaluator, conditions []policyir.Condition) error {
	shared := evaluator.shared
	for round := 0; round <= shared.maxDepth+2; round++ {
		for _, condition := range conditions {
			evaluator.collect(condition)
		}
		probes := make([]*deleteProbe, 0, len(shared.order))
		for _, probe := range shared.order {
			if !probe.answered {
				probes = append(probes, probe)
			}
		}
		checks := make([]*deleteFactCheck, 0, len(shared.checkOrder))
		for _, check := range shared.checkOrder {
			if !check.answered {
				checks = append(checks, check)
			}
		}
		if len(probes) == 0 && len(checks) == 0 {
			return nil
		}
		if err := app.runDeleteFactChecks(ctx, caller, shared, checks); err != nil {
			return err
		}
		if err := app.runDeleteProbes(ctx, caller, probes); err != nil {
			return err
		}
	}
	return nil
}

func (app *App[P, A]) unverifiableDelete(ctx context.Context, state *callerEventSubscription[P]) (subscription.Evaluation[any], bool, error) {
	events.Observe(app.eventObserver, ctx, state.model, golem.EventDeleted, events.ObservationSuppression, events.OutcomeSuppressed, events.SuppressionDeletionUnverifiable, 0, 0, 0, 0, 1)
	return subscription.Evaluation[any]{}, false, events.Failure(events.CodeSubscriptionResync)
}

func (app *App[P, A]) eventDeleteFilter(ctx context.Context, caller *Caller[P, A], state *callerEventSubscription[P]) (*policyir.Condition, error) {
	_, prepared, err := app.eventHookedRead(ctx, caller, state)
	if err != nil {
		return nil, err
	}
	where, present := prepared.request.Where()
	if !present {
		return nil, nil
	}
	return &where, nil
}

func (app *App[P, A]) newDeleteEvaluator(model policyir.ModelID, fact mutationfact.Envelope, identity mutationdecode.Identity) (*deleteEvaluator, error) {
	causation := fact.CausationID()
	shared := &deleteEvaluation{
		causation: golem.UUID(causation).String(), generation: fact.Generation(),
		maxDepth: app.readLimits.plan.MaxRelationDepth, providers: app.providers,
		forward: func(owner policyir.ModelID, field policyir.FieldID, relation policyir.RelationID) (deleteForwardHop, bool) {
			endpoint, ok := app.registry.ForwardToOneRelation(golem.ModelID(owner), golem.RelationID(relation))
			if !ok || endpoint.FieldID() != golem.FieldID(field) || len(endpoint.Correlation()) == 0 {
				return deleteForwardHop{}, false
			}
			hop := deleteForwardHop{target: endpoint.TargetModelID()}
			for _, pair := range endpoint.Correlation() {
				hop.parentField = append(hop.parentField, pair.ParentFieldID())
				hop.childField = append(hop.childField, pair.ChildFieldID())
			}
			return hop, true
		},
		equality: func(target policyir.ModelID, components []mutationdecode.IdentityComponent) (policyir.Condition, error) {
			return equalityCondition(app.registry, app.provider, target, components)
		},
		identity:   app.deleteParentIdentity,
		subscribed: app.deleteFactsCaptured,
		snapshot:   app.deleteSnapshotNode,
		probes:     make(map[string]*deleteProbe),
		checks:     make(map[string]*deleteFactCheck),
	}
	var snapshot *mutationdecode.Row
	if fact.DeleteSnapshotState() == mutationir.DeleteSnapshotStoredScalars {
		if row, present := fact.PrivateDeleteSnapshot(); present && row.ModelID() == model && app.deleteSnapshotDeclared(row) {
			snapshot = &row
		}
	}
	return newDeleteNode(shared, model, snapshot, identity, 0)
}

func newDeleteNode(shared *deleteEvaluation, model policyir.ModelID, snapshot *mutationdecode.Row, identity mutationdecode.Identity, depth int) (*deleteEvaluator, error) {
	cells := make(map[policyir.FieldID]mutationdecode.Cell)
	if snapshot != nil {
		for _, cell := range snapshot.Cells() {
			cells[cell.FieldID()] = cell
		}
	}
	for _, component := range identity.Components() {
		value, present := component.PolicyValue()
		if component.IsNull() || !present {
			return nil, fmt.Errorf("delete identity component is NULL")
		}
		cells[component.FieldID()] = mutationdecode.Value(component.FieldID(), value)
	}
	fields := make([]evaluate.Field, 0, len(cells))
	for _, cell := range cells {
		field, err := eventEvaluateField(cell)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	record, err := evaluate.NewRecord(model, fields...)
	if err != nil {
		return nil, err
	}
	return &deleteEvaluator{model: model, cells: cells, record: record, depth: depth, shared: shared}, nil
}

func (app *App[P, A]) deleteFactsCaptured(model policyir.ModelID) bool {
	metadata, ok := app.registry.Model(golem.ModelID(model))
	return ok && metadata.SubscriptionsEnabled()
}

func (app *App[P, A]) deleteParentIdentity(target policyir.ModelID, components []mutationdecode.IdentityComponent) ([]byte, bool) {
	model, ok := app.registry.Model(golem.ModelID(target))
	if !ok {
		return nil, false
	}
	primary := model.PrimaryKey()
	if len(primary) == 0 || len(primary) != len(components) {
		return nil, false
	}
	fields := make([]policyir.FieldID, len(primary))
	for index, field := range primary {
		fields[index] = policyir.FieldID(field)
	}
	cells := make([]mutationdecode.Cell, len(components))
	for index, component := range components {
		value, present := component.PolicyValue()
		if !present {
			return nil, false
		}
		cells[index] = mutationdecode.Value(component.FieldID(), value)
	}
	row, err := mutationdecode.NewRow(app.registry, target, cells)
	if err != nil {
		return nil, false
	}
	identity, err := mutationdecode.ExtractOrderedIdentity(app.registry, row, fields)
	if err != nil {
		return nil, false
	}
	encoded, err := mutationfact.EncodeIdentity(identity)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func (app *App[P, A]) deleteSnapshotNode(shared *deleteEvaluation, target policyir.ModelID, stored mutationfact.OutboxRow, depth int) (*deleteEvaluator, bool) {
	if depth > shared.maxDepth {
		return nil, false
	}
	fact, err := mutationfact.ValidateStoredRow(stored, app.eventSchemas)
	if err != nil || fact.Action() != mutationir.FactDeleted || fact.ModelID() != target || fact.Generation() != shared.generation {
		return nil, false
	}
	if golem.UUID(fact.CausationID()).String() != shared.causation || fact.DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars {
		return nil, false
	}
	snapshot, present := fact.PrivateDeleteSnapshot()
	if !present || snapshot.ModelID() != target || !app.deleteSnapshotDeclared(snapshot) {
		return nil, false
	}
	identity, present := fact.BeforeIdentity()
	if !present {
		return nil, false
	}
	node, err := newDeleteNode(shared, target, &snapshot, identity, depth)
	if err != nil {
		return nil, false
	}
	return node, true
}

func (evaluator *deleteEvaluator) collect(condition policyir.Condition) {
	evaluator.collectAt(condition, true)
}

func (evaluator *deleteEvaluator) collectAt(condition policyir.Condition, positive bool) {
	if condition.ModelID() != evaluator.model {
		return
	}
	switch condition.Kind() {
	case policyir.ConditionLogical:
		op, children, ok := condition.Logical()
		if !ok {
			return
		}
		for _, child := range children {
			evaluator.collectAt(child, positive != (op == policyir.LogicalNot))
		}
	case policyir.ConditionRelation:
		_, _, target, cardinality, child, ok := condition.Relation()
		if !ok || cardinality != policyir.RelationToOne {
			return
		}
		correlation, null, known := evaluator.correlation(condition)
		if !known || null {
			return
		}
		parent, match, ok := evaluator.relationProbesFor(condition, correlation)
		if !ok {
			return
		}
		evaluator.register(parent)
		check := evaluator.registerHopCheck(target, correlation)
		if child != nil {
			for _, model := range deleteRelationTargets(*child) {
				evaluator.registerCheck(model, nil)
			}
		}
		if match != nil && !deleteHopStale(condition, positive) {
			evaluator.register(*match)
		}
		if check != nil && check.node != nil && child != nil {
			if childPositive, ok := deleteChildPolarity(condition, positive); ok {
				check.node.collectAt(*child, childPositive)
			}
		}
	}
}

func deleteChildPolarity(condition policyir.Condition, positive bool) (bool, bool) {
	operator, ok := condition.Operator()
	if !ok {
		return false, false
	}
	switch operator {
	case policyir.OperatorRelationIs, policyir.OperatorRelationIsNotNull:
		return positive, true
	case policyir.OperatorRelationIsNot, policyir.OperatorRelationIsNull:
		return !positive, true
	default:
		return false, false
	}
}

func deleteRelationTargets(condition policyir.Condition) []policyir.ModelID {
	seen := make(map[policyir.ModelID]struct{})
	var result []policyir.ModelID
	var walk func(policyir.Condition)
	walk = func(condition policyir.Condition) {
		switch condition.Kind() {
		case policyir.ConditionLogical:
			_, children, ok := condition.Logical()
			if !ok {
				return
			}
			for _, child := range children {
				walk(child)
			}
		case policyir.ConditionRelation:
			_, _, target, _, child, ok := condition.Relation()
			if !ok {
				return
			}
			if _, duplicate := seen[target]; !duplicate {
				seen[target] = struct{}{}
				result = append(result, target)
			}
			if child != nil {
				walk(*child)
			}
		}
	}
	walk(condition)
	return result
}

func (evaluator *deleteEvaluator) hopCheckKey(target policyir.ModelID, correlation []mutationdecode.IdentityComponent) (policyir.ModelID, []byte, bool) {
	shared := evaluator.shared
	if !shared.subscribed(target) {
		return policyir.ModelID{}, nil, false
	}
	if identity, ok := shared.identity(target, correlation); ok {
		return target, identity, true
	}
	return target, nil, true
}

func (evaluator *deleteEvaluator) registerHopCheck(target policyir.ModelID, correlation []mutationdecode.IdentityComponent) *deleteFactCheck {
	model, identity, ok := evaluator.hopCheckKey(target, correlation)
	if !ok {
		return nil
	}
	return evaluator.registerCheck(model, identity)
}

func deleteCheckKey(model policyir.ModelID, identity []byte) string {
	if identity == nil {
		return "M" + string(model[:])
	}
	return "I" + string(model[:]) + string(identity)
}

func (evaluator *deleteEvaluator) registerCheck(model policyir.ModelID, identity []byte) *deleteFactCheck {
	shared := evaluator.shared
	if !shared.subscribed(model) {
		return nil
	}
	key := deleteCheckKey(model, identity)
	depth := evaluator.depth + 1
	if existing := shared.checks[key]; existing != nil {
		if !existing.answered && depth < existing.depth {
			existing.depth = depth
		}
		return existing
	}
	check := &deleteFactCheck{target: model, identity: identity, depth: depth}
	shared.checks[key] = check
	shared.checkOrder = append(shared.checkOrder, check)
	return check
}

func (evaluator *deleteEvaluator) answeredCheck(model policyir.ModelID, identity []byte) (*deleteFactCheck, bool) {
	check := evaluator.shared.checks[deleteCheckKey(model, identity)]
	if check == nil || !check.answered {
		return nil, false
	}
	return check, true
}

func (evaluator *deleteEvaluator) register(probe deleteProbe) {
	key, err := deleteProbeKey(probe)
	shared := evaluator.shared
	if err != nil || shared.probes[key] != nil {
		return
	}
	stored := &deleteProbe{target: probe.target, condition: probe.condition}
	shared.probes[key] = stored
	shared.order = append(shared.order, stored)
}

func (evaluator *deleteEvaluator) lookup(probe deleteProbe) (*deleteProbe, bool) {
	key, err := deleteProbeKey(probe)
	if err != nil {
		return nil, false
	}
	stored := evaluator.shared.probes[key]
	if stored == nil || !stored.answered {
		return nil, false
	}
	return stored, true
}

func deleteProbeKey(probe deleteProbe) (string, error) {
	canonical, err := policyir.CanonicalCondition(probe.condition)
	if err != nil {
		return "", err
	}
	return string(probe.target[:]) + string(canonical), nil
}

func (evaluator *deleteEvaluator) truth(condition policyir.Condition) deleteTruth {
	return evaluator.truthAt(condition, true)
}

func (evaluator *deleteEvaluator) truthAt(condition policyir.Condition, positive bool) deleteTruth {
	if condition.ModelID() != evaluator.model {
		return deleteUnknown
	}
	switch condition.Kind() {
	case policyir.ConditionConstant:
		value, ok := condition.Constant()
		if !ok {
			return deleteUnknown
		}
		return deleteTruthOf(value)
	case policyir.ConditionLogical:
		return evaluator.logical(condition, positive)
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		return evaluator.leaf(condition)
	case policyir.ConditionRelation:
		return evaluator.relation(condition, positive)
	default:
		return deleteUnknown
	}
}

func (evaluator *deleteEvaluator) logical(condition policyir.Condition, positive bool) deleteTruth {
	op, children, ok := condition.Logical()
	if !ok || len(children) == 0 {
		return deleteUnknown
	}
	switch op {
	case policyir.LogicalNot:
		if len(children) != 1 {
			return deleteUnknown
		}
		return evaluator.truthAt(children[0], !positive).not()
	case policyir.LogicalAnd:
		result := deleteTrue
		for _, child := range children {
			switch evaluator.truthAt(child, positive) {
			case deleteFalse:
				return deleteFalse
			case deleteUnknown:
				result = deleteUnknown
			}
		}
		return result
	case policyir.LogicalOr:
		result := deleteFalse
		for _, child := range children {
			switch evaluator.truthAt(child, positive) {
			case deleteTrue:
				return deleteTrue
			case deleteUnknown:
				result = deleteUnknown
			}
		}
		return result
	default:
		return deleteUnknown
	}
}

func (evaluator *deleteEvaluator) leaf(condition policyir.Condition) deleteTruth {
	field, ok := condition.Field()
	if !ok {
		return deleteUnknown
	}
	if _, present := evaluator.cells[field]; !present {
		return deleteUnknown
	}
	value, err := evaluate.Condition(condition, evaluator.record, evaluator.shared.providers)
	if err != nil {
		return deleteUnknown
	}
	return deleteTruthOf(value)
}

func (evaluator *deleteEvaluator) relation(condition policyir.Condition, positive bool) deleteTruth {
	field, _, target, cardinality, child, ok := condition.Relation()
	if !ok || cardinality != policyir.RelationToOne {
		return deleteUnknown
	}
	operator, ok := condition.Operator()
	if !ok {
		return deleteUnknown
	}
	correlation, null, known := evaluator.correlation(condition)
	if !known {
		return deleteUnknown
	}
	if null {
		empty, err := evaluate.ToOneField(field, target)
		if err != nil {
			return deleteUnknown
		}
		record, err := evaluate.NewRecord(evaluator.model, empty)
		if err != nil {
			return deleteUnknown
		}
		value, err := evaluate.Condition(condition, record, evaluator.shared.providers)
		if err != nil {
			return deleteUnknown
		}
		return deleteTruthOf(value)
	}
	parentProbe, matchProbe, ok := evaluator.relationProbesFor(condition, correlation)
	if !ok {
		return deleteUnknown
	}
	parent, answered := evaluator.lookup(parentProbe)
	if !answered {
		return deleteUnknown
	}
	model, identity, ok := evaluator.hopCheckKey(target, correlation)
	if !ok {
		return deleteUnknown
	}
	check, answered := evaluator.answeredCheck(model, identity)
	if !answered {
		return deleteUnknown
	}
	if check.present {
		if check.node == nil || parent.exists {
			return deleteUnknown
		}
		return check.node.fromSnapshot(operator, child, positive)
	}
	if !parent.exists || deleteHopStale(condition, positive) {
		return deleteUnknown
	}
	if child != nil {
		for _, nested := range deleteRelationTargets(*child) {
			if !evaluator.shared.subscribed(nested) {
				return deleteUnknown
			}
			changed, answered := evaluator.answeredCheck(nested, nil)
			if !answered || changed.present {
				return deleteUnknown
			}
		}
	}
	switch operator {
	case policyir.OperatorRelationIsNotNull:
		return deleteTrue
	case policyir.OperatorRelationIsNull:
		return deleteFalse
	case policyir.OperatorRelationIs, policyir.OperatorRelationIsNot:
		matched := true
		if matchProbe != nil {
			match, matchAnswered := evaluator.lookup(*matchProbe)
			if !matchAnswered {
				return deleteUnknown
			}
			matched = match.exists
		}
		if operator == policyir.OperatorRelationIsNot {
			matched = !matched
		}
		return deleteTruthOf(matched)
	default:
		return deleteUnknown
	}
}

func (node *deleteEvaluator) fromSnapshot(operator policyir.OperatorID, child *policyir.Condition, positive bool) deleteTruth {
	switch operator {
	case policyir.OperatorRelationIsNotNull:
		return deleteTrue
	case policyir.OperatorRelationIsNull:
		return deleteFalse
	case policyir.OperatorRelationIs:
		if child == nil {
			return deleteTrue
		}
		return node.truthAt(*child, positive)
	case policyir.OperatorRelationIsNot:
		if child == nil {
			return deleteFalse
		}
		return node.truthAt(*child, !positive).not()
	default:
		return deleteUnknown
	}
}

func (app *App[P, A]) runDeleteFactChecks(ctx context.Context, caller *Caller[P, A], shared *deleteEvaluation, checks []*deleteFactCheck) error {
	if len(checks) == 0 {
		return nil
	}
	query, args, err := app.deleteFactCheckStatement(shared.causation, checks)
	if err != nil {
		return err
	}
	if maximum := app.readLimits.plan.MaxStatementParameters; maximum > 0 && len(args) > maximum {
		return fmt.Errorf("delete fact lookup exceeds the parameter limit")
	}
	if err := caller.executor.requireAdmitted(ctx); err != nil {
		return err
	}
	queryer, err := caller.executor.queryerFor(app.database)
	if err != nil {
		return err
	}
	rows, err := queryer.QueryxContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := make([][]mutationfact.OutboxRow, len(checks))
	for rows.Next() {
		index, stored, err := app.scanDeleteFact(rows)
		if err != nil {
			return err
		}
		if index < 0 || index >= len(checks) {
			return fmt.Errorf("delete fact lookup returned an unknown check")
		}
		found[index] = append(found[index], stored)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index, check := range checks {
		check.rows = found[index]
		check.present = len(check.rows) != 0
		check.answered = true
		if check.identity != nil && len(check.rows) == 1 && check.rows[0].Action == "deleted" {
			if node, ok := shared.snapshot(shared, check.target, check.rows[0], check.depth); ok {
				check.node = node
			}
		}
	}
	return nil
}

func (app *App[P, A]) deleteFactCheckStatement(causation string, checks []*deleteFactCheck) (string, []any, error) {
	namespace, ok := app.registry.PhysicalSystemNamespace(app.eventProvider)
	if !ok || namespace == "" || strings.ContainsAny(string(namespace), "\x00\"") {
		return "", nil, fmt.Errorf("delete fact lookup has no system namespace")
	}
	table := `"` + string(namespace) + `"."_golem_outbox"`
	columns := `"event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at"`
	var args []any
	mark := func(value any) string {
		args = append(args, value)
		if app.provider == policyir.ProviderPostgreSQL {
			return fmt.Sprintf("$%d", len(args))
		}
		return "?"
	}
	parts := make([]string, len(checks))
	for index, check := range checks {
		model := fmt.Sprintf("%x", check.target[:])
		where := `"causation_id" = ` + mark(causation) + ` AND "model_id" = ` + mark(model)
		limit := 1
		if check.identity != nil {
			where += ` AND ("before_identity" = ` + mark(check.identity) + ` OR "after_identity" = ` + mark(check.identity) + `)`
			limit = 2
		}
		parts[index] = fmt.Sprintf(`SELECT * FROM (SELECT %d AS "golem_check",%s FROM %s WHERE %s ORDER BY "transaction_ordinal" LIMIT %d) AS "golem_c%d"`, index, columns, table, where, limit, index)
	}
	return strings.Join(parts, " UNION ALL "), args, nil
}

type deleteFactScan struct {
	Check                 int64  `db:"golem_check"`
	EventID               string `db:"event_id"`
	FactVersion           int64  `db:"fact_version"`
	CodecIdentity         string `db:"codec_identity"`
	GenerationFingerprint string `db:"generation_fingerprint"`
	ModelID               string `db:"model_id"`
	Action                string `db:"action"`
	BeforeIdentity        []byte `db:"before_identity"`
	AfterIdentity         []byte `db:"after_identity"`
	CausationID           string `db:"causation_id"`
	TransactionOrdinal    int64  `db:"transaction_ordinal"`
	Metadata              []byte `db:"metadata"`
	DeleteSnapshot        []byte `db:"delete_snapshot"`
}

func (app *App[P, A]) scanDeleteFact(rows interface{ Scan(...any) error }) (int, mutationfact.OutboxRow, error) {
	var value deleteFactScan
	destinations := []any{&value.Check, &value.EventID, &value.FactVersion, &value.CodecIdentity, &value.GenerationFingerprint, &value.ModelID, &value.Action, &value.BeforeIdentity, &value.AfterIdentity, &value.CausationID, &value.TransactionOrdinal, &value.Metadata, &value.DeleteSnapshot}
	var recorded time.Time
	var micros int64
	if app.provider == policyir.ProviderPostgreSQL {
		destinations = append(destinations, &recorded)
	} else {
		destinations = append(destinations, &micros)
	}
	if err := rows.Scan(destinations...); err != nil {
		return 0, mutationfact.OutboxRow{}, err
	}
	if app.provider != policyir.ProviderPostgreSQL {
		recorded = time.UnixMicro(micros)
	}
	return int(value.Check), mutationfact.OutboxRow{
		EventID: value.EventID, FactVersion: value.FactVersion, CodecIdentity: value.CodecIdentity,
		GenerationFingerprint: value.GenerationFingerprint, ModelID: value.ModelID, Action: value.Action,
		BeforeIdentity: value.BeforeIdentity, AfterIdentity: value.AfterIdentity, CausationID: value.CausationID,
		TransactionOrdinal: value.TransactionOrdinal, Metadata: value.Metadata, DeleteSnapshot: value.DeleteSnapshot,
		RecordedAt: recorded.UTC(),
	}, nil
}

func deleteHopStale(condition policyir.Condition, positive bool) bool {
	operator, ok := condition.Operator()
	if !ok {
		return true
	}
	_, _, _, _, child, ok := condition.Relation()
	if !ok {
		return true
	}
	if child == nil {
		return false
	}
	switch operator {
	case policyir.OperatorRelationIs, policyir.OperatorRelationIsNotNull:
		return deleteNestedHopNegative(*child, positive)
	case policyir.OperatorRelationIsNot, policyir.OperatorRelationIsNull:
		return deleteNestedHopNegative(*child, !positive)
	default:
		return true
	}
}

func deleteNestedHopNegative(condition policyir.Condition, positive bool) bool {
	switch condition.Kind() {
	case policyir.ConditionLogical:
		op, children, ok := condition.Logical()
		if !ok {
			return true
		}
		for _, child := range children {
			if deleteNestedHopNegative(child, positive != (op == policyir.LogicalNot)) {
				return true
			}
		}
		return false
	case policyir.ConditionRelation:
		operator, ok := condition.Operator()
		if !ok {
			return true
		}
		_, _, _, _, child, ok := condition.Relation()
		if !ok {
			return true
		}
		exists := positive
		switch operator {
		case policyir.OperatorRelationIs, policyir.OperatorRelationIsNotNull, policyir.OperatorRelationSome:
		case policyir.OperatorRelationIsNot, policyir.OperatorRelationIsNull, policyir.OperatorRelationNone, policyir.OperatorRelationEvery:
			exists = !positive
		default:
			return true
		}
		if !exists {
			return true
		}
		if child == nil {
			return false
		}
		if operator == policyir.OperatorRelationEvery {
			return deleteNestedHopNegative(*child, !exists)
		}
		return deleteNestedHopNegative(*child, exists)
	case policyir.ConditionConstant, policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		return false
	default:
		return true
	}
}

func (evaluator *deleteEvaluator) correlation(condition policyir.Condition) ([]mutationdecode.IdentityComponent, bool, bool) {
	field, relation, target, _, _, _ := condition.Relation()
	hop, ok := evaluator.shared.forward(condition.ModelID(), field, relation)
	if !ok || policyir.ModelID(hop.target) != target {
		return nil, false, false
	}
	components := make([]mutationdecode.IdentityComponent, 0, len(hop.parentField))
	nulls := 0
	for index, parentField := range hop.parentField {
		cell, present := evaluator.cells[policyir.FieldID(parentField)]
		if !present {
			return nil, false, false
		}
		if cell.IsNull() {
			nulls++
			continue
		}
		value, present := cell.PolicyValue()
		if !present {
			return nil, false, false
		}
		component, err := mutationdecode.IdentityValue(policyir.FieldID(hop.childField[index]), value)
		if err != nil {
			return nil, false, false
		}
		components = append(components, component)
	}
	switch {
	case nulls == len(hop.parentField):
		return nil, true, true
	case nulls != 0:
		return nil, false, false
	default:
		return components, false, true
	}
}

func (evaluator *deleteEvaluator) relationProbesFor(condition policyir.Condition, correlation []mutationdecode.IdentityComponent) (deleteProbe, *deleteProbe, bool) {
	_, _, target, _, child, _ := condition.Relation()
	key, err := evaluator.shared.equality(target, correlation)
	if err != nil {
		return deleteProbe{}, nil, false
	}
	key, err = policynormalize.Condition(key)
	if err != nil {
		return deleteProbe{}, nil, false
	}
	parent := deleteProbe{target: golem.ModelID(target), condition: key}
	if child == nil {
		return parent, nil, true
	}
	merged, err := policyir.NewLogical(target, policyir.LogicalAnd, []policyir.Condition{key, *child})
	if err != nil {
		return deleteProbe{}, nil, false
	}
	merged, err = policynormalize.Condition(merged)
	if err != nil {
		return deleteProbe{}, nil, false
	}
	return parent, &deleteProbe{target: golem.ModelID(target), condition: merged}, true
}

func (app *App[P, A]) runDeleteProbes(ctx context.Context, caller *Caller[P, A], probes []*deleteProbe) error {
	pending := make([]*deleteProbe, 0, len(probes))
	for _, probe := range probes {
		if probe != nil && !probe.answered {
			pending = append(pending, probe)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	dialect, err := deleteProbeDialect(app.provider)
	if err != nil {
		return err
	}
	resolver := policysql.SchemaResolver(app.registry)
	aliases := policysql.NewPolicyAliasAllocator()
	columns := make([]string, len(pending))
	var args []any
	for index, probe := range pending {
		model, ok := resolver.Model(app.provider, policyir.ModelID(probe.target))
		if !ok {
			return fmt.Errorf("delete probe target has no physical model")
		}
		alias := physical.PhysicalName(fmt.Sprintf("golem_d%d", index))
		fragment, err := policysql.CompileWithPolicyAliasAllocator(policysql.Request{
			Condition: probe.condition, Provider: app.provider, Resolver: resolver, Dialect: dialect,
			Capabilities: app.capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: alias,
		}, aliases)
		if err != nil {
			return err
		}
		text := policysql.RebasePlaceholders(fragment.SQL(), len(args), app.provider)
		args = append(args, fragment.Args()...)
		columns[index] = "CASE WHEN EXISTS (SELECT 1 FROM " + dialect.Table(model) + " AS " + dialect.Quote(alias) + " WHERE " + text + ") THEN 1 ELSE 0 END AS " + dialect.Quote(physical.PhysicalName(fmt.Sprintf("golem_x%d", index)))
	}
	if maximum := app.readLimits.plan.MaxStatementParameters; maximum > 0 && len(args) > maximum {
		return fmt.Errorf("delete probe statement exceeds the parameter limit")
	}
	if err := caller.executor.requireAdmitted(ctx); err != nil {
		return err
	}
	queryer, err := caller.executor.queryerFor(app.database)
	if err != nil {
		return err
	}
	rows, err := queryer.QueryxContext(ctx, "SELECT "+strings.Join(columns, ", "), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return fmt.Errorf("delete probe statement returned no row")
	}
	values := make([]int64, len(pending))
	destinations := make([]any, len(pending))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		return err
	}
	if rows.Next() {
		return fmt.Errorf("delete probe statement returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index, probe := range pending {
		switch values[index] {
		case 0:
			probe.exists = false
		case 1:
			probe.exists = true
		default:
			return fmt.Errorf("delete probe returned a non-boolean value")
		}
		probe.answered = true
	}
	return nil
}

func deleteProbeDialect(provider policyir.Provider) (policysql.Dialect, error) {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), nil
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), nil
	default:
		return nil, fmt.Errorf("delete probe provider is unsupported")
	}
}
