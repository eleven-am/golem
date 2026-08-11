package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

type viewActor struct{}

func viewVisibleLabel() string { return "visible" }

func viewEnumBindings(t *testing.T) golem.ApplicationBindings[viewActor] {
	t.Helper()
	descriptors := viewEnumDescriptors(t)
	generation := p6metrics.GolemGeneratedSchemaBundle().GenerationDigest()
	metric := p6metrics.GolemGeneratedMetricDescriptor.Metadata().ModelID()
	policies := make([]golem.PolicyBinding[viewActor], 0)
	for _, metadata := range descriptors.Models() {
		id := metadata.ModelID()
		if id == metric {
			policies = append(policies, golem.GeneratedPolicyBinding[viewActor, p6metrics.Metric](id, func(viewActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[p6metrics.Metric]()
				rules.CanRead(p6metrics.Metrics.State.Eq(p6metrics.StatusAlpha))
				rules.CanRead(p6metrics.Metrics.Label.Eq(viewVisibleLabel()))
				return rules.Freeze(id)
			}))
			continue
		}
		policies = append(policies, golem.GeneratedPolicyBinding[viewActor, p6metrics.Category](id, func(viewActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[p6metrics.Category]()
			rules.CanRead(golem.All[p6metrics.Category]())
			return rules.Freeze(id)
		}))
	}
	bindings, err := golem.GeneratedApplicationBindings(generation, golem.GeneratedStampedPackageBindings(generation, policies, nil))
	if err != nil {
		t.Fatalf("build enum bindings: %v", err)
	}
	return bindings
}

func viewEnumDescriptors(t *testing.T) golem.ApplicationDescriptors {
	t.Helper()
	descriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatalf("build enum descriptors: %v", err)
	}
	return descriptors
}

func viewEnumConstraint(t *testing.T) Constraint[p6metrics.Metric] {
	t.Helper()
	kit, err := New(viewEnumBindings(t), viewEnumDescriptors(t), p6metrics.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	policies, err := kit.ForActor(viewActor{})
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}
	metrics, err := Model(policies, p6metrics.GolemGeneratedMetricDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	constraint, err := metrics.RowConstraint(golem.FrozenActionRead)
	if err != nil {
		t.Fatalf("RowConstraint: %v", err)
	}
	return constraint
}

func viewPostConstraint(t *testing.T, action golem.FrozenAction) Constraint[p5social.Post] {
	t.Helper()
	kit := kernelKit(t)
	policies, err := kit.ForActor(kernelActor{Author: kernelAuthor()})
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}
	posts, err := Model(policies, p5social.GolemGeneratedPostDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	constraint, err := posts.RowConstraint(action)
	if err != nil {
		t.Fatalf("RowConstraint: %v", err)
	}
	return constraint
}

func viewSingleString(t *testing.T, condition golem.FrozenConditionView) string {
	t.Helper()
	operand := condition.Operand()
	if operand == nil || operand.Kind() != golem.FrozenOperandOne {
		t.Fatalf("condition carries operand arity %v, want a single operand", operand)
	}
	value, present := operand.One()
	if !present || value == nil {
		t.Fatal("the single-operand accessor disagrees with its tag")
	}
	if value.Kind() != golem.FrozenValueString {
		t.Fatalf("operand carries value kind %d, want a string", value.Kind())
	}
	text, ok := value.String()
	if !ok {
		t.Fatal("the string accessor disagrees with its tag")
	}
	return text
}

func viewFrozenCanonical[M any](t *testing.T, predicate golem.Predicate[M], descriptor golem.ModelDescriptor[M]) string {
	t.Helper()
	frozen, err := predicate.Freeze(descriptor)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	return string(frozen.View().CanonicalBytes())
}

func TestPolicyTestKitConstraintViewDescribesTheResolvedPredicate(t *testing.T) {
	t.Run("TheViewIsTheResolvedExpressionRatherThanTheAuthoredRule", func(t *testing.T) {
		constraint := viewPostConstraint(t, golem.FrozenActionRead)
		view := constraint.View()

		if view.RootModelID() != kernelPostModel(t) {
			t.Fatal("the view names a different model than the policy it was resolved from")
		}
		root := view.Root()
		if root.Kind() != golem.FrozenConditionLogical || root.Operator() != golem.FrozenOperatorNot {
			t.Fatalf("the view root is kind %d operator %d, want the negation the deny rule resolves to", root.Kind(), root.Operator())
		}
		children := root.Children()
		if len(children) != 1 {
			t.Fatalf("the negation carries %d children, want 1", len(children))
		}
		denied := children[0]
		if denied.Kind() != golem.FrozenConditionScalar || denied.Operator() != golem.FrozenOperatorEndsWith {
			t.Fatalf("the negated leaf is kind %d operator %d, want the scalar suffix comparison the rule authored", denied.Kind(), denied.Operator())
		}
		if len(denied.Children()) != 0 {
			t.Fatalf("the scalar leaf carries %d children", len(denied.Children()))
		}
		if suffix := viewSingleString(t, denied); suffix != kernelSecretSuffix() {
			t.Fatalf("the negated leaf compares against %q, want %q", suffix, kernelSecretSuffix())
		}

		authored := golem.Not(p5social.Posts.Title.EndsWith(kernelSecretSuffix()))
		field, present := denied.FieldID()
		if !present {
			t.Fatal("the negated leaf carries no field identity")
		}
		expected, expectedPresent := mustFreeze(t, authored, p5social.GolemGeneratedPostDescriptor).View().Root().Children()[0].FieldID()
		if !expectedPresent || field != expected {
			t.Fatal("the negated leaf names a different field than the rule the resolver started from")
		}
		if string(view.CanonicalBytes()) != viewFrozenCanonical(t, authored, p5social.GolemGeneratedPostDescriptor) {
			t.Fatal("the view encodes a different predicate than the one the resolved constraint describes")
		}
	})

	t.Run("EachActionIsViewedAsItsOwnConstraint", func(t *testing.T) {
		distinct := make(map[string]struct{})
		for _, action := range kernelActions() {
			view := viewPostConstraint(t, action.public).View()
			if view.RootModelID() != kernelPostModel(t) {
				t.Fatalf("%s: the view names a different model than the policy it was resolved from", action.name)
			}
			canonical := string(view.CanonicalBytes())
			if canonical == "" {
				t.Fatalf("%s: the view carries no canonical evidence", action.name)
			}
			distinct[canonical] = struct{}{}
		}
		if len(distinct) < 3 {
			t.Fatalf("the four actions produced %d distinct views, so an action mix-up would go unnoticed", len(distinct))
		}
	})

	t.Run("EnumOperandsCarryTheLabelTheProductionBinderAcceptsBack", func(t *testing.T) {
		constraint := viewEnumConstraint(t)
		view := constraint.View()

		root := view.Root()
		if root.Kind() != golem.FrozenConditionLogical || root.Operator() != golem.FrozenOperatorOr {
			t.Fatalf("the view root is kind %d operator %d, want the disjunction two grants resolve to", root.Kind(), root.Operator())
		}
		children := root.Children()
		if len(children) != 2 {
			t.Fatalf("the disjunction carries %d children, want one per grant", len(children))
		}
		labels := make([]string, 0, len(children))
		for _, child := range children {
			if child.Kind() != golem.FrozenConditionScalar || child.Operator() != golem.FrozenOperatorEq {
				t.Fatalf("a disjunct is kind %d operator %d, want the scalar equality its rule authored", child.Kind(), child.Operator())
			}
			labels = append(labels, viewSingleString(t, child))
		}
		if !viewContains(labels, string(p6metrics.StatusAlpha)) {
			t.Fatalf("the disjuncts carry operands %q, want the authored enum label %q", labels, string(p6metrics.StatusAlpha))
		}
		if viewContains(labels, "StatusAlpha") {
			t.Fatal("the enum operand is labelled with the GraphQL member name, which the policy binder cannot resolve")
		}
		if !viewContains(labels, viewVisibleLabel()) {
			t.Fatalf("the disjuncts carry operands %q, want the second grant to survive", labels)
		}

		authored := p6metrics.Metrics.State.Eq(p6metrics.StatusAlpha).Or(p6metrics.Metrics.Label.Eq(viewVisibleLabel()))
		if string(view.CanonicalBytes()) != viewFrozenCanonical(t, authored, p6metrics.GolemGeneratedMetricDescriptor) {
			t.Fatal("the viewed enum predicate does not encode the same predicate a Go-authored enum member freezes to")
		}
		equivalent, err := Equivalent(constraint, authored)
		if err != nil {
			t.Fatalf("Equivalent: %v", err)
		}
		if !equivalent {
			t.Fatal("the resolved enum constraint is not equivalent to the rule it was resolved from")
		}
	})

	t.Run("AConstraintThatWasNotResolvedYieldsASafeZeroView", func(t *testing.T) {
		view := Constraint[p5social.Post]{}.View()
		if view == nil {
			t.Fatal("an unresolved constraint returned no view, which a caller cannot inspect")
		}
		if view.RootModelID() != (golem.ModelID{}) {
			t.Fatal("an unresolved constraint claims a model identity")
		}
		if len(view.CanonicalBytes()) != 0 {
			t.Fatal("an unresolved constraint claims canonical evidence")
		}
		if root := view.Root(); root == nil || root.Kind() != 0 || len(root.Children()) != 0 {
			t.Fatal("an unresolved constraint exposes a root condition")
		}
	})
}

func viewContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mustFreeze[M any](t *testing.T, predicate golem.Predicate[M], descriptor golem.ModelDescriptor[M]) golem.FrozenPredicate {
	t.Helper()
	frozen, err := predicate.Freeze(descriptor)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	return frozen
}
