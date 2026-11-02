package plan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type memoActor struct{}

type memoOperationArguments struct{}

func memoGrantOperation(context.Context, *reachPost, memoOperationArguments) (bool, error) {
	return true, nil
}

type plainPolicies struct{ set *policyruntime.Set }

func (policies plainPolicies) GenerationDigest() golem.SchemaDigest {
	return policies.set.GenerationDigest()
}

func (policies plainPolicies) Provider() policyir.Provider { return policies.set.Provider() }

func (policies plainPolicies) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	return policies.set.Policy(model)
}

func (policies plainPolicies) PolicyError(model policyir.ModelID) error {
	return policies.set.PolicyError(model)
}

var memoActions = []policyir.Action{policyir.ActionRead, policyir.ActionCreate, policyir.ActionUpdate, policyir.ActionDelete}

func memoSet(t *testing.T, handles writeReachHandles, post func(*golem.Rules[reachPost]), user func(*golem.Rules[reachUser])) (*policyruntime.Set, golem.OperationID) {
	t.Helper()
	return memoSetWith(t, handles, post, func(memoActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[reachUser]()
		user(rules)
		return rules.Freeze(handles.fixture.User)
	})
}

func memoSetWith(t *testing.T, handles writeReachHandles, post func(*golem.Rules[reachPost]), user func(memoActor) (golem.FrozenPolicy, error)) (*policyruntime.Set, golem.OperationID) {
	t.Helper()
	fixture := handles.fixture
	generation := fixture.Registry.GenerationDigest()
	postBinding := golem.GeneratedPolicyBinding[memoActor, reachPost](fixture.Post, func(memoActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[reachPost]()
		post(rules)
		return rules.Freeze(fixture.Post)
	})
	userBinding := golem.GeneratedPolicyBinding[memoActor, reachUser](fixture.User, user)
	application, err := golem.GeneratedApplicationBindings(generation, golem.GeneratedStampedPackageBindings(generation, []golem.PolicyBinding[memoActor]{postBinding, userBinding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	application, err = golem.GeneratedApplicationOperations(application, golem.GeneratedCustomMutationOperation("grant", memoGrantOperation))
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := application.Operation(memoGrantOperation)
	if !ok {
		t.Fatal("grant operation is not registered")
	}
	proof, err := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	set, err := policyruntime.Build(policyruntime.BuildRequest[memoActor]{Bindings: application, Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Capabilities: proof})
	if err != nil {
		t.Fatal(err)
	}
	return set, operation
}

func randomPostPredicate(random *rand.Rand, handles writeReachHandles, depth int) golem.Predicate[reachPost] {
	if depth > 0 && random.IntN(3) == 0 {
		left, right := randomPostPredicate(random, handles, depth-1), randomPostPredicate(random, handles, depth-1)
		switch random.IntN(3) {
		case 0:
			return golem.And(left, right)
		case 1:
			return golem.Or(left, right)
		default:
			return golem.Not(left)
		}
	}
	switch random.IntN(7) {
	case 0:
		return golem.All[reachPost]()
	case 1:
		return golem.None[reachPost]()
	case 2:
		return handles.title.Eq(fmt.Sprintf("title-%d", random.IntN(3)))
	case 3:
		return handles.title.StartsWith(fmt.Sprintf("prefix-%d", random.IntN(2)))
	case 4:
		return handles.author.Eq(golem.UUID{15: byte(random.IntN(3))})
	default:
		return handles.writer.Is(randomUserPredicate(random, handles))
	}
}

func randomUserPredicate(random *rand.Rand, handles writeReachHandles) golem.Predicate[reachUser] {
	switch random.IntN(4) {
	case 0:
		return golem.All[reachUser]()
	case 1:
		return golem.None[reachUser]()
	default:
		return handles.userName.Eq(fmt.Sprintf("name-%d", random.IntN(3)))
	}
}

func randomMemoSet(t *testing.T, handles writeReachHandles, seed uint64) (*policyruntime.Set, golem.OperationID) {
	t.Helper()
	random := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	pick := func() golem.Predicate[reachPost] { return randomPostPredicate(random, handles, 2) }
	post := []func(*golem.Rules[reachPost]){}
	add := func(rule func(*golem.Rules[reachPost])) { post = append(post, rule) }
	read, create, update, remove := pick(), pick(), pick(), pick()
	add(func(rules *golem.Rules[reachPost]) { rules.CanRead(read) })
	add(func(rules *golem.Rules[reachPost]) { rules.CanCreate(create) })
	add(func(rules *golem.Rules[reachPost]) { rules.CanUpdate(update) })
	add(func(rules *golem.Rules[reachPost]) { rules.CanDelete(remove) })
	if random.IntN(2) == 0 {
		denied := pick()
		add(func(rules *golem.Rules[reachPost]) { rules.CannotRead(denied) })
	}
	if random.IntN(2) == 0 {
		masked := pick()
		add(func(rules *golem.Rules[reachPost]) { rules.CannotReadFields(masked, handles.title) })
	}
	if random.IntN(2) == 0 {
		fields := pick()
		add(func(rules *golem.Rules[reachPost]) { rules.CannotUpdateFields(fields, handles.title, handles.author) })
	}
	if random.IntN(2) == 0 {
		granted := pick()
		add(func(rules *golem.Rules[reachPost]) { golem.Within(rules, memoGrantOperation).CanUpdate(granted) })
	}
	if random.IntN(2) == 0 {
		granted := pick()
		add(func(rules *golem.Rules[reachPost]) {
			golem.Within(rules, memoGrantOperation).CanUpdateFields(granted, handles.title)
		})
	}
	userRead := randomUserPredicate(random, handles)
	userMask := randomUserPredicate(random, handles)
	maskUser := random.IntN(2) == 0
	return memoSet(t, handles, func(rules *golem.Rules[reachPost]) {
		for _, rule := range post {
			rule(rules)
		}
	}, func(rules *golem.Rules[reachUser]) {
		rules.CanRead(userRead)
		if maskUser {
			rules.CannotReadFields(userMask, handles.userName)
		}
	})
}

type memoResult struct {
	name      string
	condition policyir.Condition
	err       error
	raised    bool
}

func memoResults(t *testing.T, handles writeReachHandles, policies PolicySet) []memoResult {
	t.Helper()
	fixture := handles.fixture
	fields := map[policyir.ModelID][]policyir.FieldID{
		policyir.ModelID(fixture.Post): {policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)},
		policyir.ModelID(fixture.User): {policyir.FieldID(fixture.UserID), policyir.FieldID(fixture.UserName)},
	}
	var results []memoResult
	record := func(name string, run func() (policyir.Condition, error)) {
		result := memoResult{name: name}
		func() {
			defer func() {
				if recover() != nil {
					result.raised = true
				}
			}()
			result.condition, result.err = run()
		}()
		results = append(results, result)
	}
	for _, model := range []policyir.ModelID{policyir.ModelID(fixture.Post), policyir.ModelID(fixture.User)} {
		for _, depth := range []int{0, 1, 4} {
			record(fmt.Sprintf("read-reach/%x/%d", model[:2], depth), func() (policyir.Condition, error) {
				return readplan.ReadReach(policies, model, depth)
			})
		}
		for _, action := range memoActions {
			record(fmt.Sprintf("row/%x/%d", model[:2], action), func() (policyir.Condition, error) {
				return RowConstraint(policies, action, model)
			})
			for _, depth := range []int{0, 2} {
				record(fmt.Sprintf("row-reach/%x/%d/%d", model[:2], action, depth), func() (policyir.Condition, error) {
					return RowReach(policies, model, action, depth)
				})
			}
			for _, field := range fields[model] {
				record(fmt.Sprintf("field/%x/%d/%x", model[:2], action, field[:2]), func() (policyir.Condition, error) {
					return FieldCondition(policies, action, model, field)
				})
				record(fmt.Sprintf("field-reach/%x/%d/%x", model[:2], action, field[:2]), func() (policyir.Condition, error) {
					return FieldReach(policies, model, action, field, 0)
				})
			}
		}
		for _, field := range fields[model] {
			record(fmt.Sprintf("read-field-reach/%x/%x", model[:2], field[:2]), func() (policyir.Condition, error) {
				return readplan.ReadFieldReach(policies, model, field, 0)
			})
		}
	}
	return results
}

func requireSameResults(t *testing.T, label string, got, want []memoResult) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: result count %d want %d", label, len(got), len(want))
	}
	for index := range want {
		if got[index].name != want[index].name {
			t.Fatalf("%s: result %d is %s want %s", label, index, got[index].name, want[index].name)
		}
		if got[index].raised != want[index].raised {
			t.Fatalf("%s %s: raised %t want %t", label, want[index].name, got[index].raised, want[index].raised)
		}
		if want[index].raised {
			continue
		}
		if (got[index].err == nil) != (want[index].err == nil) {
			t.Fatalf("%s %s: error %v want %v", label, want[index].name, got[index].err, want[index].err)
		}
		if want[index].err != nil {
			if got[index].err.Error() != want[index].err.Error() {
				t.Fatalf("%s %s: error %q want %q", label, want[index].name, got[index].err, want[index].err)
			}
			if errors.Is(got[index].err, policyruntime.ErrPolicyUnavailable) != errors.Is(want[index].err, policyruntime.ErrPolicyUnavailable) {
				t.Fatalf("%s %s: ErrPolicyUnavailable match differs: %v want %v", label, want[index].name, got[index].err, want[index].err)
			}
			var gotPlan, wantPlan *readplan.Error
			if errors.As(got[index].err, &gotPlan) != errors.As(want[index].err, &wantPlan) {
				t.Fatalf("%s %s: planner error type differs: %v want %v", label, want[index].name, got[index].err, want[index].err)
			}
			continue
		}
		gotBytes, err := policyir.CanonicalCondition(got[index].condition)
		if err != nil {
			t.Fatal(err)
		}
		wantBytes, err := policyir.CanonicalCondition(want[index].condition)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotBytes, wantBytes) {
			t.Fatalf("%s %s: memoized condition differs from computed", label, want[index].name)
		}
	}
}

func TestPolicyMemoMatchesComputedResultsAcrossRandomPolicies(t *testing.T) {
	handles := newWriteReachHandles(t)
	for seed := range uint64(48) {
		set, operation := randomMemoSet(t, handles, seed)
		want := memoResults(t, handles, plainPolicies{set: set})
		requireSameResults(t, fmt.Sprintf("seed %d miss", seed), memoResults(t, handles, set), want)
		requireSameResults(t, fmt.Sprintf("seed %d hit", seed), memoResults(t, handles, set), want)
		scoped, release := set.Within(operation)
		scopedWant := memoResults(t, handles, plainPolicies{set: scoped})
		requireSameResults(t, fmt.Sprintf("seed %d scoped", seed), memoResults(t, handles, scoped), scopedWant)
		release()
		requireSameResults(t, fmt.Sprintf("seed %d released", seed), memoResults(t, handles, scoped), want)
	}
}

func TestPolicyMemoIsSafeUnderConcurrentPlanning(t *testing.T) {
	handles := newWriteReachHandles(t)
	set, operation := randomMemoSet(t, handles, 7)
	want := memoResults(t, handles, plainPolicies{set: set})
	scoped, release := set.Within(operation)
	defer release()
	scopedWant := memoResults(t, handles, plainPolicies{set: scoped})
	var wait sync.WaitGroup
	for worker := range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 4 {
				if worker%2 == 0 {
					requireSameResults(t, "concurrent base", memoResults(t, handles, set), want)
				} else {
					requireSameResults(t, "concurrent scoped", memoResults(t, handles, scoped), scopedWant)
				}
			}
		}()
	}
	wait.Wait()
}

func TestScopedViewNeverReadsBaseMemoizedWriteReachForGrantedModel(t *testing.T) {
	handles := newWriteReachHandles(t)
	post := policyir.ModelID(handles.fixture.Post)
	set, operation := memoSet(t, handles, func(rules *golem.Rules[reachPost]) {
		rules.CanRead(golem.All[reachPost]())
		rules.CanUpdate(handles.title.Eq("own"))
		golem.Within(rules, memoGrantOperation).CanUpdate(handles.title.Eq("granted"))
	}, func(rules *golem.Rules[reachUser]) {
		rules.CanRead(golem.All[reachUser]())
	})
	baseReach, err := RowReach(set, post, policyir.ActionUpdate, 0)
	if err != nil {
		t.Fatal(err)
	}
	scoped, release := set.Within(operation)
	defer release()
	leased, lease := scoped.Lease()
	if _, ok := leased.Policy(post); !ok {
		t.Fatal("scoped policy is absent")
	}
	scopedRow, err := RowConstraint(leased, policyir.ActionUpdate, post)
	if err != nil {
		t.Fatal(err)
	}
	scopedReach, err := RowReach(leased, post, policyir.ActionUpdate, 0)
	if err != nil {
		t.Fatal(err)
	}
	computed, err := WriteReach(plainPolicies{set: leased}, post, scopedRow, 0)
	if err != nil {
		t.Fatal(err)
	}
	baseBytes, _ := policyir.CanonicalCondition(baseReach)
	scopedBytes, _ := policyir.CanonicalCondition(scopedReach)
	computedBytes, _ := policyir.CanonicalCondition(computed)
	if bytes.Equal(baseBytes, scopedBytes) {
		t.Fatal("scoped view read the base memoized write reach")
	}
	if !bytes.Equal(scopedBytes, computedBytes) {
		t.Fatal("scoped write reach differs from the computed one")
	}
	RecordGrant(leased, post, policyir.ActionUpdate, nil)
	if !lease.Used() {
		t.Fatal("scoped write through a widening grant did not mark the lease used")
	}
}

func TestScopedViewSharesBaseMemoForModelsWithoutGrants(t *testing.T) {
	handles := newWriteReachHandles(t)
	user := policyir.ModelID(handles.fixture.User)
	set, operation := memoSet(t, handles, func(rules *golem.Rules[reachPost]) {
		rules.CanRead(golem.All[reachPost]())
		golem.Within(rules, memoGrantOperation).CanUpdate(golem.All[reachPost]())
	}, func(rules *golem.Rules[reachUser]) {
		rules.CanRead(handles.userName.Eq("visible"))
	})
	base, err := readplan.ReadReach(set, user, 0)
	if err != nil {
		t.Fatal(err)
	}
	scoped, release := set.Within(operation)
	defer release()
	leased, lease := scoped.Lease()
	reached, err := readplan.ReadReach(leased, user, 0)
	if err != nil {
		t.Fatal(err)
	}
	baseBytes, _ := policyir.CanonicalCondition(base)
	reachedBytes, _ := policyir.CanonicalCondition(reached)
	if !bytes.Equal(baseBytes, reachedBytes) {
		t.Fatal("scoped view of an ungranted model differs from the base reach")
	}
	RecordGrant(leased, user, policyir.ActionRead, nil)
	if lease.Used() {
		t.Fatal("reading an ungranted model marked the operation lease used")
	}
}

func unavailableMemoSet(t *testing.T, handles writeReachHandles, panics bool) (*policyruntime.Set, golem.OperationID) {
	t.Helper()
	return memoSetWith(t, handles, func(rules *golem.Rules[reachPost]) {
		rules.CanRead(handles.writer.Is(handles.userName.Eq("visible")))
		rules.CanUpdate(handles.title.Eq("own"))
		rules.CanDelete(handles.writer.Is(golem.All[reachUser]()))
		golem.Within(rules, memoGrantOperation).CanUpdate(handles.title.Eq("granted"))
		golem.Within(rules, memoGrantOperation).CanUpdateFields(handles.writer.Is(golem.All[reachUser]()), handles.title)
	}, func(memoActor) (golem.FrozenPolicy, error) {
		if panics {
			panic("user factory exploded")
		}
		return golem.FrozenPolicy{}, errors.New("user factory refused")
	})
}

func requireRaisedOnce(t *testing.T, label string, runs ...[]memoResult) {
	t.Helper()
	raised := 0
	for _, results := range runs {
		for _, result := range results {
			if result.raised {
				raised++
			}
		}
	}
	if raised != 1 {
		t.Fatalf("%s: factory panic raised %d times want 1", label, raised)
	}
}

func requireUnavailable(t *testing.T, label string, results []memoResult) {
	t.Helper()
	for _, result := range results {
		if errors.Is(result.err, policyruntime.ErrPolicyUnavailable) {
			return
		}
	}
	t.Fatalf("%s: no result reported ErrPolicyUnavailable", label)
}

func TestPolicyMemoMatchesUncachedErrorsForFailingFactory(t *testing.T) {
	handles := newWriteReachHandles(t)
	set, operation := unavailableMemoSet(t, handles, false)
	want := memoResults(t, handles, plainPolicies{set: set})
	requireUnavailable(t, "uncached", want)
	requireSameResults(t, "miss", memoResults(t, handles, set), want)
	requireSameResults(t, "hit", memoResults(t, handles, set), want)
	scoped, release := set.Within(operation)
	leased, _ := scoped.Lease()
	scopedWant := memoResults(t, handles, plainPolicies{set: leased})
	requireSameResults(t, "scoped", memoResults(t, handles, leased), scopedWant)
	release()
	requireSameResults(t, "released", memoResults(t, handles, leased), want)
}

func TestPolicyMemoMatchesUncachedErrorsForPanickingFactory(t *testing.T) {
	handles := newWriteReachHandles(t)
	views := func(set *policyruntime.Set, operation golem.OperationID) (*policyruntime.Set, *policyruntime.Set, func()) {
		scoped, release := set.Within(operation)
		leased, _ := scoped.Lease()
		return leased, scoped, release
	}
	for _, first := range []string{"root", "scoped", "released"} {
		memoized, memoOperation := unavailableMemoSet(t, handles, true)
		uncached, uncachedOperation := unavailableMemoSet(t, handles, true)
		memoLeased, memoScoped, memoRelease := views(memoized, memoOperation)
		plainLeased, plainScoped, plainRelease := views(uncached, uncachedOperation)
		var memoRuns, plainRuns [][]memoResult
		run := func(label string, memo, direct *policyruntime.Set) {
			got := memoResults(t, handles, memo)
			want := memoResults(t, handles, plainPolicies{set: direct})
			requireSameResults(t, first+" first/"+label, got, want)
			memoRuns, plainRuns = append(memoRuns, got), append(plainRuns, want)
		}
		switch first {
		case "root":
			run("root", memoized, uncached)
			run("root again", memoized, uncached)
			run("scoped", memoLeased, plainLeased)
		case "scoped":
			run("scoped", memoLeased, plainLeased)
			run("root", memoized, uncached)
		case "released":
			memoRelease()
			plainRelease()
			run("released", memoScoped, plainScoped)
			run("root", memoized, uncached)
		}
		memoRelease()
		plainRelease()
		run("released after", memoScoped, plainScoped)
		requireRaisedOnce(t, first+" first memoized", memoRuns...)
		requireRaisedOnce(t, first+" first uncached", plainRuns...)
		requireUnavailable(t, first+" first", memoRuns[len(memoRuns)-1])
	}
}
