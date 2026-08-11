package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func postCommentsField(t *testing.T) golem.FieldID {
	t.Helper()
	comment := p5social.GolemGeneratedCommentDescriptor.Metadata().ModelID()
	for _, relation := range p5social.GolemGeneratedPostDescriptor.Metadata().Relations() {
		if relation.TargetModelID() == comment {
			return relation.FieldID()
		}
	}
	t.Fatal("the fixture post model declares no relation to comments")
	return golem.FieldID{}
}

func acceptsReach(t *testing.T, registry *schema.Registry, policy ir.Policy, model golem.ModelID, fields []ir.FieldID, reach ir.Condition) bool {
	t.Helper()
	_, err := classify.NewRequestWithConstraint(registry, policy, ir.ModelID(model), fields, classify.UseProjection, ir.ActionRead, reach)
	return err == nil
}

func TestPolicyTestKitNarrowerReachDischargesButNeverWidensPolicy(t *testing.T) {
	bundle := p5social.GolemGeneratedSchemaBundle()
	bindings, err := p5social.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	actor := p5social.Actor{AllowedTag: "alpha"}

	kit := socialKit(t)
	policies, err := kit.ForActor(actor)
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}

	t.Run("ANarrowerReachIsAcceptedAndDischargesAConditionalField", func(t *testing.T) {
		model := kernelPostModel(t)
		policy, registry, providers := productionPolicy(t, bindings, actor, bundle, model)
		comments := ir.FieldID(postCommentsField(t))
		fields := []ir.FieldID{comments}

		baseline, baselineErr := classify.NewRequest(registry, policy, ir.ModelID(model), fields, classify.UseProjection, ir.ActionRead)
		if baselineErr != nil {
			t.Fatalf("classify.NewRequest: %v", baselineErr)
		}
		basePlan, basePlanErr := classify.Fields(baseline)
		if basePlanErr != nil {
			t.Fatalf("classify.Fields: %v", basePlanErr)
		}
		base, present := basePlan.Field(comments)
		if !present || base.Access() != classify.AccessConditional {
			t.Fatal("the fixture field is not conditionally readable, so this gate proves nothing")
		}
		if base.DischargedByConstraint() {
			t.Fatal("the field is already discharged without a reach, so a narrower reach cannot be shown to discharge it")
		}

		narrower := boundPredicate(t, p5social.Posts.Title.EndsWith("-open"), p5social.GolemGeneratedPostDescriptor, registry, providers)
		if !acceptsReach(t, registry, policy, model, fields, narrower) {
			t.Fatal("production refused a reach that narrows the action row constraint")
		}
		request, requestErr := classify.NewRequestWithConstraint(registry, policy, ir.ModelID(model), fields, classify.UseProjection, ir.ActionRead, narrower)
		if requestErr != nil {
			t.Fatalf("classify.NewRequestWithConstraint: %v", requestErr)
		}
		plan, planErr := classify.Fields(request)
		if planErr != nil {
			t.Fatalf("classify.Fields: %v", planErr)
		}
		reached, reachedPresent := plan.Field(comments)
		if !reachedPresent || !reached.DischargedByConstraint() {
			t.Fatal("a narrower reach did not discharge the conditional field it covers")
		}

		posts, postsErr := Model(policies, p5social.GolemGeneratedPostDescriptor)
		if postsErr != nil {
			t.Fatalf("Model: %v", postsErr)
		}
		constraint, constraintErr := posts.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		implied, impliedErr := Implies(constraint, p5social.Posts.Title.EndsWith("-open"))
		if impliedErr != nil {
			t.Fatalf("Implies: %v", impliedErr)
		}
		if implied {
			t.Fatal("the kit reported the action constraint to already guarantee a strictly narrower reach")
		}
	})

	t.Run("AReachThatWidensTheActionConstraintIsRefused", func(t *testing.T) {
		model := p5social.GolemGeneratedTagDescriptor.Metadata().ModelID()
		policy, registry, providers := productionPolicy(t, bindings, actor, bundle, model)
		fields := make([]ir.FieldID, 0)
		for _, field := range p5social.GolemGeneratedTagDescriptor.Metadata().ScanFields() {
			fields = append(fields, ir.FieldID(field))
		}

		tags, tagsErr := Model(policies, p5social.GolemGeneratedTagDescriptor)
		if tagsErr != nil {
			t.Fatalf("Model: %v", tagsErr)
		}
		constraint, constraintErr := tags.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		if _, constant := constraint.Constant(); constant {
			t.Fatal("the action constraint is constant, so nothing can widen it")
		}

		candidates := []struct {
			name      string
			predicate golem.Predicate[p5social.Tag]
			widens    bool
		}{
			{"Restated", p5social.Tags.Name.Eq("alpha"), false},
			{"Narrower", p5social.Tags.Name.Eq("alpha").And(p5social.Tags.ID.Eq(golem.UUID{0x31, 0x32, 0x33})), false},
			{"WiderDisjunction", p5social.Tags.Name.Eq("alpha").Or(p5social.Tags.Name.Eq("beta")), true},
			{"Unconditional", golem.All[p5social.Tag](), true},
		}

		widened := 0
		for _, candidate := range candidates {
			implied, impliedErr := Implies(constraint, candidate.predicate)
			if impliedErr != nil {
				t.Fatalf("%s: Implies: %v", candidate.name, impliedErr)
			}
			equivalent, equivalentErr := Equivalent(constraint, candidate.predicate)
			if equivalentErr != nil {
				t.Fatalf("%s: Equivalent: %v", candidate.name, equivalentErr)
			}
			widens := implied && !equivalent
			if widens != candidate.widens {
				t.Fatalf("%s: the kit reports widens=%v, want %v", candidate.name, widens, candidate.widens)
			}
			if widens {
				widened++
			}
			reach := boundPredicate(t, candidate.predicate, p5social.GolemGeneratedTagDescriptor, registry, providers)
			accepted := acceptsReach(t, registry, policy, model, fields, reach)
			if accepted == widens {
				t.Fatalf("%s: production accepted=%v while the kit reports widens=%v; the kit disagrees with the production reach rule", candidate.name, accepted, widens)
			}
		}
		if widened != 2 {
			t.Fatalf("the candidate set exercised %d widenings, want 2", widened)
		}
	})

	t.Run("AnUnsatisfiableReachIsRefusedRatherThanProvedVacuously", func(t *testing.T) {
		model := p5social.GolemGeneratedTagDescriptor.Metadata().ModelID()
		policy, registry, providers := productionPolicy(t, bindings, actor, bundle, model)
		fields := make([]ir.FieldID, 0)
		for _, field := range p5social.GolemGeneratedTagDescriptor.Metadata().ScanFields() {
			fields = append(fields, ir.FieldID(field))
		}
		empty := boundPredicate(t, golem.None[p5social.Tag](), p5social.GolemGeneratedTagDescriptor, registry, providers)
		if acceptsReach(t, registry, policy, model, fields, empty) {
			t.Fatal("production accepted an unsatisfiable reach as a proof of policy preservation")
		}

		tags, tagsErr := Model(policies, p5social.GolemGeneratedTagDescriptor)
		if tagsErr != nil {
			t.Fatalf("Model: %v", tagsErr)
		}
		constraint, constraintErr := tags.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		implied, impliedErr := Implies(constraint, golem.None[p5social.Tag]())
		if impliedErr != nil {
			t.Fatalf("Implies: %v", impliedErr)
		}
		if implied {
			t.Fatal("the kit proved a satisfiable constraint entails an unsatisfiable predicate")
		}
	})
}
