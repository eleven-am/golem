package plan

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
)

func TestPolicyExpanderRecursivelyScopesEveryRelationTarget(t *testing.T) {
	modelA, modelB, modelC := expansionModel(1), expansionModel(2), expansionModel(3)
	trueB, _ := policyir.NewConstant(modelB, true)
	trueC, _ := policyir.NewConstant(modelC, true)
	bToC := expansionRelation(t, modelB, expansionField(2), expansionRelationID(2), modelC, policyir.OperatorRelationSome, &trueC)

	policies := policyMap{
		modelA: expansionPolicy(t, modelA, nil),
		modelB: expansionPolicy(t, modelB, &bToC),
		modelC: expansionPolicy(t, modelC, nil),
	}
	aToB := expansionRelation(t, modelA, expansionField(1), expansionRelationID(1), modelB, policyir.OperatorRelationSome, &trueB)

	rewritten, err := newPolicyExpander(policies, 8).condition(aToB, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, child, ok := rewritten.Relation()
	if !ok || child == nil || !conditionHasRelation(*child) {
		t.Fatalf("target policy relation was not injected recursively: %#v", rewritten)
	}
	// The injected B→C relation must itself carry C's read policy. With an
	// allow-all C policy normalization keeps a valid relation predicate rather
	// than dropping the target scope entirely.
	var nested policyir.Condition
	if child.Kind() == policyir.ConditionRelation {
		nested = *child
	} else {
		_, children, logical := child.Logical()
		if !logical {
			t.Fatalf("unexpected authorized child kind %d", child.Kind())
		}
		for _, candidate := range children {
			if candidate.Kind() == policyir.ConditionRelation {
				nested = candidate
				break
			}
		}
	}
	if nested.Kind() != policyir.ConditionRelation {
		t.Fatal("recursive B→C target constraint is absent")
	}
}

func TestPolicyExpanderRejectsRelationBearingCycles(t *testing.T) {
	modelA, modelB := expansionModel(10), expansionModel(11)
	trueA, _ := policyir.NewConstant(modelA, true)
	trueB, _ := policyir.NewConstant(modelB, true)
	aToB := expansionRelation(t, modelA, expansionField(10), expansionRelationID(10), modelB, policyir.OperatorRelationSome, &trueB)
	bToA := expansionRelation(t, modelB, expansionField(11), expansionRelationID(11), modelA, policyir.OperatorRelationSome, &trueA)
	policies := policyMap{
		modelA: expansionPolicy(t, modelA, &aToB),
		modelB: expansionPolicy(t, modelB, &bToA),
	}

	_, err := newPolicyExpander(policies, 8).model(modelA, 0)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy {
		t.Fatalf("cycle error=%v", err)
	}
}

func TestPolicyExpanderAllowsSelfHopWithRelationFreeTargetPolicy(t *testing.T) {
	model := expansionModel(20)
	truth, _ := policyir.NewConstant(model, true)
	self := expansionRelation(t, model, expansionField(20), expansionRelationID(20), model, policyir.OperatorRelationSome, &truth)
	policies := policyMap{model: expansionPolicy(t, model, nil)}

	rewritten, err := newPolicyExpander(policies, 8).condition(self, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.Kind() != policyir.ConditionRelation {
		t.Fatalf("self relation kind=%d", rewritten.Kind())
	}
}

func TestCallerRootPolicyCannotObserveTargetPolicyInvisibleRows(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	title := golem.GeneratedTextField[planPost, string](fixture.PostTitle)

	userRules := golem.NewRules[planUser]()
	userRules.CanRead(posts.Some(title.Eq("visible")))
	userPolicy := expansionBoundPolicy(t, fixture, fixture.User, userRules)
	policies := policyMap{
		policyir.ModelID(fixture.User): userPolicy,
		policyir.ModelID(fixture.Post): denyPolicy(t, fixture.Post),
	}
	frozen, err := golem.FreezeCount(descriptor)
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
	_, _, target, _, child, ok := planned.Where().Relation()
	if !ok || target != policyir.ModelID(fixture.Post) || child == nil {
		t.Fatalf("expanded root row policy=%#v", planned.Where())
	}
	truth, constant := child.Constant()
	if !constant || truth {
		t.Fatalf("invisible target row policy was not conjoined: child=%#v", child)
	}
}

func TestCallerRejectsMutuallyRecursiveRelationRowPoliciesBeforeSQL(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[planUser, string](fixture.UserName)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	title := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	author := golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User)

	userRules := golem.NewRules[planUser]()
	userRules.CanRead(posts.Some(title.Eq("visible")))
	postRules := golem.NewRules[planPost]()
	postRules.CanRead(author.Is(name.Eq("owner")))
	policies := policyMap{
		policyir.ModelID(fixture.User): expansionBoundPolicy(t, fixture, fixture.User, userRules),
		policyir.ModelID(fixture.Post): expansionBoundPolicy(t, fixture, fixture.Post, postRules),
	}
	frozen, err := golem.FreezeCount(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Caller(request, fixture.Registry, policies, DefaultLimits())
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy {
		t.Fatalf("mutual policy cycle error=%v", err)
	}
}

type expansionFreezer interface {
	Freeze(golem.ModelID) (golem.FrozenPolicy, error)
}

func expansionBoundPolicy(t *testing.T, fixture schematest.Fixture, model golem.ModelID, rules expansionFreezer) policyir.Policy {
	t.Helper()
	frozen, err := rules.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := policybind.Policy(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	bound, err = normalize.Policy(bound)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func expansionPolicy(t *testing.T, model policyir.ModelID, condition *policyir.Condition) policyir.Policy {
	t.Helper()
	rule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, model, condition, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(model, []policyir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func expansionRelation(t *testing.T, model policyir.ModelID, field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID, operator policyir.OperatorID, child *policyir.Condition) policyir.Condition {
	t.Helper()
	value, err := policyir.NewRelation(model, field, relation, target, policyir.RelationToMany, operator, child, nil)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func expansionModel(value byte) (result policyir.ModelID)         { result[15] = value; return }
func expansionField(value byte) (result policyir.FieldID)         { result[15] = value; return }
func expansionRelationID(value byte) (result policyir.RelationID) { result[15] = value; return }
