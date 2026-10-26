package nested

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

func (reach nestedReach) userRoot(t *testing.T, mutations ...golem.NestedUpdateValue[nestedUser]) Request {
	t.Helper()
	values := make([]golem.UpdateValue[nestedUser], len(mutations))
	for index, mutation := range mutations {
		values[index] = mutation
	}
	return Request{Root: callerRoot(t, reach.fixture), Mutations: freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](reach.fixture.User, values...)), Stance: mutationir.Caller, Registry: reach.fixture.Registry, MaxDepth: 3, MaxRows: 5}
}

func (reach nestedReach) postRoot(t *testing.T, mutations ...golem.NestedUpdateValue[nestedPost]) Request {
	t.Helper()
	values := make([]golem.UpdateValue[nestedPost], len(mutations))
	for index, mutation := range mutations {
		values[index] = mutation
	}
	root := rootForModel(t, reach.fixture, reach.fixture.Post, reach.fixture.PostKey, reach.fixture.PostID)
	truth, err := policyir.NewConstant(root.Model, true)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	if err != nil {
		t.Fatal(err)
	}
	root.Selection, root.RowPostcondition = &selection, &truth
	return Request{Root: root, Mutations: freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](reach.fixture.Post, values...)), Stance: mutationir.Caller, Registry: reach.fixture.Registry, MaxDepth: 3, MaxRows: 5}
}

func readRow(t *testing.T, policies testPolicies, model golem.ModelID) policyir.Condition {
	t.Helper()
	policy, _ := policies.Policy(policyir.ModelID(model))
	row, err := resolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(model))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestNestedConstantBatchPredicateNarrowsToReadableRows(t *testing.T) {
	for _, operation := range []mutationir.Operation{mutationir.UpdateMany, mutationir.DeleteMany} {
		t.Run(map[mutationir.Operation]string{mutationir.UpdateMany: "updateMany", mutationir.DeleteMany: "deleteMany"}[operation], func(t *testing.T) {
			reach := newNestedReach(t)
			fixture := reach.fixture
			rules := golem.NewRules[nestedPost]()
			rules.CanRead(reach.author.Eq(golem.NewUUID([16]byte{9})))
			rules.CanUpdate(golem.All[nestedPost]())
			rules.CanDelete(golem.All[nestedPost]())
			policies := reach.policies(t, rules, nil)
			var mutation golem.NestedUpdateValue[nestedUser]
			if operation == mutationir.DeleteMany {
				mutation = golem.GeneratedNestedDeleteMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, golem.All[nestedPost]())
			} else {
				data := golem.GeneratedUpdateManyInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, reach.title, "many"))
				mutation = golem.GeneratedNestedUpdateMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, golem.All[nestedPost](), data)
			}
			request := reach.userRoot(t, mutation)
			request.Policies = policies
			result, err := Build(request)
			if err != nil {
				t.Fatal(err)
			}
			readable := readRow(t, policies, fixture.Post)
			for _, constraint := range postSelections(t, result, fixture.Post) {
				if !nestedImplies(t, constraint, readable) {
					t.Fatal("constant batch predicate did not narrow to readable rows")
				}
			}
		})
	}
}

func TestNestedCurrentToOneUpsertNarrowsToReadableTarget(t *testing.T) {
	reach := newNestedReach(t)
	fixture := reach.fixture
	users := golem.NewRules[nestedUser]()
	users.CanRead(reach.userName.Contains("a"))
	users.CanCreate(golem.All[nestedUser]())
	users.CanUpdate(golem.All[nestedUser]())
	posts := golem.NewRules[nestedPost]()
	posts.CanRead(golem.All[nestedPost]())
	posts.CanUpdate(golem.All[nestedPost]())
	policies := reach.policies(t, posts, users)
	create := golem.GeneratedCreateInput[nestedUser](fixture.User,
		golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedEqualField[nestedUser, golem.UUID](fixture.UserID), golem.NewUUID([16]byte{7})),
		golem.GeneratedCreateFieldValue(fixture.User, reach.userName, "made"),
	)
	update := golem.GeneratedUpdateInput[nestedUser](fixture.User, golem.GeneratedSetFieldValue(fixture.User, reach.userName, "renamed"))
	request := reach.postRoot(t, golem.GeneratedNestedUpsert[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, nil, create, update))
	request.Policies = policies
	result, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	readable := readRow(t, policies, fixture.User)
	for _, constraint := range postSelections(t, result, fixture.User) {
		if !nestedImplies(t, constraint, readable) {
			t.Fatal("current to-one upsert did not narrow to readable targets")
		}
	}
}

func TestNestedCreateWithoutGrantIsRefusedAtPlanTime(t *testing.T) {
	reach := newNestedReach(t)
	fixture := reach.fixture
	rules := golem.NewRules[nestedPost]()
	rules.CanRead(golem.All[nestedPost]())
	request := reach.userRoot(t, golem.GeneratedNestedCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, nestedPostCreate(t, fixture)))
	request.Policies = reach.policies(t, rules, nil)
	_, err := Build(request)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy || failure.Reason != golem.ReasonNotGranted {
		t.Fatalf("ungranted nested create error=%#v", err)
	}
}

func (reach nestedReach) relationFilterPolicies(t *testing.T) testPolicies {
	t.Helper()
	users := golem.NewRules[nestedUser]()
	users.CanRead(reach.userName.Contains("a"))
	users.CanUpdate(golem.All[nestedUser]())
	posts := golem.NewRules[nestedPost]()
	posts.CanRead(golem.All[nestedPost]())
	posts.CanUpdate(golem.All[nestedPost]())
	posts.CanDelete(golem.All[nestedPost]())
	return reach.policies(t, posts, users)
}

func (reach nestedReach) expandedWriterFilter(t *testing.T) (policyir.Condition, policyir.Condition) {
	t.Helper()
	raw := golem.NewRules[nestedPost]()
	raw.CanRead(reach.writer.Is(reach.userName.Contains("b")))
	expanded := golem.NewRules[nestedPost]()
	expanded.CanRead(reach.writer.Is(golem.And(reach.userName.Contains("a"), reach.userName.Contains("b"))))
	return readRow(t, testPolicies{values: map[policyir.ModelID]policyir.Policy{policyir.ModelID(reach.fixture.Post): bindNestedRules(t, reach.fixture, reach.fixture.Post, raw)}}, reach.fixture.Post),
		readRow(t, testPolicies{values: map[policyir.ModelID]policyir.Policy{policyir.ModelID(reach.fixture.Post): bindNestedRules(t, reach.fixture, reach.fixture.Post, expanded)}}, reach.fixture.Post)
}

func TestNestedBatchRelationFilterRangesOverReadableTargets(t *testing.T) {
	reach := newNestedReach(t)
	fixture := reach.fixture
	policies := reach.relationFilterPolicies(t)
	raw, expanded := reach.expandedWriterFilter(t)
	request := reach.userRoot(t, golem.GeneratedNestedDeleteMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, reach.writer.Is(reach.userName.Contains("b"))))
	request.Policies = policies
	result, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, node := range result.Graph().Nodes()[1:] {
		position, ok := node.RelationPosition()
		if !ok {
			continue
		}
		predicate, ok := position.Predicate()
		if !ok {
			continue
		}
		checked++
		if !nestedImplies(t, predicate, expanded) || nestedImplies(t, predicate, raw) {
			t.Fatal("nested batch relation filter is rendered over unreadable targets")
		}
	}
	if checked == 0 {
		t.Fatal("nested batch predicate was not retained")
	}
	for _, constraint := range postSelections(t, result, fixture.Post) {
		if nestedImplies(t, constraint, raw) {
			t.Fatal("nested batch selection carries the raw relation filter")
		}
	}
}

func TestNestedTargetGuardRelationRangesOverReadableTargets(t *testing.T) {
	reach := newNestedReach(t)
	fixture := reach.fixture
	policies := reach.relationFilterPolicies(t)
	raw, expanded := reach.expandedWriterFilter(t)
	selector := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{3})))
	target := selector.And(reach.writer.Is(reach.userName.Contains("b")))
	request := reach.userRoot(t, golem.GeneratedNestedDelete[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, target))
	request.Policies = policies
	result, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, node := range result.Graph().Nodes()[1:] {
		position, ok := node.RelationPosition()
		if !ok {
			continue
		}
		bound, ok := position.Target()
		if !ok {
			continue
		}
		guard, ok := bound.Guard()
		if !ok {
			continue
		}
		checked++
		if !nestedImplies(t, guard, expanded) || nestedImplies(t, guard, raw) {
			t.Fatal("nested target guard is rendered over unreadable targets")
		}
	}
	if checked == 0 {
		t.Fatal("nested target guard was not retained")
	}
}
