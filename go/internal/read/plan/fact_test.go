package plan

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
)

func TestPolicyFactReachPreservesPhysicalTargets(t *testing.T) {
	root, target := expansionModel(1), expansionModel(2)
	truth, _ := policyir.NewConstant(target, true)
	physical := expansionRelation(t, root, expansionField(1), expansionRelationID(1), target, policyir.OperatorRelationSome, &truth)
	policy, id := factPolicy(t, root, physical)
	denied, _ := policyir.NewConstant(target, false)
	policies := policyMap{root: policy, target: expansionPolicy(t, target, &denied)}
	reach, err := ReadReach(policies, root, 8)
	if err != nil {
		t.Fatal(err)
	}
	fact, body, ok := reach.Fact()
	if !ok || fact != id {
		t.Fatal("physical fact lost its named boundary")
	}
	_, _, _, _, child, ok := body.Relation()
	if !ok || child == nil {
		t.Fatal("physical relation was lost")
	}
	if value, constant := child.Constant(); !constant || !value {
		t.Fatal("unreadable target policy was injected inside a physical fact")
	}
	ordinary, err := newPolicyExpander(policies, 8).condition(physical, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, child, _ = ordinary.Relation()
	if value, constant := child.Constant(); !constant || value {
		t.Fatal("ordinary relation stopped enforcing target readability")
	}
}

func TestRelatedAuthorityGateKeepsItsPhysicalProvenance(t *testing.T) {
	root, target := expansionModel(1), expansionModel(2)
	truth, _ := policyir.NewConstant(root, true)
	leaf := expansionModel(3)
	leafTruth, _ := policyir.NewConstant(leaf, true)
	nested := expansionRelation(t, target, expansionField(2), expansionRelationID(2), leaf, policyir.OperatorRelationSome, &leafTruth)
	related := expansionRelation(t, root, expansionField(1), expansionRelationID(1), target, policyir.OperatorRelationSome, &nested)
	scope, err := policyir.NewFactScope(related)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := policyir.NewFactDefinition(root, "unconditional_data", truth)
	rule, _ := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, root, nil, 0)
	policy, err := policyir.NewPolicyWithFacts(root, []policyir.Rule{rule}, []policyir.FactDefinition{definition}, scope)
	if err != nil {
		t.Fatal(err)
	}
	denied, _ := policyir.NewConstant(target, false)
	reach, err := ReadReach(policyMap{root: policy, target: expansionPolicy(t, target, &denied)}, root, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !reach.IsFactScope() {
		t.Fatal("related authority became an ordinary readable relation")
	}
	_, body, _ := reach.Fact()
	_, _, _, _, child, present := body.Relation()
	if !present || child == nil {
		t.Fatal("related scope lost its path")
	}
	actual, _ := policyir.ConditionFingerprint(*child)
	expected, _ := policyir.ConditionFingerprint(nested)
	if actual != expected {
		t.Fatal("hidden target read policy changed authority membership")
	}
	_, err = ReadReach(policyMap{root: policy}, root, 1)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatalf("authority bypassed relation depth budget: %v", err)
	}
}

func TestPolicyFactBreaksOnlyItsExplicitReadabilityCycle(t *testing.T) {
	root, target := expansionModel(1), expansionModel(2)
	truthRoot, _ := policyir.NewConstant(root, true)
	truthTarget, _ := policyir.NewConstant(target, true)
	physical := expansionRelation(t, root, expansionField(1), expansionRelationID(1), target, policyir.OperatorRelationSome, &truthTarget)
	back := expansionRelation(t, target, expansionField(2), expansionRelationID(2), root, policyir.OperatorRelationSome, &truthRoot)
	policy, _ := factPolicy(t, root, physical)
	policies := policyMap{root: policy, target: expansionPolicy(t, target, &back)}
	for _, model := range []policyir.ModelID{root, target} {
		if _, err := ReadReach(policies, model, 8); err != nil {
			t.Fatalf("fact introduced a target-read recursion for model %x: %v", model, err)
		}
	}
	policies[root] = expansionPolicy(t, root, &physical)
	if _, err := ReadReach(policies, root, 8); err == nil {
		t.Fatal("ordinary relation policy cycle was admitted")
	}
}

func TestPolicyFactPhysicalPathsStillObeyRelationDepth(t *testing.T) {
	root, target, leaf := expansionModel(1), expansionModel(2), expansionModel(3)
	truth, _ := policyir.NewConstant(leaf, true)
	nested := expansionRelation(t, target, expansionField(2), expansionRelationID(2), leaf, policyir.OperatorRelationSome, &truth)
	body := expansionRelation(t, root, expansionField(1), expansionRelationID(1), target, policyir.OperatorRelationSome, &nested)
	policy, _ := factPolicy(t, root, body)
	policies := policyMap{root: policy}
	if _, err := ReadReach(policies, root, 2); err != nil {
		t.Fatal(err)
	}
	_, err := ReadReach(policies, root, 1)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatalf("physical fact ignored relation depth: %v", err)
	}
}

func TestCallerReachRejectsFactsInFilterExpressions(t *testing.T) {
	model := expansionModel(1)
	truth, _ := policyir.NewConstant(model, true)
	definition, _ := policyir.NewFactDefinition(model, "hidden", truth)
	reference, _ := policyir.NewFactReference(model, definition.ID())
	fact, _ := policyir.NewFact(model, definition.ID(), truth, unrestrictedFactScope(t, model))
	for _, expression := range []policyir.Condition{reference, fact} {
		for _, nested := range []bool{false, true} {
			condition := expression
			if nested {
				condition, _ = policyir.NewLogical(model, policyir.LogicalNot, []policyir.Condition{condition})
				condition, _ = normalize.Condition(condition)
			}
			_, _, err := CallerReach(policyMap{model: expansionPolicy(t, model, nil)}, model, &condition, 8)
			if err == nil {
				t.Fatal("caller used a policy fact as a filter")
			}
		}
	}
}

func TestFactMasksUsePhysicalDependenciesAndKeepOrdinaryMasksReadable(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[planUser, string](fixture.UserName)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	title := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	physicalPredicate := posts.Some(title.Eq("hidden"))
	frozen, err := physicalPredicate.Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := policybind.Predicate(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	root := policyir.ModelID(fixture.User)
	definition, _ := policyir.NewFactDefinition(root, "has_hidden_post", physical)
	reference, _ := policyir.NewFactReference(root, definition.ID())
	rules := golem.NewRules[planUser]()
	rules.CanRead(golem.All[planUser]())
	rules.CannotReadFields(golem.All[planUser](), name, posts)
	rules.CanReadFields(physicalPredicate, posts)
	base := expansionBoundPolicy(t, fixture, fixture.User, rules)
	grant, _ := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectGrant, root, &reference, policyir.FieldID(fixture.UserName), nil, uint32(base.RuleCount()))
	policy, err := policyir.NewPolicyWithFacts(root, append(base.Rules(), grant), []policyir.FactDefinition{definition}, unrestrictedFactScope(t, root))
	if err != nil {
		t.Fatal(err)
	}
	targetRules := golem.NewRules[planPost]()
	targetRules.CanRead(title.Eq("public"))
	policies := policyMap{root: policy, policyir.ModelID(fixture.Post): expansionBoundPolicy(t, fixture, fixture.Post, targetRules)}
	value, _ := policyir.StringValue("hidden")
	cell, _ := evaluate.ValueField(policyir.FieldID(fixture.PostTitle), value)
	post, _ := evaluate.NewRecord(policyir.ModelID(fixture.Post), cell)
	relation, _ := evaluate.ToManyField(policyir.FieldID(fixture.UserPosts), policyir.ModelID(fixture.Post), post)
	record, _ := evaluate.NewRecord(root, relation)
	for _, count := range []bool{false, true} {
		selection := golem.Select[planUser](name, posts.Args(golem.Select[planPost](title)))
		if count {
			selection = golem.Select[planUser](name, posts.Count())
		}
		frozen, err := golem.FreezeFindMany(descriptor, selection)
		if err != nil {
			t.Fatal(err)
		}
		request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		planned, err := Caller(request, fixture.Registry, policies, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		hydrations := planned.Hydrations()
		if len(hydrations) != 1 || hydrations[0].Public() {
			t.Fatal("fact dependency did not stay in one private relation view")
		}
		child := hydrations[0].Child()
		if value, constant := child.Where().Constant(); !constant || !value {
			t.Fatal("fact mask hydration was restricted by hidden target readability")
		}
		for _, field := range child.Fields() {
			if field.Public() {
				t.Fatal("physical fact dependency became a public column")
			}
		}
		for _, field := range planned.Fields() {
			if field.FieldID() != policyir.FieldID(fixture.UserName) {
				continue
			}
			mask, ok := field.Mask()
			visible, err := evaluate.Condition(mask, record, policyir.PortableProviders())
			if !ok || err != nil || !visible {
				t.Fatalf("physical fact mask=%v err=%v", visible, err)
			}
		}
		var ordinary policyir.Condition
		if count {
			ordinary, _ = planned.RelationCounts()[0].Mask()
		} else {
			ordinary, _ = planned.Relations()[0].Mask()
		}
		visible, err := evaluate.Condition(ordinary, record, policyir.PortableProviders())
		if err != nil || visible {
			t.Fatalf("ordinary mask observed a hidden physical target: visible=%v err=%v", visible, err)
		}
	}
}

func factPolicy(t *testing.T, model policyir.ModelID, body policyir.Condition) (policyir.Policy, policyir.FactID) {
	t.Helper()
	definition, err := policyir.NewFactDefinition(model, "physical", body)
	if err != nil {
		t.Fatal(err)
	}
	reference, _ := policyir.NewFactReference(model, definition.ID())
	rule, _ := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, model, &reference, 0)
	policy, err := policyir.NewPolicyWithFacts(model, []policyir.Rule{rule}, []policyir.FactDefinition{definition}, unrestrictedFactScope(t, model))
	if err != nil {
		t.Fatal(err)
	}
	return policy, definition.ID()
}
