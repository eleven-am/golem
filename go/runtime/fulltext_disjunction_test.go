package runtime

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
)

func TestTextSearchDisjunctivePolicyAndPredicateReturnOnlyAuthorizedRows(t *testing.T) {
	ctx := context.Background()
	fixture := newFullTextMutationFixture(t)
	for index, title := range []string{"alpha one", "alpha two", "alpha three", "alpha four"} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(byte(80+index), golem.UUID{15: 1}, title)); err != nil {
			t.Fatal(err)
		}
	}
	caller := mustMutationResultCaller(t, fixture)
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(fixture.title.Eq("alpha one").Or(fixture.title.Eq("alpha two")))
		return rules.Freeze(fixture.schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(
		fixture.schema.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	caller.policies, err = policyruntime.Build(policyruntime.BuildRequest[mutationResultActor]{
		Bindings: bindings, Actor: mutationResultActor{}, Registry: fixture.app.registry,
		Provider: fixture.app.provider, Capabilities: fixture.app.capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := CallerTextSearchSelect(ctx, caller, fixture.postDescriptor, runtimeFullTextIndexName, "alpha", 10, golem.Select[mutationResultPost](fixture.title), fixture.title.Eq("alpha two").Or(fixture.title.Eq("alpha three")))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, row := range rows {
		title, _ := golem.Value(row.Row(), fixture.title).Get()
		got = append(got, title)
	}
	sort.Strings(got)
	if want := []string{"alpha two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disjunctive policy and predicate full-text rows=%q want=%q", got, want)
	}
}
