package observation

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func TestUnknownFactBoundsPreserveNegativeAndConstantGuards(t *testing.T) {
	root := ir.ModelID{1}
	owner := boolean(t, root, ir.FieldID{1})
	id := must(ir.FactIdentity(root, "membership"))
	fact := must(ir.NewFact(root, id, boolean(t, root, ir.FieldID{2}), unrestrictedFactScope(t, root)))
	negative := must(negate(fact))
	for _, expression := range []ir.Condition{fact, negative,
		must(logical(root, ir.LogicalAnd, []ir.Condition{owner, fact})),
		must(logical(root, ir.LogicalAnd, []ir.Condition{owner, negative})),
		must(negate(must(logical(root, ir.LogicalOr, []ir.Condition{owner, fact})))),
	} {
		projected := must(project(expression, opaqueFacts))
		if ir.HasFacts(projected.lower) || ir.HasFacts(projected.upper) {
			t.Fatal("fact provenance escaped into a source-scope query")
		}
		for _, owns := range []bool{false, true} {
			for _, member := range []bool{false, true} {
				record := must(evaluate.NewRecord(root,
					must(evaluate.ValueField(ir.FieldID{1}, ir.BoolValue(owns))),
					must(evaluate.ValueField(ir.FieldID{2}, ir.BoolValue(member))),
				))
				actual := must(evaluate.Condition(expression, record, ir.PortableProviders()))
				lower := must(evaluate.Condition(projected.lower, record, ir.PortableProviders()))
				upper := must(evaluate.Condition(projected.upper, record, ir.PortableProviders()))
				if lower && !actual || actual && !upper {
					t.Fatalf("unsound bounds: lower=%v actual=%v upper=%v", lower, actual, upper)
				}
			}
		}
	}
	falsehood := must(ir.NewConstant(root, false))
	denied := must(ir.NewFact(root, id, falsehood, unrestrictedFactScope(t, root)))
	projected := must(project(denied, opaqueFacts))
	if value, known := projected.upper.Constant(); !known || value {
		t.Fatal("an actor's constant false fact became possible authority")
	}
}

func TestFactBoundsRespectRelationQuantifierPolarity(t *testing.T) {
	root, target := ir.ModelID{1}, ir.ModelID{2}
	id := must(ir.FactIdentity(target, "enabled"))
	child := must(ir.NewFact(target, id, boolean(t, target, ir.FieldID{2}), unrestrictedFactScope(t, target)))
	for _, operation := range []ir.OperatorID{ir.OperatorRelationSome, ir.OperatorRelationNone, ir.OperatorRelationEvery, ir.OperatorRelationIs, ir.OperatorRelationIsNot} {
		cardinality := ir.RelationToMany
		if operation == ir.OperatorRelationIs || operation == ir.OperatorRelationIsNot {
			cardinality = ir.RelationToOne
		}
		requirements := must(operator.ValidateShape(operation, operator.Shape{Node: ir.ConditionRelation, Cardinality: cardinality, HasChild: true, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()}))
		relation := must(ir.NewRelation(root, ir.FieldID{3}, ir.RelationID{1}, target, cardinality, operation, &child, requirements))
		for _, expression := range []ir.Condition{relation, must(negate(relation))} {
			projected := must(project(expression, opaqueFacts))
			for _, values := range [][]bool{nil, {false}, {true}, {false, true}} {
				if cardinality == ir.RelationToOne && len(values) > 1 {
					continue
				}
				var rows []evaluate.Record
				for _, value := range values {
					rows = append(rows, must(evaluate.NewRecord(target, must(evaluate.ValueField(ir.FieldID{2}, ir.BoolValue(value))))))
				}
				field := must(evaluate.ToManyField(ir.FieldID{3}, target, rows...))
				if cardinality == ir.RelationToOne {
					field = must(evaluate.ToOneField(ir.FieldID{3}, target, rows...))
				}
				record := must(evaluate.NewRecord(root, field))
				actual := must(evaluate.Condition(expression, record, ir.PortableProviders()))
				lower := must(evaluate.Condition(projected.lower, record, ir.PortableProviders()))
				upper := must(evaluate.Condition(projected.upper, record, ir.PortableProviders()))
				if lower && !actual || actual && !upper {
					t.Fatalf("operation=%d values=%v lower=%v actual=%v upper=%v", operation, values, lower, actual, upper)
				}
			}
		}
	}
}

func boolean(t *testing.T, model ir.ModelID, field ir.FieldID) ir.Condition {
	t.Helper()
	typ := must(ir.NewTypeRef(ir.ValueBool, false, 0, 0, ir.EnumID{}, nil, 0))
	operand := must(ir.OneOperand(ir.BoolValue(true)))
	requirements := must(operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()}))
	return must(ir.NewScalar(model, field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, requirements))
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
