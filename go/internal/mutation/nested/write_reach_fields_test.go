package nested

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func (reach nestedReach) fieldScopedPolicies(t *testing.T) testPolicies {
	t.Helper()
	rules := golem.NewRules[nestedPost]()
	rules.CanReadFields(reach.author.Eq(golem.NewUUID([16]byte{9})), golem.GeneratedEqualField[nestedPost, golem.UUID](reach.fixture.PostID), reach.title)
	rules.CanUpdate(golem.All[nestedPost]())
	rules.CanDelete(golem.All[nestedPost]())
	return reach.policies(t, rules, nil)
}

func TestNestedBatchUnderFieldScopedReadPlansAndNarrows(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation mutationir.Operation
		where     func(nestedReach) golem.Predicate[nestedPost]
	}{
		{"deleteMany-title", mutationir.DeleteMany, func(reach nestedReach) golem.Predicate[nestedPost] { return reach.title.Eq("x") }},
		{"updateMany-title", mutationir.UpdateMany, func(reach nestedReach) golem.Predicate[nestedPost] { return reach.title.Eq("x") }},
		{"deleteMany-all", mutationir.DeleteMany, func(nestedReach) golem.Predicate[nestedPost] { return golem.All[nestedPost]() }},
		{"updateMany-all", mutationir.UpdateMany, func(nestedReach) golem.Predicate[nestedPost] { return golem.All[nestedPost]() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			reach := newNestedReach(t)
			fixture := reach.fixture
			policies := reach.fieldScopedPolicies(t)
			var mutation golem.NestedUpdateValue[nestedUser]
			if test.operation == mutationir.DeleteMany {
				mutation = golem.GeneratedNestedDeleteMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, test.where(reach))
			} else {
				data := golem.GeneratedUpdateManyInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, reach.title, "many"))
				mutation = golem.GeneratedNestedUpdateMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, test.where(reach), data)
			}
			request := reach.userRoot(t, mutation)
			request.Policies = policies
			result, err := Build(request)
			if err != nil {
				t.Fatalf("nested batch under a field-scoped read rule refused: %v", err)
			}
			readable := readRow(t, policies, fixture.Post)
			for _, constraint := range postSelections(t, result, fixture.Post) {
				if !nestedImplies(t, constraint, readable) {
					t.Fatal("nested batch did not narrow to readable rows")
				}
			}
			for _, audit := range result.PositionAudits() {
				if audit.ModelID() != policyir.ModelID(fixture.Post) {
					continue
				}
				for _, field := range audit.Fields() {
					if field == policyir.FieldID(fixture.AuthorID) {
						t.Fatal("policy-authored conjunct was audited as a caller position field")
					}
				}
			}
		})
	}
}

func (reach nestedReach) cyclicPolicies(t *testing.T) testPolicies {
	t.Helper()
	rules := golem.NewRules[nestedPost]()
	rules.CanRead(reach.writer.Is(reach.userName.Contains("a")))
	rules.CanUpdate(golem.All[nestedPost]())
	rules.CanDelete(golem.All[nestedPost]())
	return reach.policies(t, rules, nil)
}

func (reach nestedReach) cyclicFilter() golem.Predicate[nestedPost] {
	written := golem.GeneratedToMany[nestedUser, nestedPost](reach.fixture.UserPosts, reach.fixture.Authorship, reach.fixture.Post)
	return reach.writer.Is(written.Some(reach.title.Eq("x")))
}

func assertNestedPolicyInvalid(t *testing.T, err error) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy {
		t.Fatalf("cyclic expansion error=%#v", err)
	}
}

func TestNestedPredicateExpansionCycleIsAPolicyRefusal(t *testing.T) {
	reach := newNestedReach(t)
	fixture := reach.fixture
	request := reach.userRoot(t, golem.GeneratedNestedDeleteMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, reach.cyclicFilter()))
	request.Policies = reach.cyclicPolicies(t)
	_, err := Build(request)
	assertNestedPolicyInvalid(t, err)
}

func TestNestedGuardExpansionCycleIsAPolicyRefusal(t *testing.T) {
	reach := newNestedReach(t)
	fixture := reach.fixture
	selector := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{3})))
	request := reach.userRoot(t, golem.GeneratedNestedDelete[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector.And(reach.cyclicFilter())))
	request.Policies = reach.cyclicPolicies(t)
	_, err := Build(request)
	assertNestedPolicyInvalid(t, err)
}
