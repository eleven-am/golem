package nested

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/imply"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type nestedReach struct {
	fixture  schematest.Fixture
	author   golem.EqualField[nestedPost, golem.UUID]
	title    golem.TextField[nestedPost, string]
	writer   golem.ToOne[nestedPost, nestedUser]
	userName golem.TextField[nestedUser, string]
}

func newNestedReach(t *testing.T) nestedReach {
	t.Helper()
	fixture := schematest.New(t)
	return nestedReach{
		fixture:  fixture,
		author:   golem.GeneratedEqualField[nestedPost, golem.UUID](fixture.AuthorID),
		title:    golem.GeneratedTextField[nestedPost, string](fixture.PostTitle),
		writer:   golem.GeneratedToOne[nestedPost, nestedUser](fixture.PostAuthor, fixture.Authorship, fixture.User),
		userName: golem.GeneratedTextField[nestedUser, string](fixture.UserName),
	}
}

func bindNestedRules[M any](t *testing.T, fixture schematest.Fixture, model golem.ModelID, rules *golem.Rules[M]) policyir.Policy {
	t.Helper()
	frozen, err := rules.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := policybind.Policy(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalize.Policy(bound)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func (reach nestedReach) policies(t *testing.T, posts *golem.Rules[nestedPost], users *golem.Rules[nestedUser]) testPolicies {
	t.Helper()
	if users == nil {
		users = golem.NewRules[nestedUser]()
		users.CanRead(golem.All[nestedUser]())
		users.CanUpdate(golem.All[nestedUser]())
	}
	return testPolicies{generation: reach.fixture.Registry.GenerationDigest(), values: map[policyir.ModelID]policyir.Policy{
		policyir.ModelID(reach.fixture.User): bindNestedRules(t, reach.fixture, reach.fixture.User, users),
		policyir.ModelID(reach.fixture.Post): bindNestedRules(t, reach.fixture, reach.fixture.Post, posts),
	}}
}

var nestedExistingOperations = []struct {
	name      string
	operation mutationir.Operation
}{
	{"delete", mutationir.Delete},
	{"update", mutationir.Update},
	{"upsert", mutationir.Upsert},
}

func (reach nestedReach) mutations(t *testing.T, operation mutationir.Operation) []golem.FrozenNestedMutation {
	t.Helper()
	fixture := reach.fixture
	selector := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{3})))
	update := golem.GeneratedUpdateInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, reach.title, "updated"))
	selected := golem.GeneratedCreateInput[nestedPost](fixture.Post, golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[nestedPost, golem.UUID](fixture.PostID), golem.NewUUID([16]byte{3})), golem.GeneratedCreateFieldValue(fixture.Post, reach.title, "title"))
	var mutation golem.NestedUpdateValue[nestedUser]
	switch operation {
	case mutationir.Delete:
		mutation = golem.GeneratedNestedDelete[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector)
	case mutationir.Update:
		mutation = golem.GeneratedNestedUpdate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, update)
	default:
		mutation = golem.GeneratedNestedUpsert[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, selected, update)
	}
	return freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User, mutation))
}

func (reach nestedReach) build(t *testing.T, policies testPolicies, operation mutationir.Operation) (Result, error) {
	t.Helper()
	return Build(Request{Root: callerRoot(t, reach.fixture), Mutations: reach.mutations(t, operation), Stance: mutationir.Caller, Registry: reach.fixture.Registry, Policies: policies, MaxDepth: 3, MaxRows: 5})
}

func grantNestedWrite(rules *golem.Rules[nestedPost], operation mutationir.Operation, condition golem.Predicate[nestedPost]) {
	if operation == mutationir.Delete {
		rules.CanDelete(condition)
		return
	}
	rules.CanUpdate(condition)
}

func nestedWriteAction(operation mutationir.Operation) policyir.Action {
	if operation == mutationir.Delete {
		return policyir.ActionDelete
	}
	return policyir.ActionUpdate
}

func postSelections(t *testing.T, result Result, model golem.ModelID) []policyir.Condition {
	t.Helper()
	var selections []policyir.Condition
	for _, node := range result.Graph().Nodes()[1:] {
		if node.ModelID() != policyir.ModelID(model) {
			continue
		}
		if selection, ok := node.SelectionRequirement(); ok {
			selections = append(selections, selection.Constraint())
		}
	}
	if len(selections) == 0 {
		t.Fatal("nested existing-row write has no selection requirement")
	}
	return selections
}

func nestedImplies(t *testing.T, selecting, required policyir.Condition) bool {
	t.Helper()
	proved, err := imply.Condition(selecting, required)
	if err != nil {
		t.Fatal(err)
	}
	return proved
}

func TestNestedNarrowerWriteRulePlansOverReadReach(t *testing.T) {
	for _, test := range nestedExistingOperations {
		t.Run(test.name, func(t *testing.T) {
			reach := newNestedReach(t)
			rules := golem.NewRules[nestedPost]()
			rules.CanRead(reach.author.In(golem.NewUUID([16]byte{8}), golem.NewUUID([16]byte{9})))
			grantNestedWrite(rules, test.operation, reach.author.Eq(golem.NewUUID([16]byte{9})))
			policies := reach.policies(t, rules, nil)
			result, err := reach.build(t, policies, test.operation)
			if err != nil {
				t.Fatalf("nested write narrower than read refused: %v", err)
			}
			policy, _ := policies.Policy(policyir.ModelID(reach.fixture.Post))
			read, err := resolve.FieldCondition(policy, policyir.ActionRead, policyir.ModelID(reach.fixture.Post), policyir.FieldID(reach.fixture.PostID))
			if err != nil {
				t.Fatal(err)
			}
			for _, selection := range postSelections(t, result, reach.fixture.Post) {
				if !nestedImplies(t, selection, read) {
					t.Fatal("nested selection does not carry the read conjunct")
				}
			}
		})
	}
}

func TestNestedUnconditionalWriteNarrowsToReadableRows(t *testing.T) {
	for _, test := range nestedExistingOperations {
		t.Run(test.name, func(t *testing.T) {
			reach := newNestedReach(t)
			rules := golem.NewRules[nestedPost]()
			rules.CanRead(reach.title.Eq("mine"))
			grantNestedWrite(rules, test.operation, golem.All[nestedPost]())
			policies := reach.policies(t, rules, nil)
			result, err := reach.build(t, policies, test.operation)
			if err != nil {
				t.Fatalf("nested unconditional write refused: %v", err)
			}
			policy, _ := policies.Policy(policyir.ModelID(reach.fixture.Post))
			read, err := resolve.FieldCondition(policy, policyir.ActionRead, policyir.ModelID(reach.fixture.Post), policyir.FieldID(reach.fixture.PostID))
			if err != nil {
				t.Fatal(err)
			}
			for _, selection := range postSelections(t, result, reach.fixture.Post) {
				if !nestedImplies(t, selection, read) {
					t.Fatal("nested unconditional write did not narrow to readable rows")
				}
			}
		})
	}
}

func TestNestedMissingWriteGrantIsAPolicyRefusal(t *testing.T) {
	for _, test := range nestedExistingOperations {
		t.Run(test.name, func(t *testing.T) {
			reach := newNestedReach(t)
			rules := golem.NewRules[nestedPost]()
			rules.CanRead(golem.All[nestedPost]())
			rules.CanCreate(golem.All[nestedPost]())
			_, err := reach.build(t, reach.policies(t, rules, nil), test.operation)
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != CodePolicy || failure.Reason != golem.ReasonNotGranted {
				t.Fatalf("nested missing %s grant error=%#v", test.name, err)
			}
		})
	}
}

func TestNestedUnprovenPositionCarriesAuthorizationUnproven(t *testing.T) {
	reach := newNestedReach(t)
	rules := golem.NewRules[nestedPost]()
	rules.CanDelete(golem.All[nestedPost]())
	_, err := reach.build(t, reach.policies(t, rules, nil), mutationir.Delete)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeClassification || failure.Reason != golem.ReasonAuthorizationUnproven {
		t.Fatalf("nested unproven position error=%#v", err)
	}
}

func TestNestedWriteActionRelationHopsRangeOverReadableTargets(t *testing.T) {
	for _, test := range nestedExistingOperations {
		t.Run(test.name, func(t *testing.T) {
			reach := newNestedReach(t)
			users := golem.NewRules[nestedUser]()
			users.CanRead(reach.userName.Contains("a"))
			users.CanUpdate(golem.All[nestedUser]())
			rules := golem.NewRules[nestedPost]()
			rules.CanRead(golem.All[nestedPost]())
			grantNestedWrite(rules, test.operation, reach.writer.Is(reach.userName.Contains("b")))
			policies := reach.policies(t, rules, users)
			result, err := reach.build(t, policies, test.operation)
			if err != nil {
				t.Fatal(err)
			}
			expected := golem.NewRules[nestedPost]()
			grantNestedWrite(expected, test.operation, reach.writer.Is(golem.And(reach.userName.Contains("a"), reach.userName.Contains("b"))))
			expanded, err := resolve.RowConstraint(bindNestedRules(t, reach.fixture, reach.fixture.Post, expected), nestedWriteAction(test.operation), policyir.ModelID(reach.fixture.Post))
			if err != nil {
				t.Fatal(err)
			}
			policy, _ := policies.Policy(policyir.ModelID(reach.fixture.Post))
			raw, err := resolve.RowConstraint(policy, nestedWriteAction(test.operation), policyir.ModelID(reach.fixture.Post))
			if err != nil {
				t.Fatal(err)
			}
			for _, selection := range postSelections(t, result, reach.fixture.Post) {
				if !nestedImplies(t, selection, expanded) || nestedImplies(t, selection, raw) {
					t.Fatal("nested write action relation hop was not expanded over the target read reach")
				}
			}
		})
	}
}

func TestNestedCurrentToOneUpdateNarrowsToReadableTarget(t *testing.T) {
	reach := newNestedReach(t)
	fixture := reach.fixture
	users := golem.NewRules[nestedUser]()
	users.CanRead(reach.userName.Contains("a"))
	users.CanUpdate(golem.All[nestedUser]())
	posts := golem.NewRules[nestedPost]()
	posts.CanRead(golem.All[nestedPost]())
	posts.CanUpdate(golem.All[nestedPost]())
	policies := reach.policies(t, posts, users)
	update := golem.GeneratedUpdateInput[nestedUser](fixture.User, golem.GeneratedSetFieldValue(fixture.User, reach.userName, "renamed"))
	mutation := golem.GeneratedNestedUpdate[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, nil, update)
	root := rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID)
	truth, err := policyir.NewConstant(root.Model, true)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	if err != nil {
		t.Fatal(err)
	}
	root.Selection, root.RowPostcondition = &selection, &truth
	result, err := Build(Request{Root: root, Mutations: freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post, mutation)), Stance: mutationir.Caller, Registry: fixture.Registry, Policies: policies, MaxDepth: 3, MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := policies.Policy(policyir.ModelID(fixture.User))
	readable, err := resolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(fixture.User))
	if err != nil {
		t.Fatal(err)
	}
	for _, constraint := range postSelections(t, result, fixture.User) {
		if !nestedImplies(t, constraint, readable) {
			t.Fatal("current to-one update did not narrow to readable targets")
		}
	}
}
