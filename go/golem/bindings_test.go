package golem

import (
	"context"
	"sync"
	"sync/atomic"
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
	var _ func(*CreateHookRequest[bindingModel], CreateFieldCapability[bindingModel, int64], int64) error = SetCreate[bindingModel, int64]
	var _ PolicyFactory[bindingActor] = func(bindingActor) (FrozenPolicy, error) { return FrozenPolicy{}, nil }

	request := &CreateHookRequest[bindingModel]{}
	if err := SetCreate(request, GeneratedCreateFieldCapability(ModelID{}, GeneratedOrderedField[bindingModel, int64](FieldID{})), int64(1)); err != nil {
		t.Fatal(err)
	}
	if err := SetCreate(request, GeneratedCreateFieldCapability(ModelID{}, GeneratedBytesField[bindingModel](FieldID{})), []byte("value")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildGeneratedPolicySetIsFreshAndConcurrentActorScoped(t *testing.T) {
	model := ModelID{1}
	digest := SchemaDigest{2}
	var calls atomic.Int64
	binding := GeneratedPolicyBinding[bindingActor, bindingModel](model, func(actor bindingActor) (FrozenPolicy, error) {
		calls.Add(1)
		rules := NewRules[bindingModel]()
		if actor.ID%2 == 0 {
			rules.CanRead(All[bindingModel]())
		} else {
			rules.CanRead(None[bindingModel]())
		}
		return rules.Freeze(model)
	})
	application, err := GeneratedApplicationBindings(digest, GeneratedStampedPackageBindings(digest, []PolicyBinding[bindingActor]{binding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	inventory := application.PolicyInventory()
	if len(inventory) != 1 || inventory[0] != model || calls.Load() != 0 {
		t.Fatalf("static inventory executed or drifted: %v calls=%d", inventory, calls.Load())
	}
	inventory[0] = ModelID{9}
	if application.PolicyInventory()[0] != model {
		t.Fatal("policy inventory leaked backing storage")
	}

	const executions = 64
	canonical := make([]string, executions)
	var wait sync.WaitGroup
	wait.Add(executions)
	for index := range executions {
		go func(index int) {
			defer wait.Done()
			set, buildErr := BuildGeneratedPolicySet(application, bindingActor{ID: int64(index)})
			if buildErr != nil {
				t.Errorf("build %d: %v", index, buildErr)
				return
			}
			policies := set.Policies()
			if len(policies) != 1 {
				t.Errorf("build %d policy count=%d", index, len(policies))
				return
			}
			canonical[index] = string(policies[0].CanonicalBytes())
		}(index)
	}
	wait.Wait()
	if calls.Load() != executions {
		t.Fatalf("factory calls=%d want=%d", calls.Load(), executions)
	}
	if canonical[0] == canonical[1] {
		t.Fatal("different actors produced aliased/equal policy snapshots")
	}
	for index := range executions {
		if canonical[index] != canonical[index%2] {
			t.Fatalf("actor class %d leaked at execution %d", index%2, index)
		}
	}
}

func TestBuildGeneratedPolicySetRejectsFactoryModelMismatchAndDuplicates(t *testing.T) {
	digest := SchemaDigest{3}
	model := ModelID{1}
	other := ModelID{2}
	factory := func(ModelID) PolicyFactory[bindingActor] {
		return func(bindingActor) (FrozenPolicy, error) { return NewRules[bindingModel]().Freeze(other) }
	}
	application, err := GeneratedApplicationBindings(digest, GeneratedStampedPackageBindings(digest, []PolicyBinding[bindingActor]{GeneratedPolicyBinding[bindingActor, bindingModel](model, factory(model))}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildGeneratedPolicySet(application, bindingActor{}); err == nil {
		t.Fatal("factory model mismatch was accepted")
	}
	valid := func(bindingActor) (FrozenPolicy, error) { return NewRules[bindingModel]().Freeze(model) }
	application, err = GeneratedApplicationBindings(digest, GeneratedStampedPackageBindings(digest, []PolicyBinding[bindingActor]{GeneratedPolicyBinding[bindingActor, bindingModel](model, valid), GeneratedPolicyBinding[bindingActor, bindingModel](model, valid)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildGeneratedPolicySet(application, bindingActor{}); err == nil {
		t.Fatal("duplicate model bindings were accepted")
	}
}

func TestBuildGeneratedPolicySetPreflightsCompleteGraphBeforeFactories(t *testing.T) {
	digest := SchemaDigest{4}
	first, second := ModelID{1}, ModelID{2}
	var calls atomic.Int64
	valid := func(model ModelID) PolicyFactory[bindingActor] {
		return func(bindingActor) (FrozenPolicy, error) {
			calls.Add(1)
			return NewRules[bindingModel]().Freeze(model)
		}
	}

	t.Run("duplicate", func(t *testing.T) {
		calls.Store(0)
		pkg := GeneratedStampedPackageBindings(digest, []PolicyBinding[bindingActor]{
			GeneratedPolicyBinding[bindingActor, bindingModel](first, valid(first)),
			GeneratedPolicyBinding[bindingActor, bindingModel](first, valid(first)),
		}, nil)
		application, err := GeneratedApplicationBindings(digest, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := BuildGeneratedPolicySet(application, bindingActor{}); err == nil {
			t.Fatal("duplicate binding was accepted")
		}
		if calls.Load() != 0 {
			t.Fatalf("factory ran before duplicate preflight: calls=%d", calls.Load())
		}
	})

	t.Run("incomplete later binding", func(t *testing.T) {
		calls.Store(0)
		pkg := GeneratedStampedPackageBindings(digest, []PolicyBinding[bindingActor]{
			GeneratedPolicyBinding[bindingActor, bindingModel](first, valid(first)),
			GeneratedPolicyBinding[bindingActor, bindingModel](second, nil),
		}, nil)
		application, err := GeneratedApplicationBindings(digest, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := BuildGeneratedPolicySet(application, bindingActor{}); err == nil {
			t.Fatal("incomplete binding was accepted")
		}
		if calls.Load() != 0 {
			t.Fatalf("factory ran before completeness preflight: calls=%d", calls.Load())
		}
	})
}
