package plan

import (
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func systemModeFixture(t *testing.T, modes ...compilerir.FieldMode) schematest.Fixture {
	t.Helper()
	return schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: modes})
}

func fieldRule(t *testing.T, model policyir.ModelID, action policyir.Action, field policyir.FieldID, position uint32) policyir.Rule {
	t.Helper()
	value, err := policyir.NewFieldRule(action, policyir.EffectGrant, model, nil, field, nil, position)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCallerPlanRefusesSystemFieldOnCreateAndUpdate(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	inputs := boundInputs(t, fixture)
	policies := policySet(fixture, allowAllPolicy(t, policyir.ModelID(fixture.Post)))

	create := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Create)
	create.Create = &inputs.create
	if _, err := BuildRoot(create); err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("caller create wrote a system field: %v", err)
	}

	update := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Update)
	update.Target, update.Update = &inputs.target, &inputs.update
	if _, err := BuildRoot(update); err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("caller update wrote a system field: %v", err)
	}

	updateMany := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.UpdateMany)
	predicate := boundBatchPredicate(t, fixture)
	updateMany.Predicate, updateMany.Update = &predicate, &inputs.updateMany
	if _, err := BuildRoot(updateMany); err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("caller updateMany wrote a system field: %v", err)
	}
}

func TestSystemPlanWritesSystemField(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	inputs := boundInputs(t, fixture)

	create := baseRequest(t, fixture, nil, mutationir.System, mutationir.Create)
	create.Create = &inputs.create
	if _, err := BuildRoot(create); err != nil {
		t.Fatalf("system create was refused a system field: %v", err)
	}

	update := baseRequest(t, fixture, nil, mutationir.System, mutationir.Update)
	update.Target, update.Update = &inputs.target, &inputs.update
	if _, err := BuildRoot(update); err != nil {
		t.Fatalf("system update was refused a system field: %v", err)
	}
}

func TestSystemFieldModeSurvivesAPolicyThatGrantsItToCallers(t *testing.T) {
	fixture := systemModeFixture(t, compilerir.ModeSystem)
	inputs := boundInputs(t, fixture)
	model := policyir.ModelID(fixture.Post)
	granting := policyWithRules(t, model,
		rule(t, model, policyir.ActionRead, nil, 0),
		rule(t, model, policyir.ActionCreate, nil, 1),
		rule(t, model, policyir.ActionUpdate, nil, 2),
		fieldRule(t, model, policyir.ActionCreate, policyir.FieldID(fixture.PostTitle), 3),
		fieldRule(t, model, policyir.ActionUpdate, policyir.FieldID(fixture.PostTitle), 4),
	)
	policies := policySet(fixture, granting)

	create := baseRequest(t, fixture, policies, mutationir.Caller, mutationir.Create)
	create.Create = &inputs.create
	if _, err := BuildRoot(create); err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("an explicit field grant defeated the system mode: %v", err)
	}
}
