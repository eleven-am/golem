package observation

import (
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// BindChange returns a source predicate for one persisted dependency image.
// The caller checks both before and after images. Old correlation keys are
// bound directly, so a moved or deleted target does not need to exist now.
// Missing relevance is conservatively routed within proven authority. Missing
// routing or authorization evidence is an error, never an invented audience.
func (plan Plan) BindChange(row mutationdecode.Row, registry *schema.Registry, provider ir.Provider) (ir.Condition, error) {
	if plan.historical {
		return ir.Condition{}, &ir.FactScopeEvidenceError{ModelID: plan.root}
	}
	if registry == nil || plan.root == (ir.ModelID{}) || row.ModelID() == (ir.ModelID{}) {
		return ir.Condition{}, fmt.Errorf("policy observation: plan, registry and persisted image are required")
	}
	var fields []evaluate.Field
	for _, cell := range row.Cells() {
		if cell.IsNull() {
			fields = append(fields, evaluate.NullField(cell.FieldID()))
			continue
		}
		value, ok := cell.PolicyValue()
		if !ok {
			return ir.Condition{}, fmt.Errorf("policy observation: image value is unavailable")
		}
		field, err := evaluate.ValueField(cell.FieldID(), value)
		if err != nil {
			return ir.Condition{}, err
		}
		fields = append(fields, field)
	}
	record, err := evaluate.NewRecord(row.ModelID(), fields...)
	if err != nil {
		return ir.Condition{}, err
	}
	resolver := policysql.SchemaResolver(registry)
	if !resolver.Providers().Contains(provider) {
		return ir.Condition{}, fmt.Errorf("policy observation: provider is not declared")
	}
	var alternatives []ir.Condition
	for _, path := range plan.paths {
		if path.TargetModel() != row.ModelID() {
			continue
		}
		condition, err := path.bind(row, registry, provider, resolver)
		if err != nil {
			return ir.Condition{}, err
		}
		if path.terminalAuthority.ModelID() != (ir.ModelID{}) {
			allowed, err := evaluate.Condition(path.terminalAuthority, record, resolver.Providers())
			if err != nil {
				return ir.Condition{}, fmt.Errorf("policy observation: historical authority is unknown: %w", err)
			}
			if !allowed {
				continue
			}
		}
		witness, err := localWitness(path.witness, registry, resolver, provider)
		if err != nil {
			return ir.Condition{}, err
		}
		matches, err := evaluate.Condition(witness.upper, record, resolver.Providers())
		if err != nil {
			var failure *evaluate.Error
			if !errors.As(err, &failure) || failure.Code != evaluate.CodeMissing {
				return ir.Condition{}, err
			}
			// Relevance is allowed to be unknown, authority is not. The route
			// retains the exact source scope even when this field is absent.
			matches = true
		}
		if !matches {
			continue
		}
		relevance, err := plan.sourceRelevance()
		if err != nil {
			return ir.Condition{}, err
		}
		condition, err = logical(plan.root, ir.LogicalAnd, []ir.Condition{condition, relevance})
		if err != nil {
			return ir.Condition{}, err
		}
		alternatives = append(alternatives, condition)
	}
	return logical(plan.root, ir.LogicalOr, alternatives)
}

// sourceRelevance retains local caller filters. Relationship truth can change
// concurrently and cannot suppress recovery through an old captured route.
// Audience authority comes solely from explicit fact scopes, retained by each
// path's exact guards and terminal scope image.
func (plan Plan) sourceRelevance() (ir.Condition, error) {
	if plan.filter != nil {
		filter, err := project(*plan.filter, opaqueRelations)
		if err != nil {
			return ir.Condition{}, err
		}
		return filter.upper, nil
	}
	return ir.NewConstant(plan.root, true)
}

func (path Path) bind(row mutationdecode.Row, registry *schema.Registry, provider ir.Provider, resolver policysql.Resolver) (ir.Condition, error) {
	last := path.hops[len(path.hops)-1]
	field, relation, target, _, _, _ := last.relation.Relation()
	endpoint, ok := registry.RelationEndpoint(golem.ModelID(last.relation.ModelID()), golem.FieldID(field), golem.RelationID(relation))
	if !ok || ir.ModelID(endpoint.TargetModelID()) != target || target != row.ModelID() || len(endpoint.Correlation()) == 0 {
		return ir.Condition{}, fmt.Errorf("policy observation: changed target correlation is absent")
	}
	guard, err := last.bindGuard(registry, provider, resolver)
	if err != nil {
		return ir.Condition{}, err
	}
	parts := []ir.Condition{guard}
	for _, pair := range endpoint.Correlation() {
		cell, present := row.Cell(ir.FieldID(pair.ChildFieldID()))
		if !present {
			return ir.Condition{}, fmt.Errorf("policy observation: historical correlation field %x is missing", pair.ChildFieldID())
		}
		if cell.IsNull() {
			return ir.NewConstant(path.hops[0].relation.ModelID(), false)
		}
		value, ok := cell.PolicyValue()
		if !ok {
			return ir.Condition{}, fmt.Errorf("policy observation: historical correlation has no exact value")
		}
		parent, ok := resolver.Field(provider, last.relation.ModelID(), ir.FieldID(pair.ParentFieldID()))
		if !ok {
			return ir.Condition{}, fmt.Errorf("policy observation: correlation parent field is absent")
		}
		operand, err := ir.OneOperand(value)
		if err != nil {
			return ir.Condition{}, err
		}
		requirements, err := operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: parent.Type, Operand: operand, Mode: ir.ComparisonSensitive, Providers: resolver.Providers()})
		if err != nil {
			return ir.Condition{}, err
		}
		condition, err := ir.NewScalar(parent.Model, parent.ID, parent.Type, ir.OperatorEqual, ir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return ir.Condition{}, err
		}
		parts = append(parts, condition)
	}
	current, err := logical(last.relation.ModelID(), ir.LogicalAnd, parts)
	if err != nil {
		return ir.Condition{}, err
	}
	for index := len(path.hops) - 2; index >= 0; index-- {
		current, err = path.hops[index].bindParent(current, registry, provider, resolver)
		if err != nil {
			return ir.Condition{}, err
		}
	}

	return current, nil
}

// bindParent retains the ordinary/fact path guard while converting route
// connectivity to positive Is/Some, independently of data predicate polarity.
func (hop Hop) bindParent(child ir.Condition, registry *schema.Registry, provider ir.Provider, resolver policysql.Resolver) (ir.Condition, error) {
	field, relation, target, cardinality, _, valid := hop.relation.Relation()
	if !valid || child.ModelID() != target {
		return ir.Condition{}, fmt.Errorf("policy observation: parent route changed model")
	}
	operation := ir.OperatorRelationSome
	if cardinality == ir.RelationToOne {
		operation = ir.OperatorRelationIs
	}
	requirements, err := operator.ValidateShape(operation, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: cardinality, HasChild: true, Providers: resolver.Providers()})
	if err != nil {
		return ir.Condition{}, err
	}
	parent, err := ir.NewRelation(hop.relation.ModelID(), field, relation, target, cardinality, operation, &child, requirements)
	if err != nil {
		return ir.Condition{}, err
	}
	guard, err := hop.bindGuard(registry, provider, resolver)
	if err != nil {
		return ir.Condition{}, err
	}
	return logical(parent.ModelID(), ir.LogicalAnd, []ir.Condition{guard, parent})
}

func (hop Hop) bindGuard(registry *schema.Registry, provider ir.Provider, resolver policysql.Resolver) (ir.Condition, error) {
	parts := []ir.Condition{hop.guard}
	for _, refinement := range hop.refinements {
		value, err := localWitness(refinement.condition, registry, resolver, provider)
		if err != nil {
			return ir.Condition{}, err
		}
		needed := value.upper
		if refinement.negative {
			needed, err = negate(value.lower)
			if err != nil {
				return ir.Condition{}, err
			}
		}
		parts = append(parts, needed)
	}
	return logical(hop.relation.ModelID(), ir.LogicalAnd, parts)
}

// WatchFields names persisted fields needed for local witness evaluation and
// relationship correlation, including parent keys of nested relationships.
func (path Path) WatchFields(registry *schema.Registry) ([]golem.FieldID, error) {
	if registry == nil || len(path.hops) == 0 {
		return nil, fmt.Errorf("policy observation: relation path and registry are required")
	}
	var fields []golem.FieldID
	seen := map[golem.FieldID]bool{}
	add := func(field golem.FieldID) {
		if !seen[field] {
			fields = append(fields, field)
			seen[field] = true
		}
	}
	last := path.hops[len(path.hops)-1]
	field, relation, target, _, _, _ := last.relation.Relation()
	endpoint, ok := registry.RelationEndpoint(golem.ModelID(last.relation.ModelID()), golem.FieldID(field), golem.RelationID(relation))
	if !ok || ir.ModelID(endpoint.TargetModelID()) != target {
		return nil, fmt.Errorf("policy observation: relation endpoint is absent")
	}
	for _, pair := range endpoint.Correlation() {
		add(pair.ChildFieldID())
	}
	for _, required := range path.dependencies {
		fact, ok := registry.Field(golem.ModelID(path.TargetModel()), golem.FieldID(required))
		if !ok {
			return nil, fmt.Errorf("policy observation: local dependency field is absent")
		}
		if fact.Kind() != compilerir.FieldRelation {
			add(fact.ID())
			continue
		}
		relation, _ := fact.RelationID()
		endpoint, ok := registry.RelationEndpoint(fact.ModelID(), fact.ID(), relation)
		if !ok {
			return nil, fmt.Errorf("policy observation: local dependency relation is absent")
		}
		for _, pair := range endpoint.Correlation() {
			add(pair.ParentFieldID())
		}
	}
	return fields, nil
}
