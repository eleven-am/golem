package plan

import (
	"bytes"
	"context"
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

var memoActions = []policyir.Action{policyir.ActionRead, policyir.ActionCreate, policyir.ActionUpdate, policyir.ActionDelete}

func memoSet(t *testing.T, handles writeReachHandles, post func(*golem.Rules[reachPost]), user func(*golem.Rules[reachUser])) (*policyruntime.Set, golem.OperationID) {
	t.Helper()
	fixture := handles.fixture
	generation := fixture.Registry.GenerationDigest()
	postBinding := golem.GeneratedPolicyBinding[memoActor, reachPost](fixture.Post, func(memoActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[reachPost]()
		post(rules)
		return rules.Freeze(fixture.Post)
	})
	userBinding := golem.GeneratedPolicyBinding[memoActor, reachUser](fixture.User, func(memoActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[reachUser]()
		user(rules)
		return rules.Freeze(fixture.User)
	})
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
}

func memoResults(t *testing.T, handles writeReachHandles, policies PolicySet) []memoResult {
	t.Helper()
	fixture := handles.fixture
	fields := map[policyir.ModelID][]policyir.FieldID{
		policyir.ModelID(fixture.Post): {policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)},
		policyir.ModelID(fixture.User): {policyir.FieldID(fixture.UserID), policyir.FieldID(fixture.UserName)},
	}
	var results []memoResult
	record := func(name string, condition policyir.Condition, err error) {
		results = append(results, memoResult{name: name, condition: condition, err: err})
	}
	for _, model := range []policyir.ModelID{policyir.ModelID(fixture.Post), policyir.ModelID(fixture.User)} {
		policy, ok := policies.Policy(model)
		if !ok {
			t.Fatalf("policy for %x is absent", model)
		}
		for _, depth := range []int{0, 1, 4} {
			reach, err := readplan.ReadReach(policies, model, depth)
			record(fmt.Sprintf("read-reach/%x/%d", model[:2], depth), reach, err)
		}
		for _, action := range memoActions {
			row, err := RowConstraint(policies, policy, action, model)
			record(fmt.Sprintf("row/%x/%d", model[:2], action), row, err)
			if err == nil {
				for _, depth := range []int{0, 2} {
					reached, reachErr := RowReach(policies, model, action, row, depth)
					record(fmt.Sprintf("row-reach/%x/%d/%d", model[:2], action, depth), reached, reachErr)
				}
			}
			for _, field := range fields[model] {
				condition, err := FieldCondition(policies, policy, action, model, field)
				record(fmt.Sprintf("field/%x/%d/%x", model[:2], action, field[:2]), condition, err)
				if err == nil {
					reached, reachErr := FieldReach(policies, model, action, field, condition, 0)
					record(fmt.Sprintf("field-reach/%x/%d/%x", model[:2], action, field[:2]), reached, reachErr)
				}
			}
		}
		for _, field := range fields[model] {
			reach, err := readplan.ReadFieldReach(policies, model, field, 0)
			record(fmt.Sprintf("read-field-reach/%x/%x", model[:2], field[:2]), reach, err)
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
		if (got[index].err == nil) != (want[index].err == nil) {
			t.Fatalf("%s %s: error %v want %v", label, want[index].name, got[index].err, want[index].err)
		}
		if want[index].err != nil {
			if got[index].err.Error() != want[index].err.Error() {
				t.Fatalf("%s %s: error %q want %q", label, want[index].name, got[index].err, want[index].err)
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
	basePolicy, _ := set.Policy(post)
	baseRow, err := RowConstraint(set, basePolicy, policyir.ActionUpdate, post)
	if err != nil {
		t.Fatal(err)
	}
	baseReach, err := RowReach(set, post, policyir.ActionUpdate, baseRow, 0)
	if err != nil {
		t.Fatal(err)
	}
	scoped, release := set.Within(operation)
	defer release()
	leased, lease := scoped.Lease()
	scopedPolicy, ok := leased.Policy(post)
	if !ok {
		t.Fatal("scoped policy is absent")
	}
	scopedRow, err := RowConstraint(leased, scopedPolicy, policyir.ActionUpdate, post)
	if err != nil {
		t.Fatal(err)
	}
	scopedReach, err := RowReach(leased, post, policyir.ActionUpdate, scopedRow, 0)
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
