package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

type maskActor struct {
	Author golem.UUID
}

func maskAuthor() golem.UUID {
	return golem.UUID{0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x7b, 0x7c, 0x7d, 0x7e, 0x7f, 0x80}
}

func maskOpenSuffix() string { return "-open" }

func maskCommentModel(t *testing.T) golem.ModelID {
	t.Helper()
	return p5social.GolemGeneratedCommentDescriptor.Metadata().ModelID()
}

func maskFactory(model, comment golem.ModelID) golem.PolicyFactory[maskActor] {
	return func(actor maskActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[p5social.Comment]()
		all := golem.All[p5social.Comment]()
		rules.CanRead(all)
		if model == comment {
			rules.CanReadFields(all, p5social.Comments.ID, p5social.Comments.AuthorID, p5social.Comments.Post, p5social.Comments.Author)
			rules.CannotReadFields(all, p5social.Comments.PostID, p5social.Comments.Body, p5social.Comments.ParentID, p5social.Comments.ReplyTo, p5social.Comments.Replies)
			rules.CanReadFields(p5social.Comments.AuthorID.Eq(actor.Author), p5social.Comments.Body)
			rules.CanReadFields(p5social.Comments.Post.Is(p5social.Posts.Title.EndsWith(maskOpenSuffix())), p5social.Comments.ParentID)
		}
		return rules.Freeze(model)
	}
}

func maskBindings(t *testing.T) golem.ApplicationBindings[maskActor] {
	t.Helper()
	descriptors, err := p5social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	comment := maskCommentModel(t)
	generation := p5social.GolemGeneratedSchemaBundle().GenerationDigest()
	policies := make([]golem.PolicyBinding[maskActor], 0)
	for _, metadata := range descriptors.Models() {
		policies = append(policies, golem.GeneratedPolicyBinding[maskActor, p5social.Comment](metadata.ModelID(), maskFactory(metadata.ModelID(), comment)))
	}
	bindings, err := golem.GeneratedApplicationBindings(
		generation,
		golem.GeneratedStampedPackageBindings(generation, policies, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return bindings
}

func maskComments(t *testing.T) ModelPolicy[p5social.Comment] {
	t.Helper()
	descriptors, err := p5social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	kit, err := New(maskBindings(t), descriptors, p5social.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	policies, err := kit.ForActor(maskActor{Author: maskAuthor()})
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}
	comments, err := Model(policies, p5social.GolemGeneratedCommentDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	return comments
}

type runtimePolicySet map[ir.ModelID]ir.Policy

func (set runtimePolicySet) Policy(model ir.ModelID) (ir.Policy, bool) {
	policy, present := set[model]
	return policy, present
}

func productionPolicySet[A any](t *testing.T, bindings golem.ApplicationBindings[A], actor A, bundle golem.SchemaBundle) (runtimePolicySet, *schema.Registry) {
	t.Helper()
	registry, providers := productionRegistry(t, bundle)
	built, err := golem.BuildGeneratedPolicySet(bindings, actor)
	if err != nil {
		t.Fatalf("BuildGeneratedPolicySet: %v", err)
	}
	set := make(runtimePolicySet)
	for _, frozen := range built.Policies() {
		bound, bindErr := bind.Policy(frozen, registry, providers)
		if bindErr != nil {
			t.Fatalf("bind.Policy: %v", bindErr)
		}
		normalized, normalizeErr := normalize.Policy(bound)
		if normalizeErr != nil {
			t.Fatalf("normalize.Policy: %v", normalizeErr)
		}
		set[ir.ModelID(frozen.View().ModelID())] = normalized
	}
	if len(set) == 0 {
		t.Fatal("the production builder produced no policies")
	}
	return set, registry
}

func maskRelation(t *testing.T, metadata golem.ModelMetadata, field golem.FieldID) golem.RelationMetadata {
	t.Helper()
	for _, relation := range metadata.Relations() {
		if relation.FieldID() == field {
			return relation
		}
	}
	t.Fatal("the fixture model declares no relation on the requested field")
	return golem.RelationMetadata{}
}

func maskScalarSelection(t *testing.T, field golem.FieldID) readir.Selection {
	t.Helper()
	selection, err := readir.NewScalarSelection(ir.FieldID(field))
	if err != nil {
		t.Fatalf("readir.NewScalarSelection: %v", err)
	}
	return selection
}

func maskRelationSelection(t *testing.T, field golem.FieldID, child golem.FieldID) readir.Selection {
	t.Helper()
	relation := maskRelation(t, p5social.GolemGeneratedCommentDescriptor.Metadata(), field)
	childRequest, err := readir.NewRequest(readir.RequestInput{
		Operation:  readir.FindMany,
		Model:      ir.ModelID(relation.TargetModelID()),
		Projection: readir.ProjectionSelect,
		Selection:  []readir.Selection{maskScalarSelection(t, child)},
	})
	if err != nil {
		t.Fatalf("readir.NewRequest child: %v", err)
	}
	selection, err := readir.NewRelationSelection(ir.FieldID(field), ir.RelationID(relation.RelationID()), ir.ModelID(relation.TargetModelID()), childRequest)
	if err != nil {
		t.Fatalf("readir.NewRelationSelection: %v", err)
	}
	return selection
}

func maskRuntimePlan(t *testing.T, selections ...readir.Selection) (readplan.Plan, error) {
	t.Helper()
	policies, registry := productionPolicySet(t, maskBindings(t), maskActor{Author: maskAuthor()}, p5social.GolemGeneratedSchemaBundle())
	request, err := readir.NewRequest(readir.RequestInput{
		Operation:  readir.FindMany,
		Model:      ir.ModelID(maskCommentModel(t)),
		Projection: readir.ProjectionSelect,
		Selection:  selections,
	})
	if err != nil {
		t.Fatalf("readir.NewRequest: %v", err)
	}
	return readplan.Caller(request, registry, policies, readplan.DefaultLimits())
}

func maskRuntimeField(t *testing.T, plan readplan.Plan, field golem.FieldID) readplan.Field {
	t.Helper()
	for _, value := range plan.Fields() {
		if value.FieldID() == ir.FieldID(field) {
			return value
		}
	}
	t.Fatal("the runtime read plan does not carry the requested field")
	return readplan.Field{}
}

func TestPolicyTestKitFieldClassificationMatchesRuntimeMasking(t *testing.T) {
	comments := maskComments(t)
	metadata := p5social.GolemGeneratedCommentDescriptor.Metadata()

	identity := func(field golem.Field[p5social.Comment]) golem.FieldID {
		t.Helper()
		value, ok := golem.FieldIdentity(field)
		if !ok {
			t.Fatal("a generated field handle carries no identity")
		}
		return value
	}
	alwaysField := identity(p5social.Comments.ID)
	ownedField := identity(p5social.Comments.Body)
	reachedField := identity(p5social.Comments.ParentID)
	neverField := identity(p5social.Comments.PostID)

	plan, err := comments.ClassifyReadFields(
		UseProjection,
		golem.FrozenActionRead,
		p5social.Comments.ID,
		p5social.Comments.Body,
		p5social.Comments.ParentID,
		p5social.Comments.PostID,
		p5social.Comments.ID,
	)
	if err != nil {
		t.Fatalf("ClassifyReadFields: %v", err)
	}

	t.Run("TheKitAnswersTheDeclaredPolicyForEveryReadability", func(t *testing.T) {
		if plan.ModelID() != metadata.ModelID() || plan.UseKind() != UseProjection || plan.SelectingAction() != golem.FrozenActionRead {
			t.Fatal("the plan does not describe the question that was asked")
		}
		order := []golem.FieldID{alwaysField, ownedField, reachedField, neverField}
		actual := plan.Fields()
		if len(actual) != len(order) {
			t.Fatalf("the plan classifies %d fields, want %d; a repeated field was not removed after its first occurrence", len(actual), len(order))
		}
		for index, expected := range order {
			if actual[index].FieldID() != expected {
				t.Fatalf("classification %d is not the caller's %dth distinct field; first-seen order was not preserved", index, index)
			}
		}

		always, present := plan.Field(p5social.Comments.ID)
		if !present || always.Access() != AccessAlways {
			t.Fatalf("an unconditionally granted field classified as %v present=%v", always.Access(), present)
		}
		if _, conditional := always.Condition(); conditional {
			t.Fatal("an always-readable field reported a condition it does not need")
		}
		if always.DischargedByConstraint() {
			t.Fatal("an always-readable field reported a discharge that is meaningless for it")
		}

		never, present := plan.Field(p5social.Comments.PostID)
		if !present || never.Access() != AccessNever {
			t.Fatalf("a denied field classified as %v present=%v", never.Access(), present)
		}
		if _, conditional := never.Condition(); conditional {
			t.Fatal("a never-readable field reported a condition that could make it readable")
		}

		for _, expected := range []struct {
			name      string
			field     golem.Field[p5social.Comment]
			predicate golem.Predicate[p5social.Comment]
		}{
			{"OwnedByCondition", p5social.Comments.Body, p5social.Comments.AuthorID.Eq(maskAuthor())},
			{"RelationCondition", p5social.Comments.ParentID, p5social.Comments.Post.Is(p5social.Posts.Title.EndsWith(maskOpenSuffix()))},
		} {
			classification, ok := plan.Field(expected.field)
			if !ok || classification.Access() != AccessConditional {
				t.Fatalf("%s: a conditionally granted field classified as %v present=%v", expected.name, classification.Access(), ok)
			}
			condition, conditional := classification.Condition()
			if !conditional {
				t.Fatalf("%s: a conditional field carries no condition", expected.name)
			}
			equivalent, equivalentErr := Equivalent(condition, expected.predicate)
			if equivalentErr != nil {
				t.Fatalf("%s: Equivalent: %v", expected.name, equivalentErr)
			}
			if !equivalent {
				t.Fatalf("%s: the field condition is not the predicate the policy declared for it", expected.name)
			}
			if classification.DischargedByConstraint() {
				t.Fatalf("%s: a conditional field was reported discharged by a row constraint that does not prove it", expected.name)
			}
		}
	})

	t.Run("TheRuntimeReadPathMasksExactlyTheFieldsTheKitCallsConditional", func(t *testing.T) {
		runtime, runtimeErr := maskRuntimePlan(t,
			maskScalarSelection(t, alwaysField),
			maskScalarSelection(t, ownedField),
			maskScalarSelection(t, reachedField),
		)
		if runtimeErr != nil {
			t.Fatalf("the runtime read planner refused the readable projection: %v", runtimeErr)
		}
		constraint, constraintErr := comments.RowConstraint(golem.FrozenActionRead)
		if constraintErr != nil {
			t.Fatalf("RowConstraint: %v", constraintErr)
		}
		if string(constraint.CanonicalBytes()) != canonical(t, runtime.Where()) {
			t.Fatal("the runtime statement reach differs from the constraint the kit classified against, so the two answers are not comparable")
		}

		for _, field := range []golem.FieldID{alwaysField, ownedField, reachedField} {
			classification, present := findClassification(t, plan, field)
			if !present {
				t.Fatal("the kit did not classify a field the runtime planned")
			}
			value := maskRuntimeField(t, runtime, field)
			mask, masked := value.Mask()

			if masked != (classification.Access() == AccessConditional) {
				t.Fatalf("the runtime masks the field=%v while the kit classified it %v", masked, classification.Access())
			}
			if masked != value.Conditional() {
				t.Fatal("the runtime read plan disagrees with itself about whether a field is conditional")
			}
			if !masked {
				if !value.DischargedByConstraint() {
					t.Fatal("the runtime withheld an unmasked field the kit called always readable")
				}
				continue
			}
			condition, conditional := classification.Condition()
			if !conditional {
				t.Fatal("the runtime masked a field the kit gave no condition for")
			}
			if string(condition.CanonicalBytes()) != canonical(t, mask) {
				t.Fatal("the kit's field condition is not the mask the runtime will apply to that field")
			}
			if value.DischargedByConstraint() != classification.DischargedByConstraint() {
				t.Fatalf("the runtime discharges the field=%v while the kit reports %v", value.DischargedByConstraint(), classification.DischargedByConstraint())
			}
			if value.DischargedByConstraint() {
				t.Fatal("the runtime discharged a mask that the bare row constraint does not prove")
			}
		}
	})

	t.Run("TheRuntimeReadPathRefusesExactlyTheFieldTheKitCallsNever", func(t *testing.T) {
		_, err := maskRuntimePlan(t, maskScalarSelection(t, neverField))
		if err == nil {
			t.Fatal("the runtime read planner returned a field the kit classified as never readable")
		}
		readable, readableErr := maskRuntimePlan(t, maskScalarSelection(t, alwaysField))
		if readableErr != nil {
			t.Fatalf("the runtime read planner refused a field the kit classified as always readable: %v", readableErr)
		}
		if len(readable.Fields()) == 0 {
			t.Fatal("the runtime read planner returned an empty projection")
		}
	})

	t.Run("ARelationFieldIsClassifiedAndMaskedTheSameWay", func(t *testing.T) {
		relationPlan, relationErr := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Comments.Post, p5social.Comments.Replies)
		if relationErr != nil {
			t.Fatalf("ClassifyReadFields: %v", relationErr)
		}
		open, ok := relationPlan.Field(p5social.Comments.Post)
		if !ok || open.Access() != AccessAlways {
			t.Fatalf("an unconditionally granted relation classified as %v present=%v", open.Access(), ok)
		}
		denied, ok := relationPlan.Field(p5social.Comments.Replies)
		if !ok || denied.Access() != AccessNever {
			t.Fatalf("a denied relation classified as %v present=%v", denied.Access(), ok)
		}

		runtime, runtimeErr := maskRuntimePlan(t, maskRelationSelection(t, identity(p5social.Comments.Post), fieldIdentityOf[p5social.Post](t, p5social.Posts.ID)))
		if runtimeErr != nil {
			t.Fatalf("the runtime read planner refused an always-readable relation: %v", runtimeErr)
		}
		relations := runtime.Relations()
		if len(relations) != 1 || relations[0].FieldID() != ir.FieldID(identity(p5social.Comments.Post)) {
			t.Fatal("the runtime read plan does not carry the projected relation")
		}
		if _, masked := relations[0].Mask(); masked {
			t.Fatal("the runtime masked a relation the kit classified as always readable")
		}
		if _, err := maskRuntimePlan(t, maskRelationSelection(t, identity(p5social.Comments.Replies), identity(p5social.Comments.ID))); err == nil {
			t.Fatal("the runtime read planner returned a relation the kit classified as never readable")
		}
	})

	t.Run("ClassificationRefusesInputItCannotJudge", func(t *testing.T) {
		_, emptyErr := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead)
		expectCode(t, emptyErr, ErrorInvalidInput, "no fields")
		_, useErr := comments.ClassifyReadFields(UseKind(0), golem.FrozenActionRead, p5social.Comments.ID)
		expectCode(t, useErr, ErrorInvalidInput, "unknown use kind")
		_, actionErr := comments.ClassifyReadFields(UseProjection, golem.FrozenAction(0), p5social.Comments.ID)
		expectCode(t, actionErr, ErrorInvalidInput, "unknown action tag")
		_, zeroErr := ModelPolicy[p5social.Comment]{}.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Comments.ID)
		expectCode(t, zeroErr, ErrorInvalidInput, "zero model policy")
		_, handleErr := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead, nil)
		expectCode(t, handleErr, ErrorInvalidInput, "nil field handle")
	})
}

func fieldIdentityOf[M any](t *testing.T, field golem.Field[M]) golem.FieldID {
	t.Helper()
	value, ok := golem.FieldIdentity(field)
	if !ok {
		t.Fatal("a generated field handle carries no identity")
	}
	return value
}

func findClassification[M any](t *testing.T, plan ReadPlan[M], field golem.FieldID) (FieldClassification[M], bool) {
	t.Helper()
	for _, classification := range plan.Fields() {
		if classification.FieldID() == field {
			return classification, true
		}
	}
	return FieldClassification[M]{}, false
}
