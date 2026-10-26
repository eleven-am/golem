package runtime

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

type lazyPolicyFixture struct {
	set        *Set
	user       policyir.ModelID
	post       policyir.ModelID
	userCalls  *atomic.Int64
	postCalls  *atomic.Int64
	userPolicy func() bool
}

func newLazyPolicyFixture(t *testing.T, post func(runtimeActor) (golem.FrozenPolicy, error)) lazyPolicyFixture {
	t.Helper()
	registry, fixture := runtimeSchemaFixture(t)
	userCalls, postCalls := &atomic.Int64{}, &atomic.Int64{}
	user := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(runtimeActor) (golem.FrozenPolicy, error) {
		userCalls.Add(1)
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(golem.All[runtimeUser]())
		return rules.Freeze(fixture.user)
	})
	broken := golem.GeneratedPolicyBinding[runtimeActor, runtimePost](fixture.post, func(actor runtimeActor) (golem.FrozenPolicy, error) {
		postCalls.Add(1)
		return post(actor)
	})
	set, err := Build(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.generation, user, broken), Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatal(err)
	}
	if userCalls.Load() != 0 || postCalls.Load() != 0 {
		t.Fatalf("Build ran factories eagerly: user=%d post=%d", userCalls.Load(), postCalls.Load())
	}
	model := policyir.ModelID(fixture.user)
	return lazyPolicyFixture{set: set, user: model, post: policyir.ModelID(fixture.post), userCalls: userCalls, postCalls: postCalls, userPolicy: func() bool {
		policy, ok := set.Policy(model)
		if !ok {
			return false
		}
		row, err := resolve.RowConstraint(policy, policyir.ActionRead, model)
		if err != nil {
			return false
		}
		allowed, err := resolve.ActionAllowed(row)
		return err == nil && allowed
	}}
}

func lazyPolicyPanics(set *Set, model policyir.ModelID) (value any, ok bool) {
	defer func() {
		value = recover()
	}()
	_, ok = set.Policy(model)
	return nil, ok
}

func TestLazyBuildFactoryErrorRefusesOnlyItsModel(t *testing.T) {
	fixture := newLazyPolicyFixture(t, func(runtimeActor) (golem.FrozenPolicy, error) {
		return golem.FrozenPolicy{}, errors.New("post factory refused")
	})
	if !fixture.userPolicy() || fixture.userCalls.Load() != 1 || fixture.postCalls.Load() != 0 {
		t.Fatalf("model A was not built alone: user=%d post=%d", fixture.userCalls.Load(), fixture.postCalls.Load())
	}
	for range 3 {
		if _, ok := fixture.set.Policy(fixture.post); ok {
			t.Fatal("a model whose factory failed returned a policy")
		}
	}
	if err := runtimePolicyError(t, fixture.set, fixture.post); err == nil || !strings.Contains(err.Error(), "P2_RUNTIME_FACTORY") || !strings.Contains(err.Error(), "post factory refused") {
		t.Fatalf("recorded factory error=%v", err)
	}
	if fixture.postCalls.Load() != 1 {
		t.Fatalf("failed factory calls=%d want exactly one", fixture.postCalls.Load())
	}
	scoped, release := fixture.set.Within(golem.OperationID{})
	defer release()
	leased, lease := scoped.Lease()
	if _, ok := leased.Policy(fixture.post); ok {
		t.Fatal("an operation view granted a model whose factory failed")
	}
	leased.RecordGrant(fixture.post, policyir.ActionCreate, nil)
	if lease.Used() {
		t.Fatal("a refused model was served from an operation grant")
	}
	if !fixture.userPolicy() || fixture.userCalls.Load() != 1 {
		t.Fatalf("model A was rebuilt or lost: calls=%d", fixture.userCalls.Load())
	}
}

func TestLazyBuildFactoryPanicIsRaisedOnceAndRefusesAfter(t *testing.T) {
	fixture := newLazyPolicyFixture(t, func(runtimeActor) (golem.FrozenPolicy, error) {
		panic("post factory exploded")
	})
	value, ok := lazyPolicyPanics(fixture.set, fixture.post)
	if value != "post factory exploded" || ok {
		t.Fatalf("first caller panic=%v ok=%t", value, ok)
	}
	for range 3 {
		if value, ok := lazyPolicyPanics(fixture.set, fixture.post); value != nil || ok {
			t.Fatalf("later caller panic=%v ok=%t", value, ok)
		}
	}
	if err := runtimePolicyError(t, fixture.set, fixture.post); err == nil || !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "post factory exploded") || !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("recorded panic error=%v", err)
	}
	if fixture.postCalls.Load() != 1 {
		t.Fatalf("panicking factory calls=%d want exactly one", fixture.postCalls.Load())
	}
	if !fixture.userPolicy() || fixture.userCalls.Load() != 1 {
		t.Fatalf("model A was affected by model B's panic: calls=%d", fixture.userCalls.Load())
	}
}

func TestLazyBuildConcurrentPanicIsRaisedExactlyOnce(t *testing.T) {
	fixture := newLazyPolicyFixture(t, func(runtimeActor) (golem.FrozenPolicy, error) {
		panic("post factory exploded")
	})
	const callers = 32
	var panics, grants atomic.Int64
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			value, ok := lazyPolicyPanics(fixture.set, fixture.post)
			if value != nil {
				panics.Add(1)
			}
			if ok {
				grants.Add(1)
			}
		}()
	}
	wait.Wait()
	if panics.Load() != 1 || grants.Load() != 0 || fixture.postCalls.Load() != 1 {
		t.Fatalf("panics=%d grants=%d factory calls=%d", panics.Load(), grants.Load(), fixture.postCalls.Load())
	}
}
