package plan

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
)

type reachComment struct{}

type reachHandles struct {
	fixture   schematest.Fixture
	users     golem.ModelDescriptor[planUser]
	posts     golem.ModelDescriptor[planPost]
	userID    golem.EqualField[planUser, golem.UUID]
	userName  golem.TextField[planUser, string]
	title     golem.TextField[planPost, string]
	author    golem.ToOne[planPost, planUser]
	userPosts golem.ToMany[planUser, planPost]
}

func newReachHandles(t *testing.T) reachHandles {
	t.Helper()
	fixture := schematest.New(t)
	return reachHandles{
		fixture:   fixture,
		users:     golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil)),
		posts:     golem.GeneratedModelDescriptor[planPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil)),
		userID:    golem.GeneratedEqualField[planUser, golem.UUID](fixture.UserID),
		userName:  golem.GeneratedTextField[planUser, string](fixture.UserName),
		title:     golem.GeneratedTextField[planPost, string](fixture.PostTitle),
		author:    golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User),
		userPosts: golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post),
	}
}

func (handles reachHandles) policies(t *testing.T, userRules *golem.Rules[planUser], postRules *golem.Rules[planPost]) policyMap {
	t.Helper()
	return policyMap{
		policyir.ModelID(handles.fixture.User): bindPlanPolicy(t, handles.fixture, handles.fixture.User, userRules),
		policyir.ModelID(handles.fixture.Post): bindPlanPolicy(t, handles.fixture, handles.fixture.Post, postRules),
	}
}

func (handles reachHandles) planPosts(t *testing.T, policies policyMap, options ...golem.ReadOption[planPost]) (Plan, error) {
	t.Helper()
	frozen, err := golem.FreezeFindMany(handles.posts, options...)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, handles.fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	return Caller(request, handles.fixture.Registry, policies, DefaultLimits())
}

func (handles reachHandles) planUsers(t *testing.T, policies policyMap, options ...golem.ReadOption[planUser]) (Plan, error) {
	t.Helper()
	frozen, err := golem.FreezeFindMany(handles.users, options...)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, handles.fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	return Caller(request, handles.fixture.Registry, policies, DefaultLimits())
}

func TestRelationPolicyNeedNotRestateTargetReadPolicy(t *testing.T) {
	handles := newReachHandles(t)
	userRules := golem.NewRules[planUser]()
	userRules.CanRead(handles.userName.Contains("a"))
	postRules := golem.NewRules[planPost]()
	postRules.CanRead(handles.author.Is(handles.userName.Contains("b")))
	policies := handles.policies(t, userRules, postRules)

	planned, err := handles.planPosts(t, policies, golem.Where(handles.title.Eq("x")))
	if err != nil {
		t.Fatalf("non-restating relation policy refused: %v", err)
	}
	unfiltered, err := handles.planPosts(t, policies)
	if err != nil {
		t.Fatal(err)
	}
	row, err := ReadReach(policies, policyir.ModelID(handles.fixture.Post), DefaultLimits().MaxRelationDepth)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCondition(t, unfiltered.Where(), row) {
		t.Fatal("the selecting constraint is no longer the expanded read row")
	}
	if !reachConjunct(t, planned.Where(), row) {
		t.Fatal("the filtered selecting constraint lost the expanded read row")
	}
}

func TestNonRestatingRelationPoliciesAreAcceptedForEveryRelationForm(t *testing.T) {
	handles := newReachHandles(t)
	restrictedUsers := func() *golem.Rules[planUser] {
		rules := golem.NewRules[planUser]()
		rules.CanRead(handles.userName.Contains("a"))
		return rules
	}
	restrictedPosts := func() *golem.Rules[planPost] {
		rules := golem.NewRules[planPost]()
		rules.CanRead(handles.title.Contains("q"))
		return rules
	}

	postCases := []struct {
		name string
		rule golem.Predicate[planPost]
	}{
		{"Is", handles.author.Is(handles.userName.Contains("b"))},
		{"IsNull", handles.author.IsNull()},
		{"IsNotNull", handles.author.IsNotNull()},
		{"NotIs", golem.Not(handles.author.Is(handles.userName.Contains("b")))},
		{"IsNot", handles.author.IsNot(handles.userName.Contains("b"))},
	}
	for _, candidate := range postCases {
		t.Run("Post"+candidate.name, func(t *testing.T) {
			postRules := golem.NewRules[planPost]()
			postRules.CanRead(candidate.rule)
			if _, err := handles.planPosts(t, handles.policies(t, restrictedUsers(), postRules), golem.Where(handles.title.Eq("x"))); err != nil {
				t.Fatalf("non-restating %s policy refused: %v", candidate.name, err)
			}
		})
	}

	t.Run("TwoRulesOneRelational", func(t *testing.T) {
		postRules := golem.NewRules[planPost]()
		postRules.CanRead(handles.title.Eq("p"))
		postRules.CanRead(handles.author.Is(handles.userName.Contains("b")))
		if _, err := handles.planPosts(t, handles.policies(t, restrictedUsers(), postRules), golem.Where(handles.title.Eq("x"))); err != nil {
			t.Fatalf("mixed relational policy refused: %v", err)
		}
	})

	userCases := []struct {
		name string
		rule golem.Predicate[planUser]
	}{
		{"Some", handles.userPosts.Some(handles.title.Eq("v"))},
		{"None", handles.userPosts.None(handles.title.Eq("v"))},
		{"Every", handles.userPosts.Every(handles.title.Eq("v"))},
	}
	for _, candidate := range userCases {
		t.Run("User"+candidate.name, func(t *testing.T) {
			userRules := golem.NewRules[planUser]()
			userRules.CanRead(candidate.rule)
			if _, err := handles.planUsers(t, handles.policies(t, userRules, restrictedPosts()), golem.Where(handles.userName.Eq("x"))); err != nil {
				t.Fatalf("non-restating %s policy refused: %v", candidate.name, err)
			}
		})
	}
}

func TestFieldRuleWithRelationIsDischargedAgainstTheExpandedReach(t *testing.T) {
	handles := newReachHandles(t)
	userRules := golem.NewRules[planUser]()
	userRules.CanRead(handles.userName.Contains("a"))
	postRules := golem.NewRules[planPost]()
	postRules.CanRead(golem.All[planPost]())
	postRules.CannotReadFields(golem.All[planPost](), handles.title)
	postRules.CanReadFields(handles.author.Is(handles.userName.Contains("b")), handles.title)
	policies := handles.policies(t, userRules, postRules)

	planned, err := handles.planPosts(t, policies,
		golem.Where(handles.author.Is(handles.userName.Contains("b")).And(handles.title.Eq("x"))),
		golem.OrderBy(handles.title.Asc()),
		golem.Select[planPost](handles.title),
	)
	if err != nil {
		t.Fatalf("relation field rule was not discharged by the caller filter: %v", err)
	}
	for _, field := range planned.Fields() {
		if field.FieldID() == policyir.FieldID(handles.fixture.PostTitle) && !field.DischargedByConstraint() {
			t.Fatal("the projected title was not discharged by the restating caller filter")
		}
	}

	_, err = handles.planPosts(t, policies, golem.Where(handles.title.Eq("x")))
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeField || failure.Field != policyir.FieldID(handles.fixture.PostTitle) {
		t.Fatalf("an undischarged relation field rule was accepted: %v", err)
	}
}

func TestProjectedRelationFieldRuleCarriesRawAndReachMasks(t *testing.T) {
	handles := newReachHandles(t)
	userRules := golem.NewRules[planUser]()
	userRules.CanRead(handles.userName.Contains("a"))
	postRules := golem.NewRules[planPost]()
	postRules.CanRead(golem.All[planPost]())
	postRules.CannotReadFields(golem.All[planPost](), handles.title)
	postRules.CanReadFields(handles.author.Is(handles.userName.Contains("b")), handles.title)
	policies := handles.policies(t, userRules, postRules)

	planned, err := handles.planPosts(t, policies, golem.Select[planPost](handles.title))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, field := range planned.Fields() {
		if field.FieldID() != policyir.FieldID(handles.fixture.PostTitle) {
			continue
		}
		found = true
		raw, masked := field.Mask()
		reach, sqlMasked := field.SQLMask()
		if !masked || !sqlMasked || field.DischargedByConstraint() {
			t.Fatalf("title masked=%v sqlMasked=%v discharged=%v", masked, sqlMasked, field.DischargedByConstraint())
		}
		if sameCondition(t, raw, reach) {
			t.Fatal("the SQL mask is the raw policy condition and can observe unreadable authors")
		}
		_, _, _, _, child, ok := reach.Relation()
		if !ok || child == nil || !reachConjunctMentions(*child, policyir.FieldID(handles.fixture.UserName), 2) {
			t.Fatalf("the SQL mask does not conjoin the author read policy: %#v", reach)
		}
	}
	if !found {
		t.Fatal("the projected title is absent")
	}
}

func TestRelationFilterChildTargetingNonRestatingPolicyIsAccepted(t *testing.T) {
	handles := newReachHandles(t)
	userRules := golem.NewRules[planUser]()
	userRules.CanRead(handles.userName.Contains("a"))
	postRules := golem.NewRules[planPost]()
	postRules.CanRead(handles.author.Is(handles.userName.Contains("b")))
	policies := handles.policies(t, userRules, postRules)

	if _, err := handles.planUsers(t, policies, golem.Where(handles.userPosts.Some(handles.title.Eq("x")))); err != nil {
		t.Fatalf("relation filter child over a non-restating target policy refused: %v", err)
	}
	if _, err := handles.planUsers(t, policies, golem.Select[planUser](handles.userPosts.Args(golem.Where(handles.title.Eq("x"))))); err != nil {
		t.Fatalf("nested read over a non-restating target policy refused: %v", err)
	}
}

func TestSelfRelationCycleWithRelationFreeTargetIsStillAccepted(t *testing.T) {
	fixture := schematest.NewSubscribedRecursiveComment(t)
	descriptor := golem.GeneratedModelDescriptor[reachComment](fixture.Comment, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	body := golem.GeneratedTextField[reachComment, string](fixture.Body)
	parent := golem.GeneratedToOne[reachComment, reachComment](fixture.Parent, fixture.Threading, fixture.Comment)
	rules := golem.NewRules[reachComment]()
	rules.CanRead(body.Contains("a"))
	policies := policyMap{policyir.ModelID(fixture.Comment): reachCommentPolicy(t, fixture, rules)}

	frozen, err := golem.FreezeFindMany(descriptor, golem.Where(parent.Is(body.Contains("p"))), golem.Select[reachComment](body, parent.Select(body)))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Caller(request, fixture.Registry, policies, DefaultLimits()); err != nil {
		t.Fatalf("self relation over a relation-free policy refused: %v", err)
	}
}

func TestMutuallyRecursiveRelationFieldGrantIsRejectedByTheWidenedRowExpansion(t *testing.T) {
	handles := newReachHandles(t)
	userRules := golem.NewRules[planUser]()
	userRules.CanRead(handles.userName.Contains("a"))
	userRules.CanReadFields(handles.userPosts.Some(handles.title.Eq("v")), handles.userName)
	postRules := golem.NewRules[planPost]()
	postRules.CanRead(handles.author.Is(handles.userName.Contains("b")))
	policies := handles.policies(t, userRules, postRules)

	_, err := handles.planUsers(t, policies, golem.Select[planUser](handles.userName))
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy || failure.Model != policyir.ModelID(handles.fixture.User) || failure.Detail != "cyclic relation policy cannot be expanded safely" {
		t.Fatalf("mutually recursive relation field rule error=%v", err)
	}
}

func TestFieldConditionCycleIsRejectedByTheReachLensAfterTheRowExpands(t *testing.T) {
	modelA, modelB, modelC := expansionModel(30), expansionModel(31), expansionModel(32)
	trueA, _ := policyir.NewConstant(modelA, true)
	trueB, _ := policyir.NewConstant(modelB, true)
	trueC, _ := policyir.NewConstant(modelC, true)
	aToC := expansionRelation(t, modelA, expansionField(30), expansionRelationID(30), modelC, policyir.OperatorRelationSome, &trueC)
	aToB := expansionRelation(t, modelA, expansionField(31), expansionRelationID(31), modelB, policyir.OperatorRelationSome, &trueB)
	bToA := expansionRelation(t, modelB, expansionField(32), expansionRelationID(32), modelA, policyir.OperatorRelationSome, &trueA)
	policies := policyMap{
		modelA: expansionPolicy(t, modelA, &aToC),
		modelB: expansionPolicy(t, modelB, &bToA),
		modelC: expansionPolicy(t, modelC, nil),
	}
	expander := newPolicyExpander(policies, 8)
	if _, err := expander.model(modelA, 0); err != nil {
		t.Fatalf("the acyclic row expansion was refused: %v", err)
	}
	reach := expander.reach(modelA, 0)
	if _, err := reach.Read(aToC); err != nil {
		t.Fatalf("an acyclic field condition was refused: %v", err)
	}
	_, err := reach.Read(aToB)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy || failure.Model != modelA || failure.Detail != "cyclic relation policy cannot be expanded safely" {
		t.Fatalf("a field condition re-entering a relation-bearing root was accepted: %v", err)
	}
	if _, err := newPolicyExpander(policies, 8).condition(aToB, 0); err != nil {
		t.Fatalf("the same condition outside the root lens should expand, so the lens is what rejects it: %v", err)
	}
}

func reachCommentPolicy(t *testing.T, fixture schematest.RecursiveCommentFixture, rules *golem.Rules[reachComment]) policyir.Policy {
	t.Helper()
	return expansionBoundPolicy(t, schematest.Fixture{Registry: fixture.Registry}, fixture.Comment, rules)
}

func reachConjunct(t *testing.T, condition, wanted policyir.Condition) bool {
	t.Helper()
	if sameCondition(t, condition, wanted) {
		return true
	}
	op, children, ok := condition.Logical()
	if !ok || op != policyir.LogicalAnd {
		return false
	}
	for _, child := range children {
		if sameCondition(t, child, wanted) {
			return true
		}
	}
	return false
}

func sameCondition(t *testing.T, left, right policyir.Condition) bool {
	t.Helper()
	leftBytes, err := policyir.CanonicalCondition(left)
	if err != nil {
		t.Fatal(err)
	}
	rightBytes, err := policyir.CanonicalCondition(right)
	if err != nil {
		t.Fatal(err)
	}
	return string(leftBytes) == string(rightBytes)
}

func reachConjunctMentions(condition policyir.Condition, field policyir.FieldID, want int) bool {
	return countFieldLeaves(condition, field) == want
}

func countFieldLeaves(condition policyir.Condition, field policyir.FieldID) int {
	switch condition.Kind() {
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		if leaf, ok := condition.Field(); ok && leaf == field {
			return 1
		}
	case policyir.ConditionLogical:
		_, children, _ := condition.Logical()
		total := 0
		for _, child := range children {
			total += countFieldLeaves(child, field)
		}
		return total
	}
	return 0
}
