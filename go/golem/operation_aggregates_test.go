package golem

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func aggregateRegistrations() []GeneratedOperation {
	return []GeneratedOperation{
		GeneratedTxOperation("operation-create", operationCreateResolver),
		GeneratedCustomMutationOperation("operation-other", operationOtherResolver),
	}
}

func TestAggregateGrantsLeaveTheBaseAndWritePoliciesByteIdentical(t *testing.T) {
	field := GeneratedEqualField[bindingModel, bool](operationFieldID)
	plain := func(rules *Rules[bindingModel]) {
		rules.CanRead(field.Eq(true))
		Within(rules, operationCreateResolver).CanCreate(field.Eq(true))
	}
	base := NewRules[bindingModel]()
	plain(base)
	counted := NewRules[bindingModel]()
	plain(counted)
	Within(counted, operationCreateResolver).CanCount(All[bindingModel]())
	Within(counted, operationCreateResolver).CanAggregate(All[bindingModel](), field)
	want, err := base.Freeze(operationModelID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := counted.Freeze(operationModelID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want.CanonicalBytes(), got.CanonicalBytes()) || len(got.View().Rules()) != 1 {
		t.Fatal("aggregate grants changed the base policy")
	}
	application := operationBindings(t, func(bindingActor) (FrozenPolicy, error) { return got, nil }, aggregateRegistrations()...)
	_, wantOperations, err := BuildGeneratedModelPolicy(operationBindings(t, func(bindingActor) (FrozenPolicy, error) { return want, nil }, aggregateRegistrations()...), operationModelID, bindingActor{})
	if err != nil {
		t.Fatal(err)
	}
	_, gotOperations, err := BuildGeneratedModelPolicy(application, operationModelID, bindingActor{})
	if err != nil {
		t.Fatal(err)
	}
	for operation, policy := range wantOperations {
		if !bytes.Equal(policy.CanonicalBytes(), gotOperations[operation].CanonicalBytes()) {
			t.Fatal("aggregate grants changed an operation's write policy")
		}
	}
	empty, err := GeneratedAggregatePolicies(application, want)
	if err != nil || len(empty) != 0 {
		t.Fatalf("a policy without aggregate grants produced aggregate policies: %d, %v", len(empty), err)
	}
}

func TestAggregatePoliciesAppendRowOnlyCountsAndNamedFieldGrants(t *testing.T) {
	field := GeneratedEqualField[bindingModel, bool](operationFieldID)
	other := GeneratedEqualField[bindingModel, bool](operationOtherField)
	rules := NewRules[bindingModel]()
	rules.CanRead(field.Eq(true))
	Within(rules, operationCreateResolver).CanCount(other.Eq(true))
	Within(rules, operationCreateResolver).CanCreate(All[bindingModel]())
	Within(rules, operationCreateResolver).CanAggregate(other.Eq(false), field, field)
	frozen, err := rules.Freeze(operationModelID)
	if err != nil {
		t.Fatal(err)
	}
	application := operationBindings(t, func(bindingActor) (FrozenPolicy, error) { return frozen, nil }, aggregateRegistrations()...)
	aggregates, err := GeneratedAggregatePolicies(application, frozen)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := application.TxOperation(operationCreateResolver)
	merged, ok := aggregates[identity]
	if len(aggregates) != 1 || !ok {
		t.Fatalf("aggregate policies = %d", len(aggregates))
	}
	views := merged.View().Rules()
	if len(views) != 3 {
		t.Fatalf("aggregate policy rules = %d, want base read plus two aggregate grants", len(views))
	}
	for index, view := range views {
		if view.Position() != uint32(index) || view.Action() != FrozenActionRead || view.Effect() != FrozenEffectGrant {
			t.Fatalf("aggregate rule %d = (position=%d action=%d effect=%d)", index, view.Position(), view.Action(), view.Effect())
		}
	}
	if fields, modelWide := views[1].Fields(); modelWide || len(fields) != 0 {
		t.Fatalf("CanCount froze as fields=%x modelWide=%t, want a row-only grant", fields, modelWide)
	}
	if fields, modelWide := views[2].Fields(); modelWide || len(fields) != 1 || fields[0] != operationFieldID {
		t.Fatalf("CanAggregate froze as fields=%x modelWide=%t", fields, modelWide)
	}
	modelWide := NewRules[bindingModel]()
	modelWide.CanRead(field.Eq(true))
	modelWide.CanRead(other.Eq(true))
	modelWide.CanReadFields(other.Eq(false), field)
	wide, err := modelWide.Freeze(operationModelID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(wide.CanonicalBytes(), merged.CanonicalBytes()) {
		t.Fatal("a row-only count grant encodes like a model-wide read grant")
	}
	again, err := GeneratedAggregatePolicies(application, frozen)
	if err != nil || !bytes.Equal(again[identity].CanonicalBytes(), merged.CanonicalBytes()) {
		t.Fatalf("aggregate policies are not stable: %v", err)
	}
	if write := frozen.operations; len(write) != 1 || len(write[0].rules) != 1 || write[0].rules[0].action != FrozenActionCreate {
		t.Fatal("aggregate grants leaked into the operation's write grants")
	}
}

func TestAggregateGrantsNamingAnythingButATransactionOperationFailTheBuild(t *testing.T) {
	field := GeneratedEqualField[bindingModel, bool](operationFieldID)
	for name, grant := range map[string]func(*Rules[bindingModel]){
		"operationOtherResolver": func(rules *Rules[bindingModel]) {
			Within(rules, operationOtherResolver).CanCount(All[bindingModel]())
		},
		"operationUnregisteredResolver": func(rules *Rules[bindingModel]) {
			Within(rules, operationUnregisteredResolver).CanAggregate(All[bindingModel](), field)
		},
		"string": func(rules *Rules[bindingModel]) {
			Within(rules, "countThings").CanCount(All[bindingModel]())
		},
	} {
		t.Run(name, func(t *testing.T) {
			factory := func(bindingActor) (FrozenPolicy, error) {
				rules := NewRules[bindingModel]()
				rules.CanRead(All[bindingModel]())
				grant(rules)
				return rules.Freeze(operationModelID)
			}
			application := operationBindings(t, factory, aggregateRegistrations()...)
			if _, err := BuildGeneratedPolicySet(application, bindingActor{}); err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "transaction operation") {
				t.Fatalf("policy set error=%v", err)
			}
			policy, _, err := BuildGeneratedModelPolicy(application, operationModelID, bindingActor{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := GeneratedAggregatePolicies(application, policy); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("aggregate policies error=%v", err)
			}
		})
	}
}

func TestAggregateGrantsOutsideWithinAreRefusedAtFreeze(t *testing.T) {
	operation := operationReferenceOf(operationCreateResolver)
	for name, rule := range map[string]ruleBuilder{
		"aggregate without an operation":   {action: frozenActionRead, effect: frozenEffectGrant, condition: All[bindingModel]().node, aggregate: true},
		"aggregate field grant outside":    {action: frozenActionRead, effect: frozenEffectGrant, condition: All[bindingModel]().node, fields: []FieldID{operationFieldID}, aggregate: true},
		"aggregate write grant":            {action: frozenActionCreate, effect: frozenEffectGrant, condition: All[bindingModel]().node, operation: &operation, aggregate: true},
		"aggregate denial":                 {action: frozenActionRead, effect: frozenEffectDeny, condition: All[bindingModel]().node, operation: &operation, aggregate: true},
		"operation read without aggregate": {action: frozenActionRead, effect: frozenEffectGrant, condition: All[bindingModel]().node, operation: &operation},
	} {
		t.Run(name, func(t *testing.T) {
			rules := NewRules[bindingModel]()
			rules.CanRead(All[bindingModel]())
			rules.appendRule(rule)
			_, err := rules.Freeze(operationModelID)
			var failure *FreezeError
			if !errors.As(err, &failure) || failure.Code != FreezeInvalidRule || !failure.HasRule || failure.RulePosition != 1 {
				t.Fatalf("freeze error=%v", err)
			}
		})
	}
}
