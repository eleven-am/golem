package golem

import "testing"

func TestFactScopeFreezePreservesPositiveRelatedPolarity(t *testing.T) {
	one := GeneratedToOne[freezeModel, freezeRelated](freezeRelationField, freezeRelationID, freezeRelatedID)
	many := GeneratedToMany[freezeModel, freezeRelated](freezeRelationField, freezeRelationID, freezeRelatedID)
	nested := GeneratedToOne[freezeRelated, freezeRelated](FieldID{0x24}, RelationID{0x32}, freezeRelatedID)
	actor := GeneratedEqualField[freezeRelated, int64](freezeFieldID).Eq(7)
	for _, test := range []struct {
		name      string
		scope     Predicate[freezeModel]
		operation FrozenOperator
	}{
		{"is", one.Is(actor), FrozenOperatorRelationIs},
		{"some", many.Some(actor), FrozenOperatorRelationSome},
		{"double-negative-is", one.Is(actor).Not().Not(), FrozenOperatorRelationIs},
		{"double-negative-some", many.Some(actor).Not().Not(), FrozenOperatorRelationSome},
		{"negative-scalar-child", many.Some(actor.Not()), FrozenOperatorRelationSome},
		{"nested-positive-is", many.Some(nested.Is(actor)), FrozenOperatorRelationSome},
		{"nested-double-negative-is", many.Some(nested.Is(actor).Not().Not()), FrozenOperatorRelationSome},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := NewRules[freezeModel]()
			rules.FactScope(test.scope)
			rules.Fact("unused", All[freezeModel]())
			rules.CanRead(All[freezeModel]())
			frozen, err := rules.Freeze(freezeModelID)
			if err != nil {
				t.Fatal(err)
			}
			scope, present := frozen.View().FactScope()
			if !present || scope.Root().Kind() != FrozenConditionRelation || scope.Root().Operator() != test.operation {
				t.Fatal("freezing lost the positive related authority path")
			}
		})
	}
}

func TestFactScopeFreezeRejectsNegativeRelatedWitnesses(t *testing.T) {
	one := GeneratedToOne[freezeModel, freezeRelated](freezeRelationField, freezeRelationID, freezeRelatedID)
	many := GeneratedToMany[freezeModel, freezeRelated](freezeRelationField, freezeRelationID, freezeRelatedID)
	nestedOne := GeneratedToOne[freezeRelated, freezeRelated](FieldID{0x24}, RelationID{0x32}, freezeRelatedID)
	nestedMany := GeneratedToMany[freezeRelated, freezeRelated](FieldID{0x25}, RelationID{0x33}, freezeRelatedID)
	child := All[freezeRelated]()
	for _, test := range []struct {
		name  string
		scope Predicate[freezeModel]
	}{
		{"not-is", one.Is(child).Not()},
		{"not-some", many.Some(child).Not()},
		{"some-not-is", many.Some(nestedOne.Is(child).Not())},
		{"is-not-some", one.Is(nestedMany.Some(child).Not())},
		{"none", many.None(child)},
		{"every", many.Every(child)},
		{"is-not", one.IsNot(child)},
		{"double-negative-none", many.None(child).Not().Not()},
		{"double-negative-every", many.Every(child).Not().Not()},
		{"double-negative-is-not", one.IsNot(child).Not().Not()},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := NewRules[freezeModel]()
			rules.FactScope(test.scope)
			rules.Fact("unused", All[freezeModel]())
			rules.CanRead(All[freezeModel]())
			if _, err := rules.Freeze(freezeModelID); err == nil {
				t.Fatal("freezing accepted a negative or unsupported related authority witness")
			}
		})
	}
}
