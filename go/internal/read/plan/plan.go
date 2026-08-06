// Package plan builds deterministic provider-neutral authorized read plans.
// It is the only P3 layer permitted to combine caller predicates with policy
// row/field constraints.
package plan

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type ErrorCode string

const (
	CodeInput      ErrorCode = "P3_PLAN_INPUT"
	CodePolicy     ErrorCode = "P3_PLAN_POLICY"
	CodeField      ErrorCode = "P3_PLAN_FIELD"
	CodeLimit      ErrorCode = "P3_PLAN_LIMIT"
	CodeProjection ErrorCode = "P3_PLAN_PROJECTION"
)

type Error struct {
	Code   ErrorCode
	Model  policyir.ModelID
	Field  policyir.FieldID
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	return fmt.Sprintf("%s: model=%x field=%x: %s", failure.Code, failure.Model, failure.Field, failure.Detail)
}
func (failure *Error) Unwrap() error { return failure.Cause }

type Limits struct {
	MaxTake                int
	MaxRelationFanout      int
	MaxRelationDepth       int
	MaxSelected            int
	MaxStatementParameters int
	MaxStatementBytes      int
	MaxStatementAliases    int
}

func DefaultLimits() Limits {
	return Limits{
		MaxRelationDepth:       5,
		MaxSelected:            256,
		MaxStatementParameters: 999,
		MaxStatementBytes:      1 << 20,
		MaxStatementAliases:    2_048,
	}
}

type PolicySet interface {
	Policy(policyir.ModelID) (policyir.Policy, bool)
}

type Field struct {
	id           policyir.FieldID
	typ          compilerir.LogicalTypeIR
	nullable     bool
	public       bool
	mask         *policyir.Condition
	dependencies dependency.Tree
}

func (field Field) FieldID() policyir.FieldID             { return field.id }
func (field Field) LogicalType() compilerir.LogicalTypeIR { return cloneLogicalType(field.typ) }
func (field Field) Nullable() bool                        { return field.nullable }
func (field Field) Public() bool                          { return field.public }
func (field Field) Mask() (policyir.Condition, bool) {
	if field.mask == nil {
		return policyir.Condition{}, false
	}
	return *field.mask, true
}
func (field Field) Dependencies() dependency.Tree { return field.dependencies }

type Relation struct {
	field      policyir.FieldID
	id         policyir.RelationID
	target     policyir.ModelID
	toMany     bool
	public     bool
	mask       *policyir.Condition
	child      *Plan
	occurrence readir.OccurrenceID
}

// RelationCount is one independently authorized correlated count over a
// to-many target. The child plan is always a Count operation and therefore
// carries only the target row policy conjoined with the caller's optional
// per-count where predicate.
type RelationCount struct {
	field      policyir.FieldID
	id         policyir.RelationID
	target     policyir.ModelID
	mask       *policyir.Condition
	child      *Plan
	occurrence readir.OccurrenceID
}

func (count RelationCount) FieldID() policyir.FieldID         { return count.field }
func (count RelationCount) RelationID() policyir.RelationID   { return count.id }
func (count RelationCount) TargetModelID() policyir.ModelID   { return count.target }
func (count RelationCount) OccurrenceID() readir.OccurrenceID { return count.occurrence }
func (count RelationCount) Mask() (policyir.Condition, bool) {
	if count.mask == nil {
		return policyir.Condition{}, false
	}
	return *count.mask, true
}
func (count RelationCount) Child() Plan {
	if count.child == nil {
		return Plan{}
	}
	return count.child.clone()
}

func (relation Relation) FieldID() policyir.FieldID         { return relation.field }
func (relation Relation) RelationID() policyir.RelationID   { return relation.id }
func (relation Relation) TargetModelID() policyir.ModelID   { return relation.target }
func (relation Relation) OccurrenceID() readir.OccurrenceID { return relation.occurrence }
func (relation Relation) ToMany() bool                      { return relation.toMany }
func (relation Relation) Public() bool                      { return relation.public }
func (relation Relation) Mask() (policyir.Condition, bool) {
	if relation.mask == nil {
		return policyir.Condition{}, false
	}
	return *relation.mask, true
}
func (relation Relation) Child() Plan {
	if relation.child == nil {
		return Plan{}
	}
	return relation.child.clone()
}

type Plan struct {
	operation   readir.Operation
	model       policyir.ModelID
	where       policyir.Condition
	anchor      policyir.Condition
	orders      []readir.Order
	take        *int
	resultLimit int
	skip        *int
	distinct    []policyir.FieldID
	cursor      *readir.Cursor
	fields      []Field
	relations   []Relation
	counts      []RelationCount
	hydrations  []Relation
	system      bool
	limits      Limits
}

func (plan Plan) Operation() readir.Operation { return plan.operation }
func (plan Plan) ModelID() policyir.ModelID   { return plan.model }
func (plan Plan) Where() policyir.Condition   { return plan.where }

// CursorAnchor is the authorization reach used to locate a cursor row. It
// deliberately excludes unrelated caller filters while retaining row policy,
// conditional order/selector field guards, and trusted relation correlation.
func (plan Plan) CursorAnchor() policyir.Condition { return plan.anchor }
func (plan Plan) OrderBy() []readir.Order          { return append([]readir.Order(nil), plan.orders...) }
func (plan Plan) Take() (int, bool) {
	if plan.take == nil {
		return 0, false
	}
	return *plan.take, true
}

// ResultLimit is non-zero only when the planner deliberately fetches one row
// beyond a configured root/fanout cap so execution can refuse overflow rather
// than silently truncate it.
func (plan Plan) ResultLimit() int { return plan.resultLimit }
func (plan Plan) Limits() Limits   { return plan.limits }
func (plan Plan) Skip() (int, bool) {
	if plan.skip == nil {
		return 0, false
	}
	return *plan.skip, true
}
func (plan Plan) Distinct() []policyir.FieldID {
	return append([]policyir.FieldID(nil), plan.distinct...)
}
func (plan Plan) Cursor() (readir.Cursor, bool) {
	if plan.cursor == nil {
		return readir.Cursor{}, false
	}
	value, err := readir.NewCursor(plan.cursor.Selector(), plan.cursor.Predicate())
	return value, err == nil
}
func (plan Plan) Fields() []Field       { return cloneFields(plan.fields) }
func (plan Plan) Relations() []Relation { return cloneRelations(plan.relations) }
func (plan Plan) RelationCounts() []RelationCount {
	return cloneRelationCounts(plan.counts)
}

// Hydrations are private, policy-scoped relation views used only to evaluate
// conditional field masks. They are deliberately independent of caller-owned
// nested filters and pagination and are never returned as projected fields.
func (plan Plan) Hydrations() []Relation { return cloneRelations(plan.hydrations) }
func (plan Plan) IsSystem() bool         { return plan.system }

func Caller(request readir.Request, registry *schema.Registry, policies PolicySet, limits Limits) (Plan, error) {
	if policies == nil {
		return Plan{}, fail(CodeInput, request.ModelID(), policyir.FieldID{}, "policy set is required", nil)
	}
	return build(request, registry, policies, limits, false, 0, limits.MaxTake)
}

func System(request readir.Request, registry *schema.Registry, limits Limits) (Plan, error) {
	return build(request, registry, nil, limits, true, 0, limits.MaxTake)
}

// WithAdditionalWhere returns a cloned plan whose already-authorized predicate
// is conjoined with one runtime-owned correlation predicate. The additional
// predicate must target the plan model; callers cannot use it to reopen or
// replace the policy constraint established by Caller.
func WithAdditionalWhere(plan Plan, condition policyir.Condition) (Plan, error) {
	if plan.model == (policyir.ModelID{}) || condition.ModelID() != plan.model {
		return Plan{}, fail(CodeInput, plan.model, policyir.FieldID{}, "additional predicate targets a different model", nil)
	}
	merged, err := and(plan.model, plan.where, condition)
	if err != nil {
		return Plan{}, fail(CodeInput, plan.model, policyir.FieldID{}, "additional predicate could not be merged", err)
	}
	result := plan.clone()
	result.where = merged
	anchor, err := and(plan.model, plan.anchor, condition)
	if err != nil {
		return Plan{}, fail(CodeInput, plan.model, policyir.FieldID{}, "additional cursor-anchor predicate could not be merged", err)
	}
	result.anchor = anchor
	return result, nil
}

// WithMaximumTake applies a trusted runtime safety ceiling without widening an
// existing stricter limit. It is used for schema-declared to-one traversals so
// corrupted data cannot turn an invariant check into an unbounded read.
func WithMaximumTake(plan Plan, maximum int) (Plan, error) {
	if plan.model == (policyir.ModelID{}) || maximum <= 0 {
		return Plan{}, fail(CodeLimit, plan.model, policyir.FieldID{}, "maximum take must be positive", nil)
	}
	result := plan.clone()
	if result.take == nil || *result.take > maximum || *result.take < -maximum {
		result.take = &maximum
	}
	return result, nil
}

func build(request readir.Request, registry *schema.Registry, policies PolicySet, limits Limits, system bool, depth, configuredRowCap int) (Plan, error) {
	if registry == nil || request.ModelID() == (policyir.ModelID{}) {
		return Plan{}, fail(CodeInput, request.ModelID(), policyir.FieldID{}, "registry and request are required", nil)
	}
	if limits.MaxTake < 0 || limits.MaxRelationFanout < 0 || limits.MaxRelationDepth < 0 || limits.MaxSelected <= 0 || limits.MaxStatementParameters <= 0 || limits.MaxStatementBytes <= 0 || limits.MaxStatementAliases <= 0 {
		return Plan{}, fail(CodeLimit, request.ModelID(), policyir.FieldID{}, "limits are invalid", nil)
	}
	if depth > limits.MaxRelationDepth {
		return Plan{}, fail(CodeLimit, request.ModelID(), policyir.FieldID{}, "relation depth limit exceeded", nil)
	}
	effectiveMaxTake := configuredRowCap
	if modelFact, ok := registry.Model(golem.ModelID(request.ModelID())); ok {
		if configured, present := modelFact.MaxTake(); present && (effectiveMaxTake == 0 || int(configured) < effectiveMaxTake) {
			effectiveMaxTake = int(configured)
		}
	}
	if take, ok := request.Take(); ok && effectiveMaxTake > 0 && (take > effectiveMaxTake || take < -effectiveMaxTake) {
		return Plan{}, fail(CodeLimit, request.ModelID(), policyir.FieldID{}, "take exceeds the model maximum", nil)
	}

	model := request.ModelID()
	var policy policyir.Policy
	var row policyir.Condition
	var rawRow policyir.Condition
	var expander *policyExpander
	var err error
	if system {
		rawRow, err = policyir.NewConstant(model, true)
	} else {
		var ok bool
		policy, ok = policies.Policy(model)
		if !ok {
			return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "model policy is absent", nil)
		}
		rawRow, err = resolve.RowConstraint(policy, policyir.ActionRead, model)
	}
	if err != nil {
		return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "read row constraint could not be resolved", err)
	}
	if !system {
		allowed, gateErr := resolve.ActionAllowed(rawRow)
		if gateErr != nil {
			return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "read action gate could not be resolved", gateErr)
		}
		if !allowed {
			return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "read action is not granted", nil)
		}
		expander = newPolicyExpander(policies, limits.MaxRelationDepth)
		row, err = expander.model(model, depth)
		if err != nil {
			return Plan{}, err
		}
	} else {
		row = rawRow
	}
	authorizedWhere := row
	if callerWhere, ok := request.Where(); ok {
		if system {
			authorizedWhere = callerWhere
		} else {
			authorizedCaller, authErr := expander.condition(callerWhere, depth)
			if authErr != nil {
				return Plan{}, authErr
			}
			authorizedWhere, err = and(model, row, authorizedCaller)
			if err != nil {
				return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "caller and policy predicates could not be merged", err)
			}
		}
	}
	authorizedWhere, err = normalize.Condition(authorizedWhere)
	if err != nil {
		return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "authorized predicate could not be normalized", err)
	}

	orders := request.OrderBy()
	if request.Operation() != readir.Count {
		if modelFact, ok := registry.Model(golem.ModelID(model)); ok {
			seenOrder := make(map[policyir.FieldID]bool, len(orders))
			for _, order := range orders {
				seenOrder[order.FieldID()] = true
			}
			for _, primary := range modelFact.PrimaryKey() {
				field := policyir.FieldID(primary)
				if seenOrder[field] {
					continue
				}
				order, orderErr := readir.NewOrder(field, readir.Ascending)
				if orderErr != nil {
					return Plan{}, fail(CodeInput, model, field, "primary-key tie-breaker is invalid", orderErr)
				}
				orders = append(orders, order)
				seenOrder[field] = true
			}
		}
	}

	cursorAnchor := row
	if !system {
		if callerWhere, ok := request.Where(); ok {
			if err := classifyCondition(callerWhere, registry, policies, policy, authorizedWhere, expander, depth); err != nil {
				return Plan{}, err
			}
		}
		if selector, ok := request.Selector(); ok {
			for _, field := range selector.Fields() {
				if _, err := classifyField(registry, policy, model, field, classify.UseSelector, authorizedWhere, true); err != nil {
					return Plan{}, err
				}
			}
		}
		for _, order := range orders {
			classification, classifyErr := classifyField(registry, policy, model, order.FieldID(), classify.UseOrder, authorizedWhere, true)
			if classifyErr != nil {
				return Plan{}, classifyErr
			}
			if classification.Access() == classify.AccessConditional {
				guard, present := classification.FieldCondition()
				if !present {
					return Plan{}, fail(CodePolicy, model, order.FieldID(), "conditional order field has no authorization guard", nil)
				}
				cursorAnchor, err = and(model, cursorAnchor, guard)
				if err != nil {
					return Plan{}, fail(CodePolicy, model, order.FieldID(), "cursor order-field guard could not be merged", err)
				}
			}
		}
		for _, field := range request.Distinct() {
			if _, err := classifyField(registry, policy, model, field, classify.UseDistinct, authorizedWhere, true); err != nil {
				return Plan{}, err
			}
		}
		if cursor, ok := request.Cursor(); ok {
			cursorReach, reachErr := and(model, authorizedWhere, cursor.Predicate())
			if reachErr != nil {
				return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "cursor reach could not be resolved", reachErr)
			}
			for _, field := range cursor.Selector().Fields() {
				classification, classifyErr := classifyField(registry, policy, model, field, classify.UseCursor, cursorReach, true)
				if classifyErr != nil {
					return Plan{}, classifyErr
				}
				if classification.Access() == classify.AccessConditional {
					guard, present := classification.FieldCondition()
					if !present {
						return Plan{}, fail(CodePolicy, model, field, "conditional cursor field has no authorization guard", nil)
					}
					cursorAnchor, err = and(model, cursorAnchor, guard)
					if err != nil {
						return Plan{}, fail(CodePolicy, model, field, "cursor selector-field guard could not be merged", err)
					}
				}
			}
		}
	}
	cursorAnchor, err = normalize.Condition(cursorAnchor)
	if err != nil {
		return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "cursor authorization reach could not be normalized", err)
	}

	result := Plan{operation: request.Operation(), model: model, where: authorizedWhere, anchor: cursorAnchor, orders: orders, distinct: request.Distinct(), system: system, limits: limits}
	if cursor, ok := request.Cursor(); ok {
		value, cursorErr := readir.NewCursor(cursor.Selector(), cursor.Predicate())
		if cursorErr != nil {
			return Plan{}, fail(CodeInput, model, policyir.FieldID{}, "cursor is invalid", cursorErr)
		}
		result.cursor = &value
	}
	if take, ok := request.Take(); ok {
		result.take = &take
	} else if request.Operation() == readir.FindMany && effectiveMaxTake > 0 {
		fetch := effectiveMaxTake + 1
		result.take = &fetch
		result.resultLimit = effectiveMaxTake
	}
	if skip, ok := request.Skip(); ok {
		result.skip = &skip
	}
	if request.Operation() == readir.Count {
		return result, nil
	}

	selections := request.Selection()
	if request.ProjectionMode() == readir.ProjectionDefault || request.ProjectionMode() == readir.ProjectionInclude {
		modelFact, ok := registry.Model(golem.ModelID(model))
		if !ok {
			return Plan{}, fail(CodeInput, model, policyir.FieldID{}, "model is absent from registry", nil)
		}
		omitted := make(map[policyir.FieldID]bool, len(request.Omitted()))
		for _, id := range request.Omitted() {
			omitted[id] = true
		}
		defaults := make([]readir.Selection, 0, len(modelFact.Fields()))
		for _, id := range modelFact.Fields() {
			fact, _ := registry.Field(golem.ModelID(model), id)
			if fact.Kind() != compilerir.FieldRelation && fact.Visible() && !omitted[policyir.FieldID(id)] {
				selection, _ := readir.NewScalarSelection(policyir.FieldID(id))
				defaults = append(defaults, selection)
			}
		}
		selections = append(defaults, selections...)
	}
	if len(selections) > limits.MaxSelected {
		return Plan{}, fail(CodeLimit, model, policyir.FieldID{}, "selected field limit exceeded", nil)
	}

	planned := make(map[policyir.FieldID]int)
	maskConditions := make([]policyir.Condition, 0)
	for _, selection := range selections {
		fact, ok := registry.Field(golem.ModelID(model), golem.FieldID(selection.FieldID()))
		if !ok {
			return Plan{}, fail(CodeField, model, selection.FieldID(), "selection field is absent", nil)
		}
		var classification classify.Classification
		if !system {
			classification, err = classifyField(registry, policy, model, selection.FieldID(), classify.UseProjection, authorizedWhere, false)
			if err != nil {
				return Plan{}, err
			}
		}
		if selection.Kind() == readir.SelectScalar {
			field := Field{id: selection.FieldID(), typ: fact.LogicalType(), nullable: fact.Nullable(), public: true}
			if !system && classification.Access() == classify.AccessConditional {
				mask, _ := classification.FieldCondition()
				field.mask = &mask
				field.dependencies = classification.Dependencies()
				maskConditions = append(maskConditions, mask)
			}
			if existing, injected := planned[field.id]; injected {
				// A dependency of an earlier selected field may already have
				// inserted this scalar privately. Promote that same scan slot;
				// never create duplicate SQL columns/evaluator fields.
				result.fields[existing] = field
			} else {
				planned[field.id] = len(result.fields)
				result.fields = append(result.fields, field)
			}
			if !system {
				injectScalarDependencies(&result, registry, classification.Requires(), planned)
			}
			continue
		}
		endpoint, ok := registry.RelationEndpoint(golem.ModelID(model), golem.FieldID(selection.FieldID()), golem.RelationID(selection.RelationID()))
		if !ok {
			return Plan{}, fail(CodeField, model, selection.FieldID(), "relation endpoint is absent", nil)
		}
		childRequest, ok := selection.Request()
		if !ok {
			return Plan{}, fail(CodeProjection, model, selection.FieldID(), "relation child request is absent", nil)
		}
		childCap := 0
		if endpoint.Cardinality() == compilerir.RelationMany {
			childCap = limits.MaxRelationFanout
		}
		child, childErr := build(childRequest, registry, policies, limits, system, depth+1, childCap)
		if childErr != nil {
			return Plan{}, childErr
		}
		if selection.Kind() == readir.SelectRelationCount {
			if endpoint.Cardinality() != compilerir.RelationMany || child.Operation() != readir.Count {
				return Plan{}, fail(CodeProjection, model, selection.FieldID(), "relation count requires a to-many endpoint and count child", nil)
			}
			count := RelationCount{field: selection.FieldID(), id: selection.RelationID(), target: selection.TargetModelID(), child: &child, occurrence: selection.OccurrenceID()}
			if !system && classification.Access() == classify.AccessConditional {
				mask, _ := classification.FieldCondition()
				count.mask = &mask
				maskConditions = append(maskConditions, mask)
				injectScalarDependencies(&result, registry, classification.Requires(), planned)
			}
			result.counts = append(result.counts, count)
			continue
		}
		relation := Relation{field: selection.FieldID(), id: selection.RelationID(), target: selection.TargetModelID(), toMany: endpoint.Cardinality() == compilerir.RelationMany, public: true, child: &child, occurrence: selection.OccurrenceID()}
		if !system && classification.Access() == classify.AccessConditional {
			mask, _ := classification.FieldCondition()
			relation.mask = &mask
			maskConditions = append(maskConditions, mask)
			injectScalarDependencies(&result, registry, classification.Requires(), planned)
		}
		result.relations = append(result.relations, relation)
		for _, pair := range endpoint.Correlation() {
			injectScalarDependencies(&result, registry, []policyir.FieldID{policyir.FieldID(pair.ParentFieldID())}, planned)
		}
	}
	for _, order := range result.orders {
		injectScalarDependencies(&result, registry, []policyir.FieldID{order.FieldID()}, planned)
	}
	injectScalarDependencies(&result, registry, result.distinct, planned)
	if len(maskConditions) != 0 {
		dependencies, dependencyErr := dependency.Collect(maskConditions...)
		if dependencyErr != nil {
			return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "field-mask dependencies could not be derived", dependencyErr)
		}
		if hydrationErr := hydrateDependencies(&result, dependencies.Dependencies(), registry, policies, limits, system, depth, planned); hydrationErr != nil {
			return Plan{}, hydrationErr
		}
	}
	if len(result.fields)+len(result.relations)+len(result.counts)+len(result.hydrations) > limits.MaxSelected {
		return Plan{}, fail(CodeLimit, model, policyir.FieldID{}, "selected and policy-only fields exceed the limit", nil)
	}
	return result, nil
}

// hydrateDependencies adds the exact private record graph required by mask
// evaluation. Public relation plans are never reused here: their caller-owned
// where/skip/take/distinct shape must not influence authorization.
func hydrateDependencies(plan *Plan, tree dependency.Tree, registry *schema.Registry, policies PolicySet, limits Limits, system bool, depth int, planned map[policyir.FieldID]int) error {
	if tree.ModelID() != plan.model {
		return fail(CodePolicy, plan.model, policyir.FieldID{}, "field-mask dependency root does not match the read model", nil)
	}
	for _, entry := range tree.Entries() {
		switch entry.Kind() {
		case dependency.Scalar:
			injectScalarDependencies(plan, registry, []policyir.FieldID{entry.FieldID()}, planned)
		case dependency.Relation:
			if depth+1 > limits.MaxRelationDepth {
				return fail(CodeLimit, plan.model, entry.FieldID(), "policy-only relation depth limit exceeded", nil)
			}
			target, ok := entry.TargetModel()
			if !ok {
				return fail(CodePolicy, plan.model, entry.FieldID(), "relation dependency has no target model", nil)
			}
			fact, ok := registry.Field(golem.ModelID(plan.model), golem.FieldID(entry.FieldID()))
			if !ok || fact.Kind() != compilerir.FieldRelation {
				return fail(CodePolicy, plan.model, entry.FieldID(), "relation dependency is absent from the schema", nil)
			}
			relationID, ok := fact.RelationID()
			if !ok {
				return fail(CodePolicy, plan.model, entry.FieldID(), "relation dependency has no relation identity", nil)
			}
			endpoint, ok := registry.RelationEndpoint(golem.ModelID(plan.model), golem.FieldID(entry.FieldID()), relationID)
			if !ok || policyir.ModelID(endpoint.TargetModelID()) != target {
				return fail(CodePolicy, plan.model, entry.FieldID(), "relation dependency does not match the schema endpoint", nil)
			}
			childCap := 0
			if endpoint.Cardinality() == compilerir.RelationMany {
				childCap = limits.MaxRelationFanout
			}
			child, err := dependencyReadPlan(entry.Children(), registry, policies, limits, system, depth+1, childCap)
			if err != nil {
				return err
			}
			plan.hydrations = append(plan.hydrations, Relation{
				field: entry.FieldID(), id: policyir.RelationID(relationID), target: target,
				toMany: endpoint.Cardinality() == compilerir.RelationMany, child: &child,
			})
			for _, pair := range endpoint.Correlation() {
				injectScalarDependencies(plan, registry, []policyir.FieldID{policyir.FieldID(pair.ParentFieldID())}, planned)
			}
		default:
			return fail(CodePolicy, plan.model, entry.FieldID(), "field-mask dependency has an unknown kind", nil)
		}
	}
	return nil
}

func dependencyReadPlan(tree dependency.Tree, registry *schema.Registry, policies PolicySet, limits Limits, system bool, depth, configuredRowCap int) (Plan, error) {
	model := tree.ModelID()
	if model == (policyir.ModelID{}) || depth > limits.MaxRelationDepth {
		return Plan{}, fail(CodeLimit, model, policyir.FieldID{}, "policy-only relation depth limit exceeded", nil)
	}
	var row policyir.Condition
	var err error
	if system {
		row, err = policyir.NewConstant(model, true)
	} else {
		_, ok := policies.Policy(model)
		if !ok {
			return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "relation dependency target policy is absent", nil)
		}
		row, err = newPolicyExpander(policies, limits.MaxRelationDepth).model(model, depth)
	}
	if err != nil {
		return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "relation dependency target policy could not be resolved", err)
	}
	row, err = normalize.Condition(row)
	if err != nil {
		return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "relation dependency target predicate could not be normalized", err)
	}
	result := Plan{operation: readir.FindMany, model: model, where: row, anchor: row, system: system, limits: limits}
	effectiveRowCap := configuredRowCap
	if modelFact, ok := registry.Model(golem.ModelID(model)); ok {
		if configured, present := modelFact.MaxTake(); present && (effectiveRowCap == 0 || int(configured) < effectiveRowCap) {
			effectiveRowCap = int(configured)
		}
	}
	if effectiveRowCap > 0 {
		fetch := effectiveRowCap + 1
		result.take = &fetch
		result.resultLimit = effectiveRowCap
	}
	planned := make(map[policyir.FieldID]int)
	if err := hydrateDependencies(&result, tree, registry, policies, limits, system, depth, planned); err != nil {
		return Plan{}, err
	}
	modelFact, ok := registry.Model(golem.ModelID(model))
	if !ok || len(modelFact.PrimaryKey()) == 0 {
		return Plan{}, fail(CodePolicy, model, policyir.FieldID{}, "relation dependency target has no primary key", nil)
	}
	for _, primary := range modelFact.PrimaryKey() {
		field := policyir.FieldID(primary)
		order, orderErr := readir.NewOrder(field, readir.Ascending)
		if orderErr != nil {
			return Plan{}, fail(CodePolicy, model, field, "relation dependency order is invalid", orderErr)
		}
		result.orders = append(result.orders, order)
		injectScalarDependencies(&result, registry, []policyir.FieldID{field}, planned)
	}
	if len(result.fields)+len(result.hydrations) > limits.MaxSelected {
		return Plan{}, fail(CodeLimit, model, policyir.FieldID{}, "policy-only dependency fields exceed the limit", nil)
	}
	return result, nil
}

func classifyCondition(condition policyir.Condition, registry *schema.Registry, policies PolicySet, policy policyir.Policy, selecting policyir.Condition, expander *policyExpander, depth int) error {
	switch condition.Kind() {
	case policyir.ConditionConstant:
		return nil
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		field, _ := condition.Field()
		_, err := classifyField(registry, policy, condition.ModelID(), field, classify.UseFilter, selecting, true)
		return err
	case policyir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := classifyCondition(child, registry, policies, policy, selecting, expander, depth); err != nil {
				return err
			}
		}
		return nil
	case policyir.ConditionRelation:
		field, _, target, _, child, _ := condition.Relation()
		if _, err := classifyField(registry, policy, condition.ModelID(), field, classify.UseFilter, selecting, true); err != nil {
			return err
		}
		if child == nil {
			return nil
		}
		targetPolicy, ok := policies.Policy(target)
		if !ok {
			return fail(CodePolicy, target, field, "relation target policy is absent", nil)
		}
		targetRow, err := expander.model(target, depth+1)
		if err != nil {
			return err
		}
		authorizedChild, err := expander.condition(*child, depth+1)
		if err != nil {
			return err
		}
		targetSelecting, err := and(target, targetRow, authorizedChild)
		if err != nil {
			return fail(CodePolicy, target, field, "relation filter selecting constraint could not be merged", err)
		}
		return classifyCondition(*child, registry, policies, targetPolicy, targetSelecting, expander, depth+1)
	default:
		return fail(CodeInput, condition.ModelID(), policyir.FieldID{}, "unknown condition kind", nil)
	}
}

func classifyField(registry *schema.Registry, policy policyir.Policy, model policyir.ModelID, field policyir.FieldID, use classify.UseKind, selecting policyir.Condition, requireDischarged bool) (classify.Classification, error) {
	request, err := classify.NewRequestWithConstraint(registry, policy, model, []policyir.FieldID{field}, use, policyir.ActionRead, selecting)
	if err != nil {
		return classify.Classification{}, fail(CodePolicy, model, field, "field classification request failed", err)
	}
	result, err := classify.Fields(request)
	if err != nil {
		return classify.Classification{}, fail(CodePolicy, model, field, "field classification failed", err)
	}
	classification, ok := result.Field(field)
	if !ok {
		return classify.Classification{}, fail(CodePolicy, model, field, "field classification is absent", nil)
	}
	if classification.Access() == classify.AccessNever || requireDischarged && classification.Access() == classify.AccessConditional && !classification.DischargedByConstraint() {
		return classify.Classification{}, fail(CodeField, model, field, "field is not readable in this statement position", nil)
	}
	return classification, nil
}

func injectScalarDependencies(plan *Plan, registry *schema.Registry, fields []policyir.FieldID, planned map[policyir.FieldID]int) {
	for _, field := range fields {
		if _, exists := planned[field]; exists {
			continue
		}
		fact, ok := registry.Field(golem.ModelID(plan.model), golem.FieldID(field))
		if !ok || fact.Kind() == compilerir.FieldRelation {
			continue
		}
		planned[field] = len(plan.fields)
		plan.fields = append(plan.fields, Field{id: field, typ: fact.LogicalType(), nullable: fact.Nullable(), public: false})
	}
}

func and(model policyir.ModelID, conditions ...policyir.Condition) (policyir.Condition, error) {
	result, err := policyir.NewLogical(model, policyir.LogicalAnd, conditions)
	if err != nil {
		return policyir.Condition{}, err
	}
	return normalize.Condition(result)
}
func logicalNot(model policyir.ModelID, condition policyir.Condition) (policyir.Condition, error) {
	result, err := policyir.NewLogical(model, policyir.LogicalNot, []policyir.Condition{condition})
	if err != nil {
		return policyir.Condition{}, err
	}
	return normalize.Condition(result)
}
func fail(code ErrorCode, model policyir.ModelID, field policyir.FieldID, detail string, cause error) error {
	return &Error{Code: code, Model: model, Field: field, Detail: detail, Cause: cause}
}

func (plan Plan) clone() Plan {
	result := plan
	result.orders = plan.OrderBy()
	result.distinct = plan.Distinct()
	result.fields = cloneFields(plan.fields)
	result.relations = cloneRelations(plan.relations)
	result.counts = cloneRelationCounts(plan.counts)
	result.hydrations = cloneRelations(plan.hydrations)
	if plan.take != nil {
		value := *plan.take
		result.take = &value
	}
	if plan.skip != nil {
		value := *plan.skip
		result.skip = &value
	}
	if plan.cursor != nil {
		value, _ := readir.NewCursor(plan.cursor.Selector(), plan.cursor.Predicate())
		result.cursor = &value
	}
	return result
}
func cloneRelationCounts(values []RelationCount) []RelationCount {
	result := make([]RelationCount, len(values))
	for index, value := range values {
		result[index] = value
		if value.mask != nil {
			mask := *value.mask
			result[index].mask = &mask
		}
		if value.child != nil {
			child := value.child.clone()
			result[index].child = &child
		}
	}
	return result
}
func cloneFields(values []Field) []Field {
	result := make([]Field, len(values))
	copy(result, values)
	for index := range result {
		result[index].typ = cloneLogicalType(result[index].typ)
		if result[index].mask != nil {
			value := *result[index].mask
			result[index].mask = &value
		}
	}
	return result
}
func cloneRelations(values []Relation) []Relation {
	result := make([]Relation, len(values))
	for index, value := range values {
		result[index] = value
		if value.mask != nil {
			mask := *value.mask
			result[index].mask = &mask
		}
		if value.child != nil {
			child := value.child.clone()
			result[index].child = &child
		}
	}
	return result
}
func cloneLogicalType(value compilerir.LogicalTypeIR) compilerir.LogicalTypeIR {
	if value.EnumID != nil {
		copy := *value.EnumID
		value.EnumID = &copy
	}
	if value.Element != nil {
		copy := cloneLogicalType(*value.Element)
		value.Element = &copy
	}
	if value.Precision != nil {
		copy := *value.Precision
		value.Precision = &copy
	}
	if value.Scale != nil {
		copy := *value.Scale
		value.Scale = &copy
	}
	if value.MaxLength != nil {
		copy := *value.MaxLength
		value.MaxLength = &copy
	}
	if value.JSONSchemaID != nil {
		copy := *value.JSONSchemaID
		value.JSONSchemaID = &copy
	}
	if value.Capability != nil {
		copy := *value.Capability
		value.Capability = &copy
	}
	return value
}
