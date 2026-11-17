package golem

import (
	"bytes"
	"testing"
)

func TestFactFreezeOwnsActorScopeAndDefinitionsAcrossPolicyCopies(t *testing.T) {
	owner := GeneratedEqualField[freezeModel, int64](freezeFieldID)
	var previous []byte
	for _, actor := range []int64{7, 8} {
		rules := NewRules[freezeModel]()
		rules.FactScope(owner.Eq(actor))
		ready := rules.Fact("ready", All[freezeModel]())
		visible := rules.Fact("visible", ready.And(owner.Eq(actor)))
		rules.CanRead(visible)
		frozen, err := rules.Freeze(freezeModelID)
		if err != nil {
			t.Fatal(err)
		}
		if frozen.Version() != 3 || len(frozen.View().Facts()) != 2 {
			t.Fatal("fact policy lost its version or owned definitions")
		}
		if previous != nil && bytes.Equal(previous, frozen.CanonicalBytes()) {
			t.Fatal("actor scope was omitted from policy identity")
		}
		previous = frozen.CanonicalBytes()
		rules.Fact("later", All[freezeModel]())
		if len(frozen.View().Facts()) != 2 {
			t.Fatal("frozen definitions aliased the mutable builder")
		}
		operation := frozenOperationRules{rules: []frozenRule{{action: FrozenActionUpdate, effect: FrozenEffectGrant, model: freezeModelID, unconditional: true}}}
		merged, err := mergeOperationPolicy(frozen, operation)
		if err != nil {
			t.Fatal(err)
		}
		set := GeneratedPolicySet{policies: []FrozenPolicy{frozen}, operations: map[OperationID][]FrozenPolicy{{1}: {merged}}}
		for _, copy := range []FrozenPolicy{merged, set.Policies()[0], set.OperationPolicies(OperationID{1})[0]} {
			scope, present := copy.View().FactScope()
			if !present || copy.Version() != 3 || len(copy.View().Facts()) != 2 {
				t.Fatal("generated or operation policy copy dropped fact authority")
			}
			operand, _ := scope.Root().Operand().One()
			if value, _, ok := operand.Signed(); !ok || value != actor {
				t.Fatal("copied scope retained a different actor")
			}
		}
	}
}

func TestFactFreezeRejectsImplicitOrBorrowedAuthority(t *testing.T) {
	for _, name := range []string{"missing", "duplicate-scope", "negative-related-scope", "fact-scope", "duplicate-fact", "invalid-name", "borrowed", "unused-scope"} {
		t.Run(name, func(t *testing.T) {
			rules := NewRules[freezeModel]()
			if name != "missing" && name != "negative-related-scope" && name != "fact-scope" {
				rules.FactScope(All[freezeModel]())
			}
			if name != "unused-scope" {
				rules.Fact("ready", All[freezeModel]())
			}
			switch name {
			case "duplicate-scope":
				rules.FactScope(All[freezeModel]())
			case "negative-related-scope":
				relation := GeneratedToOne[freezeModel, freezeRelated](freezeRelationField, freezeRelationID, freezeRelatedID)
				rules.FactScope(relation.Is(All[freezeRelated]()).Not())
			case "fact-scope":
				rules.FactScope(rules.Fact("scope", All[freezeModel]()))
			case "duplicate-fact":
				rules.Fact("ready", All[freezeModel]())
			case "invalid-name":
				rules.Fact(" padded ", All[freezeModel]())
			case "borrowed":
				other := NewRules[freezeModel]()
				borrowed := other.Fact("ready", All[freezeModel]())
				rules.CanRead(borrowed.And(None[freezeModel]()))
			}
			if _, err := rules.Freeze(freezeModelID); err == nil {
				t.Fatal("invalid fact authority was accepted")
			}
		})
	}
}

func TestFactProvenanceSurvivesDeadCallerBranches(t *testing.T) {
	rules := NewRules[freezeModel]()
	fact := rules.Fact("ready", All[freezeModel]())
	for _, predicate := range []Predicate[freezeModel]{fact.And(None[freezeModel]()), fact.Or(All[freezeModel]()), fact.Not().Not()} {
		frozen, err := predicate.Freeze(freezeDescriptor)
		if err != nil {
			t.Fatal(err)
		}
		if frozen.Version() != 3 || !frozenHasFacts(frozen.root) {
			t.Fatal("Boolean normalization erased forbidden caller provenance")
		}
	}
	for _, predicate := range []Predicate[freezeModel]{And(fact), fact.And(), fact.Or()} {
		frozen, err := predicate.Freeze(freezeDescriptor)
		if err != nil {
			t.Fatal(err)
		}
		if frozen.View().Root().Kind() != FrozenConditionFactReference {
			t.Fatal("single-child composition lost its normalized fact reference")
		}
	}
}

func TestFactDeclarationsRetainDeadPhysicalBranchesThroughTheSharedFreezer(t *testing.T) {
	many := GeneratedToMany[freezeModel, freezeRelated](freezeRelationField, freezeRelationID, freezeRelatedID)
	child := GeneratedEqualField[freezeRelated, int64](freezeSecondFieldID)
	physical := many.Some(child.Eq(7))
	for _, test := range []struct {
		name      string
		predicate Predicate[freezeModel]
		operator  FrozenOperator
		truth     bool
	}{
		{"absorbing-or", All[freezeModel]().Or(physical), FrozenOperatorOr, true},
		{"absorbing-and", None[freezeModel]().And(physical), FrozenOperatorAnd, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := NewRules[freezeModel]()
			rules.FactScope(test.predicate)
			rules.Fact("unused", test.predicate)
			rules.CanRead(All[freezeModel]())
			frozen, err := rules.Freeze(freezeModelID)
			if err != nil {
				t.Fatal(err)
			}
			scope, present := frozen.View().FactScope()
			if !present {
				t.Fatal("fact scope missing")
			}
			for _, declaration := range []FrozenPredicateView{scope, frozen.View().Facts()[0].Body()} {
				root := declaration.Root()
				if root.Kind() != FrozenConditionLogical || root.Operator() != test.operator || len(root.Children()) != 2 {
					t.Fatal("authored declaration lost its multi-child Boolean structure")
				}
				physical := root.Children()[1]
				if physical.Kind() != FrozenConditionRelation || physical.Operator() != FrozenOperatorRelationSome || len(physical.Children()) != 1 {
					t.Fatal("dead physical branch disappeared before registry admission")
				}
				if field, present := physical.Children()[0].FieldID(); !present || field != freezeSecondFieldID {
					t.Fatal("recursive declaration freezing lost its physical leaf")
				}
			}
			ordinary, err := test.predicate.Freeze(freezeDescriptor)
			if err != nil {
				t.Fatal(err)
			}
			truth, present := ordinary.View().Root().Operand().Flag()
			if ordinary.View().Root().Kind() != FrozenConditionConstant || !present || truth != test.truth {
				t.Fatal("ordinary predicate no longer applies its existing Boolean normalization")
			}
		})
	}

	var prior []byte
	for _, value := range []int64{7, 8} {
		rules := NewRules[freezeModel]()
		rules.FactScope(All[freezeModel]())
		// The inner absorbing expression must also survive a relation hop.
		rules.Fact("nested", many.Some(All[freezeRelated]().Or(child.Eq(value))))
		rules.CanRead(All[freezeModel]())
		frozen, err := rules.Freeze(freezeModelID)
		if err != nil {
			t.Fatal(err)
		}
		root := frozen.View().Facts()[0].Body().Root()
		if len(root.Children()) != 1 || root.Children()[0].Kind() != FrozenConditionLogical || len(root.Children()[0].Children()) != 2 {
			t.Fatal("declaration preservation did not recurse through a relation child")
		}
		if prior != nil && bytes.Equal(prior, frozen.CanonicalBytes()) {
			t.Fatal("different authored physical leaves lost their canonical declaration identity")
		}
		prior = frozen.CanonicalBytes()
	}
}

func TestFactDeclarationFreezingKeepsExistingArityAndNotRules(t *testing.T) {
	leaf := GeneratedEqualField[freezeModel, int64](freezeFieldID).Eq(7)
	for _, test := range []struct {
		name      string
		predicate Predicate[freezeModel]
		kind      FrozenConditionKind
	}{
		{"empty-and", And[freezeModel](), FrozenConditionConstant},
		{"empty-or", Or[freezeModel](), FrozenConditionConstant},
		{"one-and", And(leaf), FrozenConditionScalar},
		{"one-or", Or(leaf), FrozenConditionScalar},
		{"method-empty-and", leaf.And(), FrozenConditionScalar},
		{"method-empty-or", leaf.Or(), FrozenConditionScalar},
		{"double-not", leaf.Not().Not(), FrozenConditionScalar},
		{"constant-not", All[freezeModel]().Not(), FrozenConditionConstant},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := NewRules[freezeModel]()
			rules.FactScope(All[freezeModel]())
			rules.Fact("body", test.predicate)
			frozen, err := rules.Freeze(freezeModelID)
			if err != nil {
				t.Fatal(err)
			}
			if got := frozen.View().Facts()[0].Body().Root().Kind(); got != test.kind {
				t.Fatalf("declaration kind=%v want=%v", got, test.kind)
			}
		})
	}
	for _, inScope := range []bool{false, true} {
		rules := NewRules[freezeModel]()
		malformed := All[freezeModel]().Or(Predicate[freezeModel]{})
		if inScope {
			rules.FactScope(malformed)
			rules.Fact("body", All[freezeModel]())
		} else {
			rules.FactScope(All[freezeModel]())
			rules.Fact("body", malformed)
		}
		if _, err := rules.Freeze(freezeModelID); err == nil {
			t.Fatal("declaration mode stopped validating a malformed Boolean-dead child")
		}
	}
}
