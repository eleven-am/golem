package observation

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

type fieldProjection struct {
	owner  ir.ModelID
	all    bool
	fields map[ir.FieldID]policysql.Field
}

type witnessProjector struct {
	root     ir.ModelID
	registry *schema.Registry
	resolver policysql.Resolver
	provider ir.Provider
}

// localWitness preserves constraints on related keys by transporting them
// through the relationship correlation. Treating the complete hop as unknown
// would erase actor grants such as Storage.ID.In(actor.StorageIDs).
func localWitness(input ir.Condition, registry *schema.Registry, resolver policysql.Resolver, provider ir.Provider) (bounds, error) {
	projector := witnessProjector{root: input.ModelID(), registry: registry, resolver: resolver, provider: provider}
	return projector.project(input, fieldProjection{owner: input.ModelID(), all: true})
}

func (projector witnessProjector) field(mapping fieldProjection, id ir.FieldID) (policysql.Field, bool) {
	if mapping.all {
		return projector.resolver.Field(projector.provider, mapping.owner, id)
	}
	field, ok := mapping.fields[id]
	return field, ok
}

func (projector witnessProjector) project(input ir.Condition, mapping fieldProjection) (bounds, error) {
	if input.ModelID() != mapping.owner {
		return bounds{}, fmt.Errorf("policy observation: witness projection changed model")
	}
	switch input.Kind() {
	case ir.ConditionConstant:
		value, _ := input.Constant()
		constant, err := ir.NewConstant(projector.root, value)
		return bounds{lower: constant, upper: constant}, err
	case ir.ConditionFact:
		_, body, _ := input.Fact()
		return projector.project(body, mapping)
	case ir.ConditionLogical:
		operation, children, _ := input.Logical()
		lower, upper := make([]ir.Condition, len(children)), make([]ir.Condition, len(children))
		for index, child := range children {
			value, err := projector.project(child, mapping)
			if err != nil {
				return bounds{}, err
			}
			lower[index], upper[index] = value.lower, value.upper
		}
		if operation == ir.LogicalNot {
			lower, upper = upper, lower
		}
		return combineBounds(projector.root, operation, lower, upper)
	case ir.ConditionScalar:
		fieldID, _ := input.Field()
		field, known := projector.field(mapping, fieldID)
		if !known {
			if mapping.all {
				return bounds{}, fmt.Errorf("policy observation: witness field is absent from the schema")
			}
			return unknown(projector.root)
		}
		operation, _ := input.Operator()
		mode, _ := input.Mode()
		operand, _ := input.Operand()
		requirements, err := operator.ValidateShape(operation, operator.Shape{Node: ir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: mode, Providers: projector.resolver.Providers()})
		if err != nil {
			return bounds{}, err
		}
		condition, err := ir.NewScalar(projector.root, field.ID, field.Type, operation, mode, operand, requirements)
		return bounds{lower: condition, upper: condition}, err
	case ir.ConditionList, ir.ConditionJSON:
		if mapping.all {
			return bounds{lower: input, upper: input}, nil
		}
		return unknown(projector.root)
	case ir.ConditionRelation:
		return projector.relation(input, mapping)
	default:
		return bounds{}, fmt.Errorf("policy observation: unresolved witness condition")
	}
}

func (projector witnessProjector) relation(input ir.Condition, mapping fieldProjection) (bounds, error) {
	field, relation, target, _, child, _ := input.Relation()
	endpoint, ok := projector.registry.RelationEndpoint(golem.ModelID(input.ModelID()), golem.FieldID(field), golem.RelationID(relation))
	if !ok || ir.ModelID(endpoint.TargetModelID()) != target || len(endpoint.Correlation()) == 0 {
		return bounds{}, fmt.Errorf("policy observation: witness relationship is absent")
	}
	childMapping := fieldProjection{owner: target, fields: map[ir.FieldID]policysql.Field{}}
	var present []ir.Condition
	for _, pair := range endpoint.Correlation() {
		parent, known := projector.field(mapping, ir.FieldID(pair.ParentFieldID()))
		if !known {
			continue
		}
		childMapping.fields[ir.FieldID(pair.ChildFieldID())] = parent
		if parent.Type.Nullable() {
			requirements, err := operator.ValidateShape(ir.OperatorIsNotNull, operator.Shape{Node: ir.ConditionScalar, FieldType: parent.Type, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Providers: projector.resolver.Providers()})
			if err != nil {
				return bounds{}, err
			}
			condition, err := ir.NewScalar(projector.root, parent.ID, parent.Type, ir.OperatorIsNotNull, ir.ComparisonSensitive, ir.NoOperand(), requirements)
			if err != nil {
				return bounds{}, err
			}
			present = append(present, condition)
		}
	}
	presence, err := logical(projector.root, ir.LogicalAnd, present)
	if err != nil {
		return bounds{}, err
	}
	truth, _ := ir.NewConstant(projector.root, true)
	falsehood, _ := ir.NewConstant(projector.root, false)
	value := bounds{lower: truth, upper: truth}
	if child != nil {
		value, err = projector.project(*child, childMapping)
		if err != nil {
			return bounds{}, err
		}
	}
	match, err := logical(projector.root, ir.LogicalAnd, []ir.Condition{presence, value.upper})
	if err != nil {
		return bounds{}, err
	}
	operation, _ := input.Operator()

	switch operation {
	case ir.OperatorRelationSome, ir.OperatorRelationIs:
		return bounds{lower: falsehood, upper: match}, nil
	case ir.OperatorRelationNone, ir.OperatorRelationIsNot:
		absent, err := negate(match)
		return bounds{lower: absent, upper: truth}, err
	case ir.OperatorRelationEvery:
		absent, err := negate(presence)
		if err != nil {
			return bounds{}, err
		}
		lower, err := logical(projector.root, ir.LogicalOr, []ir.Condition{absent, value.lower})
		return bounds{lower: lower, upper: truth}, err
	case ir.OperatorRelationIsNull:
		absent, err := negate(presence)
		return bounds{lower: absent, upper: truth}, err
	case ir.OperatorRelationIsNotNull:
		return bounds{lower: falsehood, upper: presence}, nil
	default:
		return bounds{}, fmt.Errorf("policy observation: unsupported witness relationship")
	}
}
