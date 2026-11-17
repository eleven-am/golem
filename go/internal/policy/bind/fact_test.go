package bind

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func TestPublicFactsBindPhysicalBodiesAndPreserveTheActorDomain(t *testing.T) {
	registry, ids := bindFixture(t)
	owner := golem.GeneratedEqualField[testPost, golem.UUID](ids.postAuthorID)
	author := golem.GeneratedToOne[testPost, testUser](ids.postAuthor, ids.relation)
	name := golem.GeneratedNullableTextField[testUser, string](ids.userName)
	actor := golem.UUID{15: 7}
	rules := golem.NewRules[testPost]()
	rules.FactScope(owner.Eq(actor))
	ready := rules.Fact("ready", author.Is(name.Eq("enabled")))
	visible := rules.Fact("visible", ready)
	rules.CanRead(visible)
	rules.CanUpdate(ready.Not())
	frozen, err := rules.Freeze(ids.post)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := Policy(frozen, registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Facts()) != 2 || policy.FactScope().ModelID() != ir.ModelID(ids.post) {
		t.Fatal("public binding lost the root-owned definitions or scope")
	}
	for _, action := range []ir.Action{ir.ActionRead, ir.ActionUpdate} {
		condition, err := resolve.RowConstraint(policy, action, ir.ModelID(ids.post))
		if err != nil || !ir.HasFacts(condition) {
			t.Fatalf("action %d did not resolve its physical fact: %v", action, err)
		}
		for _, owned := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				userName := "disabled"
				if enabled {
					userName = "enabled"
				}
				value, _ := ir.StringValue(userName)
				userField, _ := evaluate.ValueField(ir.FieldID(ids.userName), value)
				user, _ := evaluate.NewRecord(ir.ModelID(ids.user), userField)
				related, _ := evaluate.ToOneField(ir.FieldID(ids.postAuthor), ir.ModelID(ids.user), user)
				ownerID := actor
				if !owned {
					ownerID = golem.UUID{15: 8}
				}
				ownerField, _ := evaluate.ValueField(ir.FieldID(ids.postAuthorID), ir.UUIDValue(ownerID.Bytes()))
				post, _ := evaluate.NewRecord(ir.ModelID(ids.post), ownerField, related)
				got, err := evaluate.Condition(condition, post, ir.PortableProviders())
				want := owned && (enabled == (action == ir.ActionRead))
				if err != nil || got != want {
					t.Fatalf("action=%d owned=%v enabled=%v result=%v want=%v err=%v", action, owned, enabled, got, want, err)
				}
			}
		}
	}
	for _, predicate := range []golem.Predicate[testPost]{ready, ready.And(golem.None[testPost]()), ready.Or(golem.All[testPost]())} {
		caller, err := predicate.Freeze(modelDescriptor[testPost](ids.post))
		if err != nil {
			t.Fatal(err)
		}
		for _, bind := range []func(golem.FrozenPredicate, *schema.Registry, ir.ProviderSet) (ir.Condition, error){Predicate, ExactPredicate} {
			if _, err := bind(caller, registry, ir.PortableProviders()); err == nil {
				t.Fatal("caller binding accepted a policy fact, including its dead branches")
			}
		}
	}
}
