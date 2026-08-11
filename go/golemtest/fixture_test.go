package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

type syntheticActor struct {
	Email    string
	Token    string
	Tenant   string
	Session  string
	Database string
	Row      string
}

func foreignGeneration() golem.SchemaDigest {
	return golem.SchemaDigest{0xfe, 0xed, 0xfa, 0xce, 0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}
}

func syntheticBundle() golem.SchemaBundle {
	return p6metrics.GolemGeneratedSchemaBundle()
}

func syntheticGeneration() golem.SchemaDigest {
	return syntheticBundle().GenerationDigest()
}

func syntheticDescriptor() golem.ModelDescriptor[p6metrics.Category] {
	return p6metrics.GolemGeneratedCategoryDescriptor
}

func syntheticModels(t *testing.T) []golem.ModelMetadata {
	t.Helper()
	descriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatalf("build synthetic descriptors: %v", err)
	}
	models := descriptors.Models()
	if len(models) < 2 {
		t.Fatalf("the synthetic fixture declares %d models, want at least 2", len(models))
	}
	return models
}

func syntheticDescriptors(t *testing.T) golem.ApplicationDescriptors {
	t.Helper()
	descriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatalf("build synthetic descriptors: %v", err)
	}
	return descriptors
}

func syntheticBindings(t *testing.T, factory func(golem.ModelID) golem.PolicyFactory[syntheticActor]) golem.ApplicationBindings[syntheticActor] {
	t.Helper()
	generation := syntheticGeneration()
	models := syntheticModels(t)
	policies := make([]golem.PolicyBinding[syntheticActor], 0, len(models))
	for _, metadata := range models {
		policies = append(policies, golem.GeneratedPolicyBinding[syntheticActor, p6metrics.Category](metadata.ModelID(), factory(metadata.ModelID())))
	}
	bindings, err := golem.GeneratedApplicationBindings(
		generation,
		golem.GeneratedStampedPackageBindings(generation, policies, nil),
	)
	if err != nil {
		t.Fatalf("build synthetic bindings: %v", err)
	}
	return bindings
}

func syntheticPolicy(t *testing.T, model golem.ModelID) golem.FrozenPolicy {
	t.Helper()
	rules := golem.NewRules[p6metrics.Category]()
	rules.CanRead(golem.All[p6metrics.Category]())
	policy, err := rules.Freeze(model)
	if err != nil {
		t.Fatalf("freeze synthetic policy: %v", err)
	}
	return policy
}

func syntheticOpenFactory(t *testing.T) func(golem.ModelID) golem.PolicyFactory[syntheticActor] {
	t.Helper()
	return func(model golem.ModelID) golem.PolicyFactory[syntheticActor] {
		policy := syntheticPolicy(t, model)
		return func(syntheticActor) (golem.FrozenPolicy, error) { return policy, nil }
	}
}

func syntheticKit(t *testing.T, factory func(golem.ModelID) golem.PolicyFactory[syntheticActor]) *Kit[syntheticActor] {
	t.Helper()
	kit, err := New(syntheticBindings(t, factory), syntheticDescriptors(t), syntheticBundle())
	if err != nil {
		t.Fatalf("build synthetic kit: %v", err)
	}
	return kit
}
