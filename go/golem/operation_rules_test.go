package golem

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type operationArguments struct{ Value string }

func operationCreateResolver(context.Context, *bindingModel, operationArguments) (bool, error) {
	return true, nil
}

func operationOtherResolver(context.Context, *bindingModel, operationArguments) (bool, error) {
	return true, nil
}

func operationUnregisteredResolver(context.Context, *bindingModel, operationArguments) (bool, error) {
	return true, nil
}

var (
	operationModelID     = ModelID{0x51}
	operationFieldID     = FieldID{0x52}
	operationOtherField  = FieldID{0x53}
	operationGenerations = SchemaDigest{0x54}
)

func operationBindings(t *testing.T, factory PolicyFactory[bindingActor], operations ...GeneratedOperation) ApplicationBindings[bindingActor] {
	t.Helper()
	application, err := GeneratedApplicationBindings(operationGenerations, GeneratedStampedPackageBindings(operationGenerations, []PolicyBinding[bindingActor]{GeneratedPolicyBinding[bindingActor, bindingModel](operationModelID, factory)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) == 0 {
		return application
	}
	application, err = GeneratedApplicationOperations(application, operations...)
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func operationRegistrations() []GeneratedOperation {
	return []GeneratedOperation{
		GeneratedCustomMutationOperation("operation-create", operationCreateResolver),
		GeneratedCustomMutationOperation("operation-other", operationOtherResolver),
	}
}

func TestWithinGrantsLeaveTheBasePolicyByteIdentical(t *testing.T) {
	field := GeneratedEqualField[bindingModel, bool](operationFieldID)
	base := NewRules[bindingModel]()
	base.CanRead(All[bindingModel]())
	base.CannotCreate(All[bindingModel]())
	scoped := NewRules[bindingModel]()
	scoped.CanRead(All[bindingModel]())
	Within(scoped, operationCreateResolver).CanCreate(field.Eq(true))
	scoped.CannotCreate(All[bindingModel]())
	Within(scoped, operationOtherResolver).CanUpdateFields(All[bindingModel](), field)
	want, err := base.Freeze(operationModelID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scoped.Freeze(operationModelID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want.CanonicalBytes(), got.CanonicalBytes()) || len(got.View().Rules()) != 2 {
		t.Fatalf("Within grants leaked into the base policy: rules=%d", len(got.View().Rules()))
	}
}

func TestOperationPoliciesAppendOnlyThatOperationsGrantsAfterTheBaseRules(t *testing.T) {
	field := GeneratedEqualField[bindingModel, bool](operationFieldID)
	other := GeneratedEqualField[bindingModel, bool](operationOtherField)
	factory := func(bindingActor) (FrozenPolicy, error) {
		rules := NewRules[bindingModel]()
		rules.CanRead(All[bindingModel]())
		Within(rules, operationCreateResolver).CanCreate(field.Eq(true))
		rules.CannotUpdateFields(All[bindingModel](), field, other)
		Within(rules, operationCreateResolver).CanUpdateFields(All[bindingModel](), field)
		Within(rules, operationOtherResolver).CanDelete(All[bindingModel]())
		return rules.Freeze(operationModelID)
	}
	application := operationBindings(t, factory, operationRegistrations()...)
	set, err := BuildGeneratedPolicySet(application, bindingActor{})
	if err != nil {
		t.Fatal(err)
	}
	createID, ok := application.Operation(operationCreateResolver)
	if !ok {
		t.Fatal("registered resolver has no operation identity")
	}
	otherID, ok := application.Operation(operationOtherResolver)
	if !ok || otherID == createID {
		t.Fatalf("distinct resolvers share or lack identities: ok=%t", ok)
	}
	if operations := set.Operations(); len(operations) != 2 {
		t.Fatalf("operations=%d want=2", len(operations))
	}
	merged := set.OperationPolicies(createID)
	if len(merged) != 1 || merged[0].View().ModelID() != operationModelID {
		t.Fatalf("create operation policies=%d", len(merged))
	}
	rules := merged[0].View().Rules()
	wantActions := []FrozenAction{FrozenActionRead, FrozenActionUpdate, FrozenActionCreate, FrozenActionUpdate}
	wantEffects := []FrozenEffect{FrozenEffectGrant, FrozenEffectDeny, FrozenEffectGrant, FrozenEffectGrant}
	if len(rules) != len(wantActions) {
		t.Fatalf("merged rules=%d want=%d", len(rules), len(wantActions))
	}
	for index, rule := range rules {
		if rule.Position() != uint32(index) || rule.Action() != wantActions[index] || rule.Effect() != wantEffects[index] {
			t.Fatalf("merged rule %d = (position=%d action=%d effect=%d)", index, rule.Position(), rule.Action(), rule.Effect())
		}
	}
	if fields, modelWide := rules[3].Fields(); modelWide || len(fields) != 1 || fields[0] != operationFieldID {
		t.Fatalf("Within field grant fields=%x modelWide=%t", fields, modelWide)
	}
	for _, rule := range set.OperationPolicies(otherID)[0].View().Rules() {
		if rule.Action() == FrozenActionCreate {
			t.Fatal("another operation's create grant leaked into this operation")
		}
	}
	if base := set.Policies()[0].View().Rules(); len(base) != 2 {
		t.Fatalf("base policy carries %d rules, want the 2 caller rules", len(base))
	}
	again := set.OperationPolicies(createID)
	if !bytes.Equal(again[0].CanonicalBytes(), merged[0].CanonicalBytes()) {
		t.Fatal("operation policies are not stable")
	}
}

func TestWithinAnUnregisteredResolverFailsThePolicyBuildNamingIt(t *testing.T) {
	for name, grant := range map[string]func(*Rules[bindingModel]){
		"operationUnregisteredResolver": func(rules *Rules[bindingModel]) {
			Within(rules, operationUnregisteredResolver).CanCreate(All[bindingModel]())
		},
		"string": func(rules *Rules[bindingModel]) {
			Within(rules, "createThing").CanCreate(All[bindingModel]())
		},
		"nil": func(rules *Rules[bindingModel]) {
			var resolver func(context.Context, *bindingModel, operationArguments) (bool, error)
			Within(rules, resolver).CanUpdate(All[bindingModel]())
		},
	} {
		t.Run(name, func(t *testing.T) {
			factory := func(bindingActor) (FrozenPolicy, error) {
				rules := NewRules[bindingModel]()
				rules.CanRead(All[bindingModel]())
				grant(rules)
				return rules.Freeze(operationModelID)
			}
			_, err := BuildGeneratedPolicySet(operationBindings(t, factory, operationRegistrations()...), bindingActor{})
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "custom mutation") {
				t.Fatalf("unregistered Within error=%v", err)
			}
		})
	}
	factory := func(bindingActor) (FrozenPolicy, error) {
		rules := NewRules[bindingModel]()
		Within(rules, operationCreateResolver).CanCreate(All[bindingModel]())
		return rules.Freeze(operationModelID)
	}
	if _, err := BuildGeneratedPolicySet(operationBindings(t, factory), bindingActor{}); err == nil || !strings.Contains(err.Error(), "operationCreateResolver") {
		t.Fatalf("Within with no generated operations table error=%v", err)
	}
}

func TestGeneratedApplicationOperationsRejectMalformedTables(t *testing.T) {
	application := operationBindings(t, func(bindingActor) (FrozenPolicy, error) { return NewRules[bindingModel]().Freeze(operationModelID) })
	for name, operations := range map[string][]GeneratedOperation{
		"duplicate resolver": {GeneratedCustomMutationOperation("a", operationCreateResolver), GeneratedCustomMutationOperation("b", operationCreateResolver)},
		"duplicate identity": {GeneratedCustomMutationOperation("a", operationCreateResolver), GeneratedCustomMutationOperation("a", operationOtherResolver)},
		"empty identity":     {GeneratedCustomMutationOperation("", operationCreateResolver)},
		"non-function":       {GeneratedCustomMutationOperation("a", "resolver")},
		"nil function":       {GeneratedCustomMutationOperation[func()]("a", nil)},
		"zero value":         {{}},
	} {
		if result, err := GeneratedApplicationOperations(application, operations...); err == nil {
			t.Fatalf("%s: malformed operations table was accepted", name)
		} else if _, ok := result.Operation(operationCreateResolver); ok {
			t.Fatalf("%s: rejected table still resolved an operation", name)
		}
	}
	if _, err := GeneratedApplicationOperations(ApplicationBindings[bindingActor]{}, operationRegistrations()...); err == nil {
		t.Fatal("operations were attached to unstamped bindings")
	}
}

func TestApplicationBindingsResolveOnlyRegisteredOperationResolvers(t *testing.T) {
	application := operationBindings(t, func(bindingActor) (FrozenPolicy, error) { return NewRules[bindingModel]().Freeze(operationModelID) }, operationRegistrations()...)
	if _, ok := application.Operation(operationCreateResolver); !ok {
		t.Fatal("registered resolver did not resolve")
	}
	var nilResolver func(context.Context, *bindingModel, operationArguments) (bool, error)
	wrapped := func(ctx context.Context, model *bindingModel, arguments operationArguments) (bool, error) {
		return operationCreateResolver(ctx, model, arguments)
	}
	for name, candidate := range map[string]any{
		"unregistered": operationUnregisteredResolver,
		"wrapper":      wrapped,
		"nil":          nilResolver,
		"untyped nil":  nil,
		"string":       "operation-create",
	} {
		if _, ok := application.Operation(candidate); ok {
			t.Fatalf("%s resolved to a registered operation", name)
		}
	}
	unscoped := operationBindings(t, func(bindingActor) (FrozenPolicy, error) { return NewRules[bindingModel]().Freeze(operationModelID) })
	if _, ok := unscoped.Operation(operationCreateResolver); ok {
		t.Fatal("bindings without an operations table resolved an operation")
	}
}

func TestOperationRulesOfferOnlyWriteGrants(t *testing.T) {
	var rules *OperationRules[bindingModel]
	var _ func(Predicate[bindingModel]) = rules.CanCreate
	var _ func(Predicate[bindingModel]) = rules.CanUpdate
	var _ func(Predicate[bindingModel]) = rules.CanDelete
	var _ func(Predicate[bindingModel], Field[bindingModel], ...Field[bindingModel]) = rules.CanCreateFields
	var _ func(Predicate[bindingModel], Field[bindingModel], ...Field[bindingModel]) = rules.CanUpdateFields
}
