package plan

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func hookCreateInput(t *testing.T, fixture schematest.Fixture, hookAuthored ...golem.FieldID) mutationbind.ScalarInput {
	t.Helper()
	id := golem.GeneratedEqualField[testPost, golem.UUID](fixture.PostID)
	author := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	uuid := golem.NewUUID([16]byte{4})
	frozen, err := golem.RuntimeFreezeCreateInput(golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, author, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, title, "hook wrote this"),
	))
	if err != nil {
		t.Fatal(err)
	}
	bound, _, err := mutationbind.CreateInputFromHook(frozen, fixture.Registry, nil, hookAuthored)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func hookUpdateInput(t *testing.T, fixture schematest.Fixture, hookAuthored ...golem.FieldID) mutationbind.ScalarInput {
	t.Helper()
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	frozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "hook wrote this")))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := mutationbind.UpdateInputFromHook(frozen, fixture.Registry, hookAuthored)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func TestCallerPlanAcceptsHookAuthoredSystemField(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	policies := policySet(fixture, allowAllPolicy(t, policyir.ModelID(fixture.Post)))
	inputs := boundInputs(t, fixture)

	create := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Create)
	created := hookCreateInput(t, fixture, fixture.PostTitle)
	create.Create = &created
	if _, err := BuildRoot(create); err != nil {
		t.Fatalf("hook-authored create was refused a system field: %v", err)
	}

	update := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Update)
	updated := hookUpdateInput(t, fixture, fixture.PostTitle)
	update.Target, update.Update = &inputs.target, &updated
	if _, err := BuildRoot(update); err != nil {
		t.Fatalf("hook-authored update was refused a system field: %v", err)
	}
}

func denyFieldRule(t *testing.T, model policyir.ModelID, action policyir.Action, field policyir.FieldID, position uint32) policyir.Rule {
	t.Helper()
	value, err := policyir.NewFieldRule(action, policyir.EffectDeny, model, nil, field, nil, position)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPolicyDenialOfASystemFieldDoesNotReachTheHookThatOwnsIt(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	model := policyir.ModelID(fixture.Post)
	denying := policyWithRules(t, model,
		rule(t, model, policyir.ActionRead, nil, 0),
		rule(t, model, policyir.ActionCreate, nil, 1),
		rule(t, model, policyir.ActionUpdate, nil, 2),
		denyFieldRule(t, model, policyir.ActionCreate, policyir.FieldID(fixture.PostTitle), 3),
		denyFieldRule(t, model, policyir.ActionUpdate, policyir.FieldID(fixture.PostTitle), 4),
	)
	policies := policySet(fixture, denying)

	create := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Create)
	created := hookCreateInput(t, fixture, fixture.PostTitle)
	create.Create = &created
	if _, err := BuildRoot(create); err != nil {
		t.Fatalf("a caller field denial blocked the hook that owns the field: %v", err)
	}

	inputs := boundInputs(t, fixture)
	update := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Update)
	updated := hookUpdateInput(t, fixture, fixture.PostTitle)
	update.Target, update.Update = &inputs.target, &updated
	planned, err := BuildRoot(update)
	if err != nil {
		t.Fatalf("a caller field denial blocked the hook that owns the field: %v", err)
	}
	root, _ := planned.Graph().Root()
	for _, authorization := range root.FieldAuthorizations() {
		if authorization.FieldID() == policyir.FieldID(fixture.PostTitle) {
			t.Fatal("hook-authored system field carries a caller field authorization")
		}
	}
}

func TestCallerPlanStillRefusesTheSameSystemFieldWhenTheHookOnlyPassedItThrough(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	policies := policySet(fixture, allowAllPolicy(t, policyir.ModelID(fixture.Post)))

	create := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Create)
	passedThrough := hookCreateInput(t, fixture)
	create.Create = &passedThrough
	if _, err := BuildRoot(create); err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("caller-supplied system field survived a hook: %v", err)
	}
}

func TestOrdinaryBindingNeverProducesAHookAuthoredOperation(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	id := golem.GeneratedEqualField[testPost, golem.UUID](fixture.PostID)
	author := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	uuid := golem.NewUUID([16]byte{4})
	createFrozen, err := golem.RuntimeFreezeCreateInput(golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, author, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, title, "caller wrote this"),
	))
	if err != nil {
		t.Fatal(err)
	}
	created, err := mutationbind.CreateInput(createFrozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	updateFrozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "caller wrote this")))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := mutationbind.UpdateInput(updateFrozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range append(created.Operations(), updated.Operations()...) {
		if operation.HookAuthored() {
			t.Fatalf("caller-reachable binding produced a hook-authored operation for field %v", operation.FieldID())
		}
	}
}

func TestBindRefusesHookAuthorshipOfAFieldThatIsNotSystemOwned(t *testing.T) {
	fixture := systemModeFixture(t)
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	frozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "forged")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutationbind.UpdateInputFromHook(frozen, fixture.Registry, []golem.FieldID{fixture.PostTitle}); err == nil {
		t.Fatal("hook authorship was accepted for an ordinary field")
	}
}

func TestBindRefusesHookAuthorshipOfAFieldTheTransformedInputDoesNotWrite(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	author := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	frozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, author, golem.NewUUID([16]byte{9}))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutationbind.UpdateInputFromHook(frozen, fixture.Registry, []golem.FieldID{fixture.PostTitle}); err == nil {
		t.Fatal("hook authorship was accepted for an absent field")
	}
}

func TestHookCannotUpdateASystemImmutableField(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem, compilerir.ModeImmutable)
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	frozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "hook wrote this")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutationbind.UpdateInputFromHook(frozen, fixture.Registry, []golem.FieldID{fixture.PostTitle}); err == nil {
		t.Fatal("a hook updated a system;immutable field")
	}
}
