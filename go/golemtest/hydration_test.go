package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func maskRuntimeHydration(t *testing.T, plan readplan.Plan, field golem.FieldID) readplan.Relation {
	t.Helper()
	for _, relation := range plan.Hydrations() {
		if relation.FieldID() == ir.FieldID(field) {
			return relation
		}
	}
	t.Fatal("the runtime read plan hydrates no relation for the requested field")
	return readplan.Relation{}
}

func assertPlanHydratesTree(t *testing.T, label string, tree DependencyTree, plan readplan.Plan) {
	t.Helper()
	if tree.ModelID() != golem.ModelID(plan.ModelID()) {
		t.Fatalf("%s: the dependency tree is rooted at a different model than the record the runtime fetches", label)
	}
	scalars := make(map[ir.FieldID]bool)
	for _, field := range plan.Fields() {
		scalars[field.FieldID()] = true
	}
	relations := 0
	for _, entry := range tree.Entries() {
		switch entry.Kind() {
		case DependencyScalar:
			if _, isRelation := entry.TargetModelID(); isRelation {
				t.Fatalf("%s: a scalar dependency claims a relation target", label)
			}
			if !scalars[ir.FieldID(entry.FieldID())] {
				t.Fatalf("%s: the runtime does not fetch a scalar the kit says the condition depends on", label)
			}
		case DependencyRelation:
			target, isRelation := entry.TargetModelID()
			if !isRelation || target == (golem.ModelID{}) {
				t.Fatalf("%s: a relation dependency carries no target model", label)
			}
			relations++
			hydration := maskRuntimeHydration(t, plan, entry.FieldID())
			if hydration.TargetModelID() != ir.ModelID(target) {
				t.Fatalf("%s: the runtime hydrates a different target than the kit reports", label)
			}
			assertPlanHydratesTree(t, label+"/"+"child", entry.Children(), hydration.Child())
		default:
			t.Fatalf("%s: the kit reported a dependency kind it does not define", label)
		}
	}
	if len(plan.Hydrations()) != relations {
		t.Fatalf("%s: the runtime hydrates %d relations while the kit reports %d", label, len(plan.Hydrations()), relations)
	}
}

func TestPolicyTestKitRelationDependencyTreeMatchesRuntimeHydration(t *testing.T) {
	comments := maskComments(t)
	commentModel := maskCommentModel(t)
	postModel := p5social.GolemGeneratedPostDescriptor.Metadata().ModelID()
	postRelation := fieldIdentityOf[p5social.Comment](t, p5social.Comments.Post)
	postTitle := fieldIdentityOf[p5social.Post](t, p5social.Posts.Title)
	authorField := fieldIdentityOf[p5social.Comment](t, p5social.Comments.AuthorID)

	t.Run("ARelationConditionKeepsItsRelationHopAndItsTargetSubtree", func(t *testing.T) {
		plan, err := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Comments.ParentID)
		if err != nil {
			t.Fatalf("ClassifyReadFields: %v", err)
		}
		classification, present := plan.Field(p5social.Comments.ParentID)
		if !present || classification.Access() != AccessConditional {
			t.Fatalf("the fixture field is not conditionally readable, so this gate proves nothing: access=%v present=%v", classification.Access(), present)
		}

		tree := classification.Dependencies()
		if tree.ModelID() != commentModel {
			t.Fatal("the dependency tree is not rooted at the classified model")
		}
		entries := tree.Entries()
		if len(entries) != 1 {
			t.Fatalf("the relation condition collected %d dependencies, want exactly the relation it names", len(entries))
		}
		if entries[0].FieldID() != postRelation || entries[0].Kind() != DependencyRelation {
			t.Fatal("the relation dependency was flattened away or renamed")
		}
		target, isRelation := entries[0].TargetModelID()
		if !isRelation || target != postModel {
			t.Fatal("the relation dependency does not retain the model it must hydrate")
		}
		children := entries[0].Children()
		if children.ModelID() != postModel || len(children.Entries()) != 1 {
			t.Fatalf("the relation subtree is rooted at %x with %d entries, want the one field the nested condition reads", children.ModelID(), len(children.Entries()))
		}
		if children.Entries()[0].FieldID() != postTitle || children.Entries()[0].Kind() != DependencyScalar {
			t.Fatal("the relation subtree does not name the target field the nested condition reads")
		}
		if required := classification.RequiredFields(); len(required) != 1 || required[0] != postRelation {
			t.Fatalf("the classification requires %d fields on its own model, want only the relation hop", len(required))
		}

		runtime, runtimeErr := maskRuntimePlan(t, maskScalarSelection(t, fieldIdentityOf[p5social.Comment](t, p5social.Comments.ParentID)))
		if runtimeErr != nil {
			t.Fatalf("the runtime read planner refused the conditional projection: %v", runtimeErr)
		}
		if len(runtime.Hydrations()) != 1 {
			t.Fatalf("the runtime read path privately fetches %d relations to evaluate this mask, want exactly 1", len(runtime.Hydrations()))
		}
		hydration := maskRuntimeHydration(t, runtime, postRelation)
		if hydration.Public() {
			t.Fatal("the runtime returned the mask hydration as a projected relation")
		}
		child := hydration.Child()
		fetched := false
		for _, field := range child.Fields() {
			if field.FieldID() == ir.FieldID(postTitle) {
				fetched = true
			}
		}
		if !fetched {
			t.Fatal("the runtime does not fetch the target field the kit says the mask depends on")
		}
		assertPlanHydratesTree(t, "ParentID", tree, runtime)
	})

	t.Run("AScalarConditionRequiresTheScalarTheRuntimeInjectsPrivately", func(t *testing.T) {
		plan, err := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Comments.Body)
		if err != nil {
			t.Fatalf("ClassifyReadFields: %v", err)
		}
		classification, present := plan.Field(p5social.Comments.Body)
		if !present || classification.Access() != AccessConditional {
			t.Fatalf("the fixture field is not conditionally readable: access=%v present=%v", classification.Access(), present)
		}
		entries := classification.Dependencies().Entries()
		if len(entries) != 1 || entries[0].FieldID() != authorField || entries[0].Kind() != DependencyScalar {
			t.Fatalf("the scalar condition collected %d dependencies, want exactly the scalar it reads", len(entries))
		}
		if _, isRelation := entries[0].TargetModelID(); isRelation {
			t.Fatal("a scalar dependency claims a relation target")
		}
		if required := classification.RequiredFields(); len(required) != 1 || required[0] != authorField {
			t.Fatalf("the classification requires %d fields, want exactly the scalar the condition reads", len(required))
		}

		runtime, runtimeErr := maskRuntimePlan(t, maskScalarSelection(t, fieldIdentityOf[p5social.Comment](t, p5social.Comments.Body)))
		if runtimeErr != nil {
			t.Fatalf("the runtime read planner refused the conditional projection: %v", runtimeErr)
		}
		if len(runtime.Hydrations()) != 0 {
			t.Fatal("the runtime hydrated a relation for a mask that reads only its own row")
		}
		injected := false
		for _, field := range runtime.Fields() {
			if field.FieldID() != ir.FieldID(authorField) {
				continue
			}
			injected = true
			if field.Public() {
				t.Fatal("the runtime returned a policy-only dependency as a projected field")
			}
		}
		if !injected {
			t.Fatal("the runtime does not fetch the scalar the kit says the mask depends on")
		}
		assertPlanHydratesTree(t, "Body", classification.Dependencies(), runtime)
	})

	t.Run("AnUnconditionalFieldCarriesAnEmptyTreeAndTheRuntimeHydratesNothing", func(t *testing.T) {
		plan, err := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Comments.ID, p5social.Comments.PostID)
		if err != nil {
			t.Fatalf("ClassifyReadFields: %v", err)
		}
		for _, field := range plan.Fields() {
			if len(field.Dependencies().Entries()) != 0 || len(field.RequiredFields()) != 0 {
				t.Fatal("a field that needs no proof carries a hydration plan")
			}
			if field.Dependencies().ModelID() != commentModel {
				t.Fatal("an empty dependency tree lost the model it belongs to")
			}
		}
		runtime, runtimeErr := maskRuntimePlan(t, maskScalarSelection(t, fieldIdentityOf[p5social.Comment](t, p5social.Comments.ID)))
		if runtimeErr != nil {
			t.Fatalf("the runtime read planner refused an unconditional projection: %v", runtimeErr)
		}
		if len(runtime.Hydrations()) != 0 {
			t.Fatal("the runtime hydrated a relation for a projection that carries no mask")
		}
	})

	t.Run("EveryTreeAccessorReturnsACopy", func(t *testing.T) {
		plan, err := comments.ClassifyReadFields(UseProjection, golem.FrozenActionRead, p5social.Comments.ParentID)
		if err != nil {
			t.Fatalf("ClassifyReadFields: %v", err)
		}
		classification, present := plan.Field(p5social.Comments.ParentID)
		if !present {
			t.Fatal("the plan lost the field it classified")
		}
		mutated := classification.Dependencies()
		entries := mutated.Entries()
		if len(entries) != 1 || len(entries[0].Children().Entries()) != 1 {
			t.Fatal("the fixture no longer produces the nested dependency this copy check needs")
		}
		entries[0] = DependencyEntry{}
		grandchildren := mutated.Entries()[0].Children().Entries()
		grandchildren[0] = DependencyEntry{}
		required := classification.RequiredFields()
		required[0] = golem.FieldID{}
		fields := plan.Fields()
		fields[0] = FieldClassification[p5social.Comment]{}

		again := classification.Dependencies()
		if len(again.Entries()) != 1 || again.Entries()[0].FieldID() != postRelation {
			t.Fatal("mutating a returned dependency slice changed the classification")
		}
		if again.Entries()[0].Children().Entries()[0].FieldID() != postTitle {
			t.Fatal("mutating a returned subtree changed the classification")
		}
		if classification.RequiredFields()[0] != postRelation {
			t.Fatal("mutating a returned required-field slice changed the classification")
		}
		if repeated, ok := plan.Field(p5social.Comments.ParentID); !ok || repeated.FieldID() != fieldIdentityOf[p5social.Comment](t, p5social.Comments.ParentID) {
			t.Fatal("mutating a returned classification slice changed the plan")
		}
	})
}
