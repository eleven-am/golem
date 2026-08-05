package golem

import (
	"context"
	"testing"
)

type bindingActor struct{ ID int64 }
type bindingModel struct{ ID int64 }

func TestGeneratedPackageBindingsAreCopiedAndExecutionScoped(t *testing.T) {
	calls := 0
	policy := GeneratedPolicyBinding[bindingActor, bindingModel](ModelID{}, func(bindingActor) (FrozenPolicy, error) {
		calls++
		return FrozenPolicy{}, nil
	})
	hook := GeneratedBeforeHookBinding[bindingActor, bindingModel](ModelID{}, HookCreate, func(context.Context, *CreateHookRequest[bindingModel]) error { return nil })
	policies := []PolicyBinding[bindingActor]{policy}
	hooks := []HookBinding[bindingActor]{hook}
	digest := SchemaDigest{1}
	bindings := GeneratedStampedPackageBindings(digest, policies, hooks)
	policies[0] = PolicyBinding[bindingActor]{}
	hooks[0] = HookBinding[bindingActor]{}
	if bindings.policies[0].build == nil || bindings.hooks[0].invoke == nil {
		t.Fatal("package bindings retained mutable caller slices")
	}
	if calls != 0 {
		t.Fatal("constructing bindings executed policy application code")
	}
	application, err := GeneratedApplicationBindings(digest, bindings)
	if err != nil || application.GenerationDigest() != digest || bindings.GenerationDigest() != digest {
		t.Fatalf("stamped binding composition = %#v, %v", application, err)
	}
}

func TestGeneratedApplicationBindingsRejectMixedAndUnstampedPackages(t *testing.T) {
	expected := SchemaDigest{1}
	for _, pkg := range []PackageBindings[bindingActor]{
		GeneratedStampedPackageBindings[bindingActor](SchemaDigest{2}, nil, nil),
		GeneratedPackageBindings[bindingActor](nil, nil),
	} {
		application, err := GeneratedApplicationBindings(expected, GeneratedStampedPackageBindings[bindingActor](expected, nil, nil), pkg)
		if application.GenerationDigest() != (SchemaDigest{}) {
			t.Fatal("rejected application bindings retained a generation stamp")
		}
		mismatch, ok := err.(*GenerationDigestError)
		if !ok || mismatch.PackageIndex != 1 || mismatch.Expected != expected || mismatch.Actual != pkg.GenerationDigest() {
			t.Fatalf("mixed package error=%#v", err)
		}
	}
	if _, err := GeneratedApplicationBindings[bindingActor](SchemaDigest{}); err == nil {
		t.Fatal("unstamped expected bindings generation was accepted")
	}
}

func TestTypedBindingShellSignatures(t *testing.T) {
	var _ func(context.Context) bindingActor = ActorFrom[bindingActor]
	var _ func(*CreateHookRequest[bindingModel], ScalarColumn[bindingModel, int64], int64) error = SetCreate[bindingModel, int64]
	var _ PolicyFactory[bindingActor] = func(bindingActor) (FrozenPolicy, error) { return FrozenPolicy{}, nil }

	request := &CreateHookRequest[bindingModel]{}
	if err := SetCreate(request, GeneratedOrderedField[bindingModel, int64](FieldID{}), int64(1)); err != nil {
		t.Fatal(err)
	}
	if err := SetCreate(request, GeneratedBytesField[bindingModel](FieldID{}), []byte("value")); err != nil {
		t.Fatal(err)
	}
}
