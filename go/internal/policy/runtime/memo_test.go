package runtime

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

func memoUserSet(t *testing.T, factory func(runtimeFixture) func(runtimeActor) (golem.FrozenPolicy, error)) (*Set, policyir.ModelID) {
	t.Helper()
	registry, fixture := runtimeSchemaFixture(t)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, factory(fixture))
	set, err := Build(BuildRequest[runtimeActor]{Bindings: runtimeBindings(t, fixture.generation, binding), Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatal(err)
	}
	return set, policyir.ModelID(fixture.user)
}

func memoReadableUser(t *testing.T) (*Set, policyir.ModelID) {
	t.Helper()
	return memoUserSet(t, func(fixture runtimeFixture) func(runtimeActor) (golem.FrozenPolicy, error) {
		handle := golem.GeneratedModeTextField[runtimeUser, string](fixture.userHandle)
		return func(runtimeActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[runtimeUser]()
			rules.CanRead(handle.StartsWith("visible"))
			rules.CanUpdate(handle.Eq("owned"))
			return rules.Freeze(fixture.user)
		}
	})
}

func memoRowCompute(model policyir.ModelID, action policyir.Action, calls *atomic.Int64) func(Source) (policyir.Condition, error) {
	return func(source Source) (policyir.Condition, error) {
		calls.Add(1)
		policy, ok := source.Policy(model)
		if !ok {
			return policyir.Condition{}, errors.New("policy is absent")
		}
		return resolve.RowConstraint(policy, action, model)
	}
}

func TestMemoBoundHoldsPerKind(t *testing.T) {
	set, user := memoReadableUser(t)
	var calls atomic.Int64
	const keys = memoLimit + 512
	for depth := range keys {
		_, served, err := set.Memoized(MemoKey{Kind: MemoReadExpansion, Model: user, Depth: depth}, memoRowCompute(user, policyir.ActionRead, &calls))
		if !served || err != nil {
			t.Fatalf("depth %d: served=%t err=%v", depth, served, err)
		}
	}
	if size := set.memo.conditions[MemoReadExpansion-1].size(); size != memoLimit {
		t.Fatalf("memo size %d want %d", size, memoLimit)
	}
	stored := 0
	set.memo.conditions[MemoReadExpansion-1].entries.Range(func(any, any) bool { stored++; return true })
	if stored != memoLimit {
		t.Fatalf("stored entries %d want %d", stored, memoLimit)
	}
	for kind := MemoRowConstraint; int(kind) <= memoKinds; kind++ {
		if kind != MemoReadExpansion && set.memo.conditions[kind-1].size() != 0 {
			t.Fatalf("kind %d shares the expansion bound", kind)
		}
	}
	calls.Store(0)
	if _, _, err := set.Memoized(MemoKey{Kind: MemoReadExpansion, Model: user, Depth: 0}, memoRowCompute(user, policyir.ActionRead, &calls)); err != nil || calls.Load() != 0 {
		t.Fatalf("stored key recomputed: calls=%d err=%v", calls.Load(), err)
	}
	overflow, served, err := set.Memoized(MemoKey{Kind: MemoReadExpansion, Model: user, Depth: keys - 1}, memoRowCompute(user, policyir.ActionRead, &calls))
	if !served || err != nil || calls.Load() != 1 {
		t.Fatalf("key past the bound was not computed: served=%t calls=%d err=%v", served, calls.Load(), err)
	}
	policy, _ := set.Policy(user)
	direct, err := resolve.RowConstraint(policy, policyir.ActionRead, user)
	if err != nil {
		t.Fatal(err)
	}
	overflowBytes, _ := policyir.CanonicalCondition(overflow)
	directBytes, _ := policyir.CanonicalCondition(direct)
	if !bytes.Equal(overflowBytes, directBytes) {
		t.Fatal("key past the bound computed a different condition")
	}
	if size := set.memo.conditions[MemoReadExpansion-1].size(); size != memoLimit {
		t.Fatalf("memo grew past the bound: %d", size)
	}
}

func TestMemoNeverStoresFailedLazyBuild(t *testing.T) {
	set, user := memoUserSet(t, func(runtimeFixture) func(runtimeActor) (golem.FrozenPolicy, error) {
		return func(runtimeActor) (golem.FrozenPolicy, error) {
			return golem.FrozenPolicy{}, errors.New("factory refused")
		}
	})
	var calls atomic.Int64
	key := MemoKey{Kind: MemoRowConstraint, Model: user, Action: policyir.ActionRead}
	for attempt := range 2 {
		_, served, err := set.Memoized(key, memoRowCompute(user, policyir.ActionRead, &calls))
		if !served || err == nil {
			t.Fatalf("attempt %d: failed build answered served=%t err=%v", attempt, served, err)
		}
	}
	if calls.Load() != 2 || set.memo.conditions[MemoRowConstraint-1].size() != 0 {
		t.Fatalf("failed build was memoized: calls=%d size=%d", calls.Load(), set.memo.conditions[MemoRowConstraint-1].size())
	}
}

func TestMemoNeverStoresPanickingLazyBuild(t *testing.T) {
	set, user := memoUserSet(t, func(runtimeFixture) func(runtimeActor) (golem.FrozenPolicy, error) {
		return func(runtimeActor) (golem.FrozenPolicy, error) {
			panic("factory exploded")
		}
	})
	var calls atomic.Int64
	key := MemoKey{Kind: MemoRowConstraint, Model: user, Action: policyir.ActionRead}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("first use did not re-raise the factory panic")
			}
		}()
		_, _, _ = set.Memoized(key, memoRowCompute(user, policyir.ActionRead, &calls))
	}()
	if set.memo.conditions[MemoRowConstraint-1].size() != 0 {
		t.Fatal("panicking build was memoized")
	}
	_, served, err := set.Memoized(key, memoRowCompute(user, policyir.ActionRead, &calls))
	if !served || err == nil || set.memo.conditions[MemoRowConstraint-1].size() != 0 {
		t.Fatalf("panicked build answered as success: served=%t err=%v", served, err)
	}
}

func TestMemoBypassesUnknownViews(t *testing.T) {
	set, user := memoReadableUser(t)
	var calls atomic.Int64
	key := MemoKey{Kind: MemoRowConstraint, Model: user, Action: policyir.ActionRead}
	if _, served, err := set.Memoized(key, memoRowCompute(user, policyir.ActionRead, &calls)); !served || err != nil {
		t.Fatalf("base memo refused: served=%t err=%v", served, err)
	}
	copied := *set
	if _, served, _ := copied.Memoized(key, memoRowCompute(user, policyir.ActionRead, &calls)); served {
		t.Fatal("a copied view answered from the base memo")
	}
	if _, served, _ := (&Set{}).Memoized(key, memoRowCompute(user, policyir.ActionRead, &calls)); served {
		t.Fatal("a set without a memo answered")
	}
	if _, served, _ := set.Memoized(MemoKey{Kind: MemoKind(0), Model: user}, memoRowCompute(user, policyir.ActionRead, &calls)); served {
		t.Fatal("an unknown memo kind answered")
	}
}

func TestMemoScopedViewsOnlyShareEntriesWithoutGrants(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	var calls atomic.Int64
	key := MemoKey{Kind: MemoRowConstraint, Model: fixture.user, Action: policyir.ActionDelete}
	base, served, err := fixture.set.Memoized(key, memoRowCompute(fixture.user, policyir.ActionDelete, &calls))
	if !served || err != nil {
		t.Fatal(err)
	}
	audit, release := fixture.set.Within(fixture.audit)
	if _, served, _ := audit.Memoized(key, memoRowCompute(fixture.user, policyir.ActionDelete, &calls)); served {
		t.Fatal("a scoped view whose operation grants the model answered from the base memo")
	}
	release()
	released, served, err := audit.Memoized(key, memoRowCompute(fixture.user, policyir.ActionDelete, &calls))
	if !served || err != nil {
		t.Fatal("a released scoped view did not answer as the base set")
	}
	baseBytes, _ := policyir.CanonicalCondition(base)
	releasedBytes, _ := policyir.CanonicalCondition(released)
	if !bytes.Equal(baseBytes, releasedBytes) {
		t.Fatal("released scoped view answered differently from the base set")
	}
}

func TestMemoGrantDecisionsAreKeyedByOperation(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	decide := func(operation golem.OperationID) bool {
		view, release := fixture.set.Within(operation)
		defer release()
		leased, lease := view.Lease()
		if _, ok := leased.Policy(fixture.user); !ok {
			t.Fatal("scoped policy is absent")
		}
		leased.RecordGrant(fixture.user, policyir.ActionDelete, nil)
		return lease.Used()
	}
	for range 2 {
		if !decide(fixture.audit) {
			t.Fatal("audit delete grant widens the caller policy but was not recorded")
		}
		if decide(fixture.rename) {
			t.Fatal("rename has no delete grant but reused the audit decision")
		}
	}
	if size := fixture.set.memo.grants.size(); size != 2 {
		t.Fatalf("grant memo size %d want 2", size)
	}
}

func TestMemoIsConcurrencySafe(t *testing.T) {
	set, user := memoReadableUser(t)
	var calls atomic.Int64
	actions := []policyir.Action{policyir.ActionRead, policyir.ActionUpdate, policyir.ActionDelete, policyir.ActionCreate}
	want := make([][]byte, len(actions))
	policy, _ := set.Policy(user)
	for index, action := range actions {
		condition, err := resolve.RowConstraint(policy, action, user)
		if err != nil {
			t.Fatal(err)
		}
		want[index], _ = policyir.CanonicalCondition(condition)
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for round := range 64 {
				index := round % len(actions)
				condition, served, err := set.Memoized(MemoKey{Kind: MemoRowConstraint, Model: user, Action: actions[index]}, memoRowCompute(user, actions[index], &calls))
				if !served || err != nil {
					t.Errorf("served=%t err=%v", served, err)
					return
				}
				encoded, _ := policyir.CanonicalCondition(condition)
				if !bytes.Equal(encoded, want[index]) {
					t.Error("concurrent memo answered a different condition")
					return
				}
			}
		}()
	}
	wait.Wait()
	if size := set.memo.conditions[MemoRowConstraint-1].size(); size != len(actions) {
		t.Fatalf("memo size %d want %d", size, len(actions))
	}
}
