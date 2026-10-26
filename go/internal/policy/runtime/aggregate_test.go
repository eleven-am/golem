package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/gentest"
	"github.com/eleven-am/golem/go/internal/policy/imply"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

func runtimeCountOperation(context.Context, *runtimeUser, runtimeOperationArguments) (bool, error) {
	return true, nil
}

func runtimeSumOperation(context.Context, *runtimeUser, runtimeOperationArguments) (bool, error) {
	return true, nil
}

type runtimeAggregateFixture struct {
	set    *Set
	user   policyir.ModelID
	handle policyir.FieldID
	id     policyir.FieldID
	count  golem.OperationID
	sum    golem.OperationID
}

func newRuntimeAggregateFixture(t *testing.T) runtimeAggregateFixture {
	t.Helper()
	registry, fixture := runtimeSchemaFixture(t)
	var userID golem.FieldID
	for _, model := range gentest.SocialCompilationIR().Model.Models {
		if model.LogicalName != "User" {
			continue
		}
		for _, field := range model.Fields {
			if field.GoName == "ID" {
				userID = runtimeFieldID(t, field.ID)
			}
		}
	}
	handle := golem.GeneratedTextField[runtimeUser, string](fixture.userHandle)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(handle.Eq("owned"))
		golem.Within(rules, runtimeCountOperation).CanCount(handle.StartsWith("own"))
		golem.Within(rules, runtimeSumOperation).CanAggregate(handle.StartsWith("sum"), handle)
		return rules.Freeze(fixture.user)
	})
	bindings, err := golem.GeneratedApplicationOperations(runtimeBindings(t, fixture.generation, binding),
		golem.GeneratedTxOperation("count", runtimeCountOperation),
		golem.GeneratedTxOperation("sum", runtimeSumOperation),
		golem.GeneratedCustomMutationOperation("invite", runtimeInviteOperation),
	)
	if err != nil {
		t.Fatal(err)
	}
	set, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatal(err)
	}
	count, countOK := bindings.TxOperation(runtimeCountOperation)
	sum, sumOK := bindings.TxOperation(runtimeSumOperation)
	if !countOK || !sumOK {
		t.Fatal("registered transaction operations did not resolve")
	}
	return runtimeAggregateFixture{set: set, user: policyir.ModelID(fixture.user), handle: policyir.FieldID(fixture.userHandle), id: policyir.FieldID(userID), count: count, sum: sum}
}

func runtimeReadConditions(t *testing.T, set *Set, model policyir.ModelID, fields ...policyir.FieldID) []policyir.Condition {
	t.Helper()
	policy, ok := set.Policy(model)
	if !ok {
		t.Fatalf("policy is absent: %v", set.PolicyError(model))
	}
	row, err := resolve.RowConstraint(policy, policyir.ActionRead, model)
	if err != nil {
		t.Fatal(err)
	}
	result := []policyir.Condition{row}
	for _, field := range fields {
		condition, err := resolve.FieldCondition(policy, policyir.ActionRead, model, field)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, condition)
	}
	return result
}

func runtimeSameConditions(t *testing.T, left, right []policyir.Condition) bool {
	t.Helper()
	for index := range left {
		same, err := imply.Same(left[index], right[index])
		if err != nil {
			t.Fatal(err)
		}
		if !same {
			return false
		}
	}
	return true
}

func TestAggregateViewWidensOnlyAggregateReadsOfALiveTransactionOperation(t *testing.T) {
	fixture := newRuntimeAggregateFixture(t)
	base := runtimeReadConditions(t, fixture.set, fixture.user, fixture.handle, fixture.id)
	if fixture.set.AggregateView() != fixture.set {
		t.Fatal("the caller's own set gained an aggregate view")
	}
	scoped, hide, release := fixture.set.WithinTransaction(fixture.count)
	defer release()
	if !runtimeSameConditions(t, base, runtimeReadConditions(t, scoped, fixture.user, fixture.handle, fixture.id)) {
		t.Fatal("an operation set widened row reads outside the aggregate view")
	}
	view := scoped.AggregateView()
	widened := runtimeReadConditions(t, view, fixture.user, fixture.handle, fixture.id)
	if runtimeSameConditions(t, base[:1], widened[:1]) {
		t.Fatal("CanCount did not widen the aggregate row reach")
	}
	if !runtimeSameConditions(t, base[1:], widened[1:]) {
		t.Fatal("CanCount widened a field lens")
	}
	if !runtimeSameConditions(t, base, runtimeReadConditions(t, scoped, fixture.user, fixture.handle, fixture.id)) {
		t.Fatal("taking an aggregate view widened the operation set it came from")
	}
	summing, _, releaseSum := fixture.set.WithinTransaction(fixture.sum)
	defer releaseSum()
	named := runtimeReadConditions(t, summing.AggregateView(), fixture.user, fixture.handle, fixture.id)
	if runtimeSameConditions(t, base[:2], named[:2]) {
		t.Fatal("CanAggregate did not widen the row reach and the named field")
	}
	if !runtimeSameConditions(t, base[2:], named[2:]) {
		t.Fatal("CanAggregate widened a field it does not name")
	}
	hide()
	if !runtimeSameConditions(t, base, runtimeReadConditions(t, view, fixture.user, fixture.handle, fixture.id)) {
		t.Fatal("an aggregate view kept its grant after the operation returned")
	}
	invite, releaseInvite := fixture.set.Within(golem.OperationID{})
	defer releaseInvite()
	if !runtimeSameConditions(t, base, runtimeReadConditions(t, invite.AggregateView(), fixture.user, fixture.handle, fixture.id)) {
		t.Fatal("an operation without aggregate grants widened its aggregate view")
	}
}

func TestAggregatePolicyIsBuiltOnlyWhenAnAggregateViewAsksForIt(t *testing.T) {
	fixture := newRuntimeAggregateFixture(t)
	entry := fixture.set.policies[fixture.user]
	if _, ok := fixture.set.Policy(fixture.user); !ok {
		t.Fatal("base policy is absent")
	}
	if entry.aggregates != nil || len(entry.aggregateFrozen) != 2 {
		t.Fatalf("aggregate policies were bound eagerly: bound=%d frozen=%d", len(entry.aggregates), len(entry.aggregateFrozen))
	}
	scoped, _, release := fixture.set.WithinTransaction(fixture.count)
	defer release()
	if _, ok := scoped.Policy(fixture.user); !ok || entry.aggregates != nil {
		t.Fatal("an operation set bound aggregate policies outside an aggregate view")
	}
	if _, ok := scoped.AggregateView().Policy(fixture.user); !ok || len(entry.aggregates) != 2 {
		t.Fatalf("the aggregate view did not bind the model's aggregate policies: %d", len(entry.aggregates))
	}
}

func TestBuildRejectsAggregateGrantsNamingAnythingButATransactionOperation(t *testing.T) {
	registry, fixture := runtimeSchemaFixture(t)
	handle := golem.GeneratedTextField[runtimeUser, string](fixture.userHandle)
	for name, grant := range map[string]func(*golem.Rules[runtimeUser]){
		"runtimeInviteOperation": func(rules *golem.Rules[runtimeUser]) {
			golem.Within(rules, runtimeInviteOperation).CanCount(golem.All[runtimeUser]())
		},
		"runtimeRenameOperation": func(rules *golem.Rules[runtimeUser]) {
			golem.Within(rules, runtimeRenameOperation).CanAggregate(golem.All[runtimeUser](), handle)
		},
	} {
		t.Run(name, func(t *testing.T) {
			binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(runtimeActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[runtimeUser]()
				rules.CanRead(handle.Eq("owned"))
				grant(rules)
				return rules.Freeze(fixture.user)
			})
			bindings, err := golem.GeneratedApplicationOperations(runtimeBindings(t, fixture.generation, binding), golem.GeneratedCustomMutationOperation("invite", runtimeInviteOperation))
			if err != nil {
				t.Fatal(err)
			}
			set, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
			if err != nil {
				t.Fatal(err)
			}
			if err := runtimePolicyError(t, set, policyir.ModelID(fixture.user)); err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "transaction operation") {
				t.Fatalf("aggregate grant error=%v", err)
			}
		})
	}
}
