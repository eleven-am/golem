package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policynormalize "github.com/eleven-am/golem/go/internal/policy/normalize"
	policyresolve "github.com/eleven-am/golem/go/internal/policy/resolve"
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

type deleteEvaluator struct {
	model     policyir.ModelID
	cells     map[policyir.FieldID]mutationdecode.Cell
	record    evaluate.Record
	providers policyir.ProviderSet
	forward   func(policyir.ModelID, policyir.FieldID, policyir.RelationID) (deleteForwardHop, bool)
	equality  func(policyir.ModelID, []mutationdecode.IdentityComponent) (policyir.Condition, error)
	probes    map[string]*deleteProbe
	order     []*deleteProbe
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

func (app *App[P, A]) evaluateDeletedEvent(ctx context.Context, caller *Caller[P, A], state *callerEventSubscription[P], fact interface {
	PrivateDeleteSnapshot() (mutationdecode.Row, bool)
	DeleteSnapshotState() mutationir.DeleteSnapshotState
}, identity mutationdecode.Identity) (subscription.Evaluation[any], bool, error) {
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
		for _, condition := range pending {
			evaluator.collect(condition)
		}
		if err := app.runDeleteProbes(ctx, caller, evaluator.order); err != nil {
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

func (app *App[P, A]) newDeleteEvaluator(model policyir.ModelID, fact interface {
	PrivateDeleteSnapshot() (mutationdecode.Row, bool)
	DeleteSnapshotState() mutationir.DeleteSnapshotState
}, identity mutationdecode.Identity) (*deleteEvaluator, error) {
	cells := make(map[policyir.FieldID]mutationdecode.Cell)
	if fact.DeleteSnapshotState() == mutationir.DeleteSnapshotStoredScalars {
		if snapshot, present := fact.PrivateDeleteSnapshot(); present && snapshot.ModelID() == model && app.deleteSnapshotDeclared(snapshot) {
			for _, cell := range snapshot.Cells() {
				cells[cell.FieldID()] = cell
			}
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
	return &deleteEvaluator{
		model: model, cells: cells, record: record, providers: app.providers,
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
		probes: make(map[string]*deleteProbe),
	}, nil
}

func (evaluator *deleteEvaluator) collect(condition policyir.Condition) {
	evaluator.collectAt(condition, true)
}

func (evaluator *deleteEvaluator) collectAt(condition policyir.Condition, positive bool) {
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
		if deleteHopStale(condition, positive) {
			return
		}
		parent, match, ok := evaluator.relationProbes(condition)
		if !ok {
			return
		}
		evaluator.register(parent)
		if match != nil {
			evaluator.register(*match)
		}
	}
}

func (evaluator *deleteEvaluator) register(probe deleteProbe) {
	key, err := deleteProbeKey(probe)
	if err != nil || evaluator.probes[key] != nil {
		return
	}
	stored := &deleteProbe{target: probe.target, condition: probe.condition}
	evaluator.probes[key] = stored
	evaluator.order = append(evaluator.order, stored)
}

func (evaluator *deleteEvaluator) lookup(probe deleteProbe) (*deleteProbe, bool) {
	key, err := deleteProbeKey(probe)
	if err != nil {
		return nil, false
	}
	stored := evaluator.probes[key]
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
	value, err := evaluate.Condition(condition, evaluator.record, evaluator.providers)
	if err != nil {
		return deleteUnknown
	}
	return deleteTruthOf(value)
}

func (evaluator *deleteEvaluator) relation(condition policyir.Condition, positive bool) deleteTruth {
	field, _, target, cardinality, _, ok := condition.Relation()
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
		value, err := evaluate.Condition(condition, record, evaluator.providers)
		if err != nil {
			return deleteUnknown
		}
		return deleteTruthOf(value)
	}
	if deleteHopStale(condition, positive) {
		return deleteUnknown
	}
	parentProbe, matchProbe, ok := evaluator.relationProbesFor(condition, correlation)
	if !ok {
		return deleteUnknown
	}
	parent, answered := evaluator.lookup(parentProbe)
	if !answered || !parent.exists {
		return deleteUnknown
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
	hop, ok := evaluator.forward(condition.ModelID(), field, relation)
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

func (evaluator *deleteEvaluator) relationProbes(condition policyir.Condition) (deleteProbe, *deleteProbe, bool) {
	if condition.ModelID() != evaluator.model {
		return deleteProbe{}, nil, false
	}
	_, _, _, cardinality, _, ok := condition.Relation()
	if !ok || cardinality != policyir.RelationToOne {
		return deleteProbe{}, nil, false
	}
	correlation, null, known := evaluator.correlation(condition)
	if !known || null {
		return deleteProbe{}, nil, false
	}
	return evaluator.relationProbesFor(condition, correlation)
}

func (evaluator *deleteEvaluator) relationProbesFor(condition policyir.Condition, correlation []mutationdecode.IdentityComponent) (deleteProbe, *deleteProbe, bool) {
	_, _, target, _, child, _ := condition.Relation()
	key, err := evaluator.equality(target, correlation)
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
