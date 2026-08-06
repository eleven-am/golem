package ir

import (
	"bytes"
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func providerRequirements(t *testing.T) []ProviderRequirement {
	t.Helper()
	value, err := NewProviderRequirement(policyir.PortableProviders(), CapabilityTransaction)
	if err != nil {
		t.Fatal(err)
	}
	return []ProviderRequirement{value}
}

func bounds(t *testing.T) StatementBounds {
	t.Helper()
	value, err := NewStatementBounds(999, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func updateGraph(t *testing.T, operations []ScalarOperation, fact FactRequirement) Graph {
	t.Helper()
	model := testModel(1)
	valueTarget := target(t, model, 1)
	selectUpdate := selection(t, model, policyir.ActionUpdate)
	post := constant(t, model)
	graph, err := NewGraph(NodeInput{Operation: Update, Model: model, Target: &valueTarget, Selection: &selectUpdate, RowPostcondition: &post, ScalarOperations: operations, Fact: fact, Identity: IdentityUnchanged})
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func TestPlanSeparatesCallerAndSystemRequirements(t *testing.T) {
	callerGraph := updateGraph(t, nil, NoFact())
	if _, err := NewPlan(PlanInput{Stance: Caller, Graph: callerGraph, Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(PlanInput{Stance: System, Graph: callerGraph, Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)}); err == nil {
		t.Fatal("system plan carrying caller policy accepted")
	}

	model := testModel(1)
	valueTarget := target(t, model, 1)
	systemGraph, err := NewGraph(NodeInput{Operation: Update, Model: model, Target: &valueTarget, Identity: IdentityUnchanged})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(PlanInput{Stance: System, Graph: systemGraph, Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(PlanInput{Stance: Caller, Graph: systemGraph, Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)}); err == nil {
		t.Fatal("caller plan without selecting constraint accepted")
	}
}

func TestPlanRequiresFactCodecExactlyWhenFactsExist(t *testing.T) {
	fact, err := NewFactRequirement(FactUpdated, []policyir.FieldID{testField(1)}, []policyir.FieldID{testField(1)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph := updateGraph(t, nil, fact)
	input := PlanInput{Stance: Caller, Graph: graph, Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)}
	if _, err := NewPlan(input); err == nil {
		t.Fatal("fact plan without codec accepted")
	}
	var generation [32]byte
	generation[0] = 1
	codec, err := NewFactCodecRequirement(1, "golem.exact.v1", generation)
	if err != nil {
		t.Fatal(err)
	}
	input.FactCodec = &codec
	if _, err := NewPlan(input); err != nil {
		t.Fatal(err)
	}

	input.Graph = updateGraph(t, nil, NoFact())
	if _, err := NewPlan(input); err == nil {
		t.Fatal("unused fact codec accepted")
	}
}

func TestCanonicalPlanIsDeterministicAndCloneSafe(t *testing.T) {
	a, _ := NewSet(testField(2), stringType(t, false), stringValue(t, "second"))
	b, _ := NewSet(testField(1), stringType(t, false), stringValue(t, "first"))
	first, err := NewPlan(PlanInput{Stance: Caller, Graph: updateGraph(t, []ScalarOperation{a, b}, NoFact()), Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPlan(PlanInput{Stance: Caller, Graph: updateGraph(t, []ScalarOperation{b, a}, NoFact()), Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)})
	if err != nil {
		t.Fatal(err)
	}
	left, err := CanonicalPlan(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalPlan(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatal("equivalent normalized plans have different canonical encodings")
	}
	leftFingerprint, _ := PlanFingerprint(first)
	rightFingerprint, _ := PlanFingerprint(second)
	if leftFingerprint != rightFingerprint {
		t.Fatal("equivalent plans have different fingerprints")
	}

	providers := first.ProviderRequirements()
	providers[0] = ProviderRequirement{}
	nodes := first.Graph().Nodes()
	operations := nodes[0].ScalarOperations()
	operations[0] = ScalarOperation{}
	after, err := CanonicalPlan(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, after) {
		t.Fatal("plan accessors mutated canonical plan")
	}

	changed, err := NewPlan(PlanInput{Stance: Caller, Graph: updateGraph(t, []ScalarOperation{a}, NoFact()), Providers: providerRequirements(t), Retry: NoRetry, Bounds: bounds(t)})
	if err != nil {
		t.Fatal(err)
	}
	changedFingerprint, _ := PlanFingerprint(changed)
	if changedFingerprint == leftFingerprint {
		t.Fatal("semantic plan change did not change fingerprint")
	}
}

func TestPlanRejectsRetryOwnershipMismatch(t *testing.T) {
	if _, err := NewPlan(PlanInput{Stance: Caller, Graph: updateGraph(t, nil, NoFact()), Providers: providerRequirements(t), Retry: EngineOwnedUpsertRetry, Bounds: bounds(t)}); err == nil {
		t.Fatal("upsert retry on update accepted")
	}
}

func TestUpsertPlanDeclaresEngineOrCallerTransactionRetry(t *testing.T) {
	model := testModel(1)
	valueTarget := target(t, model, 1)
	selectUpdate := selection(t, model, policyir.ActionUpdate)
	post := constant(t, model)
	graph, err := NewGraph(NodeInput{
		Operation: Upsert, Model: model, Target: &valueTarget, Selection: &selectUpdate, Identity: IdentityUnchanged,
		Children: []NodeInput{
			{Operation: Create, Model: model, Branch: UpsertCreateBranch, RowPostcondition: &post, Identity: IdentityProduced},
			{Operation: Update, Model: model, Branch: UpsertUpdateBranch, Target: &valueTarget, Selection: &selectUpdate, RowPostcondition: &post, Identity: IdentityUnchanged},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := PlanInput{Stance: Caller, Graph: graph, Providers: providerRequirements(t), Bounds: bounds(t)}
	base.Retry = EngineOwnedUpsertRetry
	if _, err := NewPlan(base); err != nil {
		t.Fatal(err)
	}
	base.Retry = CallerTransactionNoReplay
	if _, err := NewPlan(base); err != nil {
		t.Fatal(err)
	}
	base.Retry = NoRetry
	if _, err := NewPlan(base); err == nil {
		t.Fatal("upsert without retry ownership accepted")
	}
}
