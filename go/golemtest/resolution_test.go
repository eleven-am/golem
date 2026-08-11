package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func kernelAuthor() golem.UUID {
	return golem.UUID{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50}
}

func kernelActions() []struct {
	name   string
	public golem.FrozenAction
	inner  ir.Action
} {
	return []struct {
		name   string
		public golem.FrozenAction
		inner  ir.Action
	}{
		{"Read", golem.FrozenActionRead, ir.ActionRead},
		{"Create", golem.FrozenActionCreate, ir.ActionCreate},
		{"Update", golem.FrozenActionUpdate, ir.ActionUpdate},
		{"Delete", golem.FrozenActionDelete, ir.ActionDelete},
	}
}

func TestPolicyTestKitRowConstraintMatchesProductionResolver(t *testing.T) {
	bundle := p5social.GolemGeneratedSchemaBundle()
	post := kernelPostModel(t)
	actor := kernelActor{Author: kernelAuthor()}

	kit := kernelKit(t)
	policies, err := kit.ForActor(actor)
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}
	posts, err := Model(policies, p5social.GolemGeneratedPostDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	t.Run("EveryActionMatchesTheProductionResolversOwnAnswer", func(t *testing.T) {
		distinct := make(map[string]struct{})
		for _, action := range kernelActions() {
			constraint, constraintErr := posts.RowConstraint(action.public)
			if constraintErr != nil {
				t.Fatalf("%s: RowConstraint: %v", action.name, constraintErr)
			}
			expected := canonical(t, productionRowConstraint(t, kernelBindings(t), actor, bundle, post, action.inner))
			if string(constraint.CanonicalBytes()) != expected {
				t.Fatalf("%s: the kit answered a different constraint than the production resolver", action.name)
			}
			distinct[expected] = struct{}{}
		}
		if len(distinct) < 3 {
			t.Fatalf("the fixture produced %d distinct action constraints, so an action mix-up would go unnoticed", len(distinct))
		}
	})

	t.Run("ReadMatchesTheConstraintTheRuntimeClassifierUses", func(t *testing.T) {
		policy, registry, _ := productionPolicy(t, kernelBindings(t), actor, bundle, post)
		fields := p5social.GolemGeneratedPostDescriptor.Metadata().ScanFields()
		inner := make([]ir.FieldID, 0, len(fields))
		for _, field := range fields {
			inner = append(inner, ir.FieldID(field))
		}
		request, requestErr := classify.NewRequest(registry, policy, ir.ModelID(post), inner, classify.UseProjection, ir.ActionRead)
		if requestErr != nil {
			t.Fatalf("classify.NewRequest: %v", requestErr)
		}
		constraint, constraintErr := posts.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		if string(constraint.CanonicalBytes()) != canonical(t, request.SelectingConstraint()) {
			t.Fatal("the kit answered a different read constraint than the runtime classifier resolves")
		}
	})

	t.Run("TheModelWideDenyRuleShapesTheResolvedReadConstraint", func(t *testing.T) {
		constraint, constraintErr := posts.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		if _, constant := constraint.Constant(); constant {
			t.Fatal("the read constraint collapsed to a constant, so the deny rule was not honoured")
		}
		expected := golem.Not(p5social.Posts.Title.EndsWith(kernelSecretSuffix()))
		equivalent, equivalentErr := Equivalent(constraint, expected)
		if equivalentErr != nil {
			t.Fatalf("Equivalent: %v", equivalentErr)
		}
		if !equivalent {
			t.Fatal("the resolved read constraint does not exclude exactly the rows the deny rule denies")
		}
		granted, grantedErr := Implies(constraint, golem.All[p5social.Post]())
		if grantedErr != nil {
			t.Fatalf("Implies: %v", grantedErr)
		}
		if !granted {
			t.Fatal("the resolved read constraint does not entail the unconditional grant it was built from")
		}
	})

	t.Run("ConditionalGrantsShapeTheResolvedWriteConstraints", func(t *testing.T) {
		owned := p5social.Posts.AuthorID.Eq(kernelAuthor())
		for _, action := range []golem.FrozenAction{golem.FrozenActionCreate, golem.FrozenActionUpdate} {
			constraint, constraintErr := posts.RowConstraint(action)
			if constraintErr != nil {
				t.Fatalf("RowConstraint: %v", constraintErr)
			}
			equivalent, equivalentErr := Equivalent(constraint, owned)
			if equivalentErr != nil {
				t.Fatalf("Equivalent: %v", equivalentErr)
			}
			if !equivalent {
				t.Fatal("a write constraint is not the conditional grant the policy declares")
			}
		}
		deletes, deleteErr := posts.RowConstraint(golem.FrozenActionDelete)
		if deleteErr != nil {
			t.Fatalf("RowConstraint: %v", deleteErr)
		}
		value, constant := deletes.Constant()
		if !constant || value {
			t.Fatal("an unauthored action did not resolve to a refused constant")
		}
	})

	t.Run("TheActorReachesOnlyTheRowsItsOwnPolicyAllows", func(t *testing.T) {
		stock := socialKit(t)
		stockPolicies, stockErr := stock.ForActor(p5social.Actor{AllowedTag: "alpha"})
		if stockErr != nil {
			t.Fatalf("ForActor: %v", stockErr)
		}
		tags, tagErr := Model(stockPolicies, p5social.GolemGeneratedTagDescriptor)
		if tagErr != nil {
			t.Fatalf("Model: %v", tagErr)
		}
		constraint, constraintErr := tags.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		equivalent, equivalentErr := Equivalent(constraint, p5social.Tags.Name.Eq("alpha"))
		if equivalentErr != nil {
			t.Fatalf("Equivalent: %v", equivalentErr)
		}
		if !equivalent {
			t.Fatal("the actor-derived read constraint is not the reach the application declared for it")
		}
		other, otherErr := Equivalent(constraint, p5social.Tags.Name.Eq("beta"))
		if otherErr != nil {
			t.Fatalf("Equivalent: %v", otherErr)
		}
		if other {
			t.Fatal("the read constraint of one actor matched another actor's reach")
		}
	})

	t.Run("RowConstraintRejectsAnUnknownActionAndAnUnbuiltPolicy", func(t *testing.T) {
		_, unknownErr := posts.RowConstraint(golem.FrozenAction(0))
		expectCode(t, unknownErr, ErrorInvalidInput, "unknown action tag")
		_, zeroErr := ModelPolicy[p5social.Post]{}.RowConstraint(golem.FrozenActionRead)
		expectCode(t, zeroErr, ErrorInvalidInput, "zero model policy")
	})
}

func TestPolicyTestKitEquivalenceIsProvedRatherThanCompared(t *testing.T) {
	bundle := p5social.GolemGeneratedSchemaBundle()
	post := kernelPostModel(t)
	actor := kernelActor{Author: kernelAuthor()}

	kit := kernelKit(t)
	policies, err := kit.ForActor(actor)
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}
	posts, err := Model(policies, p5social.GolemGeneratedPostDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	constraint, err := posts.RowConstraint(golem.FrozenActionRead)
	if err != nil {
		t.Fatalf("RowConstraint: %v", err)
	}

	visible := golem.Not(p5social.Posts.Title.EndsWith(kernelSecretSuffix()))
	owned := p5social.Posts.AuthorID.Eq(kernelAuthor())
	restated := visible.Or(golem.And(visible, owned))

	registry, providers := productionRegistry(t, bundle)
	restatedBytes := canonical(t, boundPredicate(t, restated, p5social.GolemGeneratedPostDescriptor, registry, providers))
	if string(constraint.CanonicalBytes()) == restatedBytes {
		t.Fatal("the restated predicate encodes identically to the constraint, so this gate cannot tell proof from comparison")
	}
	if ir.ModelID(post) == (ir.ModelID{}) {
		t.Fatal("the fixture model has a zero identity")
	}

	equivalent, err := Equivalent(constraint, restated)
	if err != nil {
		t.Fatalf("Equivalent: %v", err)
	}
	if !equivalent {
		t.Fatal("two logically equivalent predicates that encode differently were not proved equivalent")
	}

	forward, err := Implies(constraint, restated)
	if err != nil {
		t.Fatalf("Implies: %v", err)
	}
	if !forward {
		t.Fatal("the constraint was not proved to imply its own restatement")
	}

	unequal, err := Equivalent(constraint, owned)
	if err != nil {
		t.Fatalf("Equivalent: %v", err)
	}
	if unequal {
		t.Fatal("a strictly narrower predicate was proved equivalent to the constraint")
	}
}
