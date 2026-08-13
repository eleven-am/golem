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

func tagScanFields(t *testing.T) []ir.FieldID {
	t.Helper()
	fields := make([]ir.FieldID, 0)
	for _, field := range p5social.GolemGeneratedTagDescriptor.Metadata().ScanFields() {
		fields = append(fields, ir.FieldID(field))
	}
	return fields
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
	posts, err := Model(policies, p5social.GolemGeneratedPostDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	tags, err := Model(policies, p5social.GolemGeneratedTagDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	t.Run("ANarrowerReachDischargesAConditionalFieldTheRowConstraintDoesNot", func(t *testing.T) {
		open := p5social.Posts.Title.EndsWith("-open")

		constraint, constraintErr := posts.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		implied, impliedErr := Implies(constraint, open)
		if impliedErr != nil {
			t.Fatalf("Implies: %v", impliedErr)
		}
		if implied {
			t.Fatal("the action constraint already guarantees the reach, so no discharge transition can be shown")
		}

		bare, bareErr := posts.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Posts.Comments)
		if bareErr != nil {
			t.Fatalf("ClassifyReadFields: %v", bareErr)
		}
		undischarged, present := bare.Field(p5social.Posts.Comments)
		if !present || undischarged.Access() != AccessConditional {
			t.Fatalf("the fixture field is not conditionally readable, so this gate proves nothing: access=%v present=%v", undischarged.Access(), present)
		}
		if undischarged.DischargedByConstraint() {
			t.Fatal("the field is already discharged without a reach, so a narrower reach cannot be shown to discharge it")
		}

		reached, reachedErr := posts.ClassifyReadFieldsWithReach(UseProjection, golem.FrozenActionRead, open, p5social.Posts.Comments)
		if reachedErr != nil {
			t.Fatalf("ClassifyReadFieldsWithReach: %v", reachedErr)
		}
		discharged, present := reached.Field(p5social.Posts.Comments)
		if !present {
			t.Fatal("the reached plan lost the field it was asked to classify")
		}
		if !discharged.DischargedByConstraint() {
			t.Fatal("a narrower reach did not discharge the conditional field it covers")
		}
		if discharged.Access() != AccessConditional {
			t.Fatalf("a narrower reach changed the field's readability to %v; a reach discharges a condition, it does not widen a grant", discharged.Access())
		}

		bareCondition, _ := undischarged.Condition()
		reachedCondition, _ := discharged.Condition()
		if string(bareCondition.CanonicalBytes()) != string(reachedCondition.CanonicalBytes()) {
			t.Fatal("a narrower reach rewrote the field condition instead of discharging it")
		}
		equivalent, equivalentErr := Equivalent(reachedCondition, open)
		if equivalentErr != nil {
			t.Fatalf("Equivalent: %v", equivalentErr)
		}
		if !equivalent {
			t.Fatal("the discharged field condition is not the predicate the policy declared for it")
		}

		policy, registry, providers := productionPolicy(t, bindings, actor, bundle, kernelPostModel(t))
		narrower := boundPredicate(t, open, p5social.GolemGeneratedPostDescriptor, registry, providers)
		if !acceptsReach(t, registry, policy, kernelPostModel(t), []ir.FieldID{ir.FieldID(postCommentsField(t))}, narrower) {
			t.Fatal("production refused a reach the kit accepted")
		}
	})

	t.Run("AReachThatWidensTheActionConstraintIsRefused", func(t *testing.T) {
		model := p5social.GolemGeneratedTagDescriptor.Metadata().ModelID()
		policy, registry, providers := productionPolicy(t, bindings, actor, bundle, model)

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

			_, reachErr := tags.ClassifyReadFieldsWithReach(UseProjection, golem.FrozenActionRead, candidate.predicate, p5social.Tags.ID, p5social.Tags.Name)
			if widens {
				if reachErr == nil {
					t.Fatalf("%s: the kit classified fields against a reach that widens the actor's row constraint", candidate.name)
				}
				expectCode(t, reachErr, ErrorPolicyAnalysis, candidate.name)
			} else if reachErr != nil {
				t.Fatalf("%s: the kit refused a reach that does not widen the actor's row constraint: %v", candidate.name, reachErr)
			}

			reach := boundPredicate(t, candidate.predicate, p5social.GolemGeneratedTagDescriptor, registry, providers)
			accepted := acceptsReach(t, registry, policy, model, tagScanFields(t), reach)
			if accepted != (reachErr == nil) {
				t.Fatalf("%s: production accepted=%v while the kit accepted=%v; the kit disagrees with the production reach rule", candidate.name, accepted, reachErr == nil)
			}
		}
		if widened != 2 {
			t.Fatalf("the candidate set exercised %d widenings, want 2", widened)
		}
	})

	t.Run("AnUnsatisfiableReachIsRefusedRatherThanProvedVacuously", func(t *testing.T) {
		model := p5social.GolemGeneratedTagDescriptor.Metadata().ModelID()
		policy, registry, providers := productionPolicy(t, bindings, actor, bundle, model)

		_, reachErr := tags.ClassifyReadFieldsWithReach(UseProjection, golem.FrozenActionRead, golem.None[p5social.Tag](), p5social.Tags.ID, p5social.Tags.Name)
		if reachErr == nil {
			t.Fatal("the kit accepted an unsatisfiable reach as a proof of policy preservation")
		}
		expectCode(t, reachErr, ErrorPolicyAnalysis, "unsatisfiable reach")

		empty := boundPredicate(t, golem.None[p5social.Tag](), p5social.GolemGeneratedTagDescriptor, registry, providers)
		if acceptsReach(t, registry, policy, model, tagScanFields(t), empty) {
			t.Fatal("production accepted an unsatisfiable reach as a proof of policy preservation")
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

	t.Run("ANarrowerReachNeverMakesADeniedFieldReadable", func(t *testing.T) {
		comments := maskComments(t)
		narrower := p5social.Comments.Body.EndsWith(maskOpenSuffix())

		bare, bareErr := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Comments.PostID, p5social.Comments.ID)
		if bareErr != nil {
			t.Fatalf("ClassifyReadFields: %v", bareErr)
		}
		reached, reachedErr := comments.ClassifyReadFieldsWithReach(UseProjection, golem.FrozenActionRead, narrower, p5social.Comments.PostID, p5social.Comments.ID)
		if reachedErr != nil {
			t.Fatalf("ClassifyReadFieldsWithReach: %v", reachedErr)
		}

		denied, present := reached.Field(p5social.Comments.PostID)
		if !present || denied.Access() != AccessNever {
			t.Fatalf("a narrower reach changed a denied field to %v present=%v", denied.Access(), present)
		}
		if denied.DischargedByConstraint() {
			t.Fatal("a never-readable field reported a discharge, which would claim a caller filter can buy access")
		}
		if before, _ := bare.Field(p5social.Comments.PostID); before.Access() != denied.Access() {
			t.Fatal("a reach changed a denied field's readability")
		}

		granted, present := reached.Field(p5social.Comments.ID)
		if !present || granted.Access() != AccessAlways {
			t.Fatalf("a narrower reach changed an always-readable field to %v present=%v", granted.Access(), present)
		}
		if granted.DischargedByConstraint() {
			t.Fatal("an always-readable field reported a discharge that is meaningless for it")
		}
	})
}
