package plan

import (
	"bytes"
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

type reachPost struct{}
type reachUser struct{}

type writeReachHandles struct {
	fixture  schematest.Fixture
	inputs   boundSet
	id       golem.EqualField[reachPost, golem.UUID]
	author   golem.EqualField[reachPost, golem.UUID]
	title    golem.TextField[reachPost, string]
	writer   golem.ToOne[reachPost, reachUser]
	userName golem.TextField[reachUser, string]
}

func newWriteReachHandles(t *testing.T) writeReachHandles {
	t.Helper()
	fixture := schematest.New(t)
	return writeReachHandles{
		fixture:  fixture,
		inputs:   boundInputs(t, fixture),
		id:       golem.GeneratedEqualField[reachPost, golem.UUID](fixture.PostID),
		author:   golem.GeneratedEqualField[reachPost, golem.UUID](fixture.AuthorID),
		title:    golem.GeneratedTextField[reachPost, string](fixture.PostTitle),
		writer:   golem.GeneratedToOne[reachPost, reachUser](fixture.PostAuthor, fixture.Authorship, fixture.User),
		userName: golem.GeneratedTextField[reachUser, string](fixture.UserName),
	}
}

func bindRules[M any](t *testing.T, fixture schematest.Fixture, model golem.ModelID, rules *golem.Rules[M]) policyir.Policy {
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

func (handles writeReachHandles) policies(t *testing.T, post *golem.Rules[reachPost], user *golem.Rules[reachUser]) testPolicies {
	t.Helper()
	values := map[policyir.ModelID]policyir.Policy{policyir.ModelID(handles.fixture.Post): bindRules(t, handles.fixture, handles.fixture.Post, post)}
	if user == nil {
		values[policyir.ModelID(handles.fixture.User)] = readablePolicy(policyir.ModelID(handles.fixture.User))
	} else {
		values[policyir.ModelID(handles.fixture.User)] = bindRules(t, handles.fixture, handles.fixture.User, user)
	}
	return testPolicies{generation: handles.fixture.Registry.GenerationDigest(), provider: policyir.ProviderSQLite, values: values}
}

func (handles writeReachHandles) request(t *testing.T, policies testPolicies, operation mutationir.Operation) RootRequest {
	t.Helper()
	request := baseRequest(t, handles.fixture, policies, mutationir.Caller, operation)
	request.Target = &handles.inputs.target
	switch operation {
	case mutationir.Update:
		request.Update = &handles.inputs.update
	case mutationir.Upsert:
		request.Create, request.Update, request.Retry = &handles.inputs.create, &handles.inputs.update, mutationir.EngineOwnedUpsertRetry
	}
	return request
}

func grantWrite(rules *golem.Rules[reachPost], operation mutationir.Operation, condition golem.Predicate[reachPost]) {
	if operation == mutationir.Delete {
		rules.CanDelete(condition)
		return
	}
	rules.CanUpdate(condition)
}

func writeAction(operation mutationir.Operation) policyir.Action {
	if operation == mutationir.Delete {
		return policyir.ActionDelete
	}
	return policyir.ActionUpdate
}

var existingRowOperations = []struct {
	name      string
	operation mutationir.Operation
}{
	{"delete", mutationir.Delete},
	{"update", mutationir.Update},
	{"upsert", mutationir.Upsert},
}

func rootSelection(t *testing.T, planned mutationir.Plan) policyir.Condition {
	t.Helper()
	root, _ := planned.Graph().Root()
	selection, ok := root.SelectionRequirement()
	if !ok {
		t.Fatal("existing-row plan has no selection requirement")
	}
	return selection.Constraint()
}

func implies(t *testing.T, selecting, required policyir.Condition) bool {
	t.Helper()
	proved, err := imply.Condition(selecting, required)
	if err != nil {
		t.Fatal(err)
	}
	return proved
}

func readFieldCondition(t *testing.T, policies testPolicies, model policyir.ModelID, field policyir.FieldID) policyir.Condition {
	t.Helper()
	policy, _ := policies.Policy(model)
	condition, err := resolve.FieldCondition(policy, policyir.ActionRead, model, field)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func uuid(value byte) golem.UUID { return golem.NewUUID([16]byte{value}) }

func TestNarrowerDeleteRulePlansOverReadReach(t *testing.T) {
	for _, test := range existingRowOperations {
		t.Run(test.name, func(t *testing.T) {
			handles := newWriteReachHandles(t)
			rules := golem.NewRules[reachPost]()
			rules.CanRead(handles.author.In(uuid(8), uuid(9)))
			grantWrite(rules, test.operation, handles.author.Eq(uuid(9)))
			policies := handles.policies(t, rules, nil)
			planned, err := BuildRoot(handles.request(t, policies, test.operation))
			if err != nil {
				t.Fatalf("write narrower than read refused: %v", err)
			}
			read := readFieldCondition(t, policies, policyir.ModelID(handles.fixture.Post), policyir.FieldID(handles.fixture.PostID))
			if !implies(t, rootSelection(t, planned), read) {
				t.Fatal("selection does not carry the read conjunct")
			}
		})
	}
}

func TestMissingDeleteGrantIsAPolicyRefusal(t *testing.T) {
	for _, test := range existingRowOperations {
		t.Run(test.name, func(t *testing.T) {
			handles := newWriteReachHandles(t)
			model := policyir.ModelID(handles.fixture.Post)
			policy := policyWithRules(t, model, rule(t, model, policyir.ActionRead, nil, 0), rule(t, model, policyir.ActionCreate, nil, 1))
			_, err := BuildRoot(handles.request(t, policySet(handles.fixture, policy), test.operation))
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != CodePolicy || failure.Reason != golem.ReasonNotGranted {
				t.Fatalf("missing %s grant error=%#v", test.name, err)
			}
		})
	}
}

func TestMissingCreateGrantCarriesNotGranted(t *testing.T) {
	handles := newWriteReachHandles(t)
	model := policyir.ModelID(handles.fixture.Post)
	policy := policyWithRules(t, model, rule(t, model, policyir.ActionRead, nil, 0))
	request := baseRequest(t, handles.fixture, policySet(handles.fixture, policy), mutationir.Caller, mutationir.Create)
	request.Create = &handles.inputs.create
	_, err := BuildRoot(request)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy || failure.Reason != golem.ReasonNotGranted {
		t.Fatalf("missing create grant error=%#v", err)
	}
}

func TestDisjointSyntaxWriteRulePlansWithReadConjunct(t *testing.T) {
	for _, test := range existingRowOperations {
		t.Run(test.name, func(t *testing.T) {
			handles := newWriteReachHandles(t)
			rules := golem.NewRules[reachPost]()
			rules.CanRead(handles.author.Eq(uuid(9)))
			grantWrite(rules, test.operation, handles.title.Eq("draft"))
			policies := handles.policies(t, rules, nil)
			planned, err := BuildRoot(handles.request(t, policies, test.operation))
			if err != nil {
				t.Fatalf("disjoint-syntax write rule refused: %v", err)
			}
			selection := rootSelection(t, planned)
			read := readFieldCondition(t, policies, policyir.ModelID(handles.fixture.Post), policyir.FieldID(handles.fixture.PostID))
			policy, _ := policies.Policy(policyir.ModelID(handles.fixture.Post))
			write, err := resolve.RowConstraint(policy, writeAction(test.operation), policyir.ModelID(handles.fixture.Post))
			if err != nil {
				t.Fatal(err)
			}
			if !implies(t, selection, read) || !implies(t, selection, write) {
				t.Fatal("selection lost the read conjunct or the write rule")
			}
		})
	}
}

func TestUnconditionalWriteNarrowsToReadableRows(t *testing.T) {
	for _, test := range existingRowOperations {
		t.Run(test.name, func(t *testing.T) {
			handles := newWriteReachHandles(t)
			rules := golem.NewRules[reachPost]()
			rules.CanRead(handles.author.Eq(uuid(9)))
			grantWrite(rules, test.operation, golem.All[reachPost]())
			policies := handles.policies(t, rules, nil)
			planned, err := BuildRoot(handles.request(t, policies, test.operation))
			if err != nil {
				t.Fatalf("unconditional write with narrower read refused: %v", err)
			}
			read := readFieldCondition(t, policies, policyir.ModelID(handles.fixture.Post), policyir.FieldID(handles.fixture.PostID))
			if !implies(t, rootSelection(t, planned), read) {
				t.Fatal("unconditional write did not narrow to readable rows")
			}
		})
	}
}

func TestSelectorConjoinsFieldReachNotWidenedReadRow(t *testing.T) {
	handles := newWriteReachHandles(t)
	rules := golem.NewRules[reachPost]()
	rules.CanRead(handles.author.Eq(uuid(9)))
	rules.CanReadFields(handles.title.Eq("public"), handles.title)
	rules.CanDelete(golem.All[reachPost]())
	policies := handles.policies(t, rules, nil)
	planned, err := BuildRoot(handles.request(t, policies, mutationir.Delete))
	if err != nil {
		t.Fatal(err)
	}
	selection := rootSelection(t, planned)
	policy, _ := policies.Policy(policyir.ModelID(handles.fixture.Post))
	readRow, err := resolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(handles.fixture.Post))
	if err != nil {
		t.Fatal(err)
	}
	read := readFieldCondition(t, policies, policyir.ModelID(handles.fixture.Post), policyir.FieldID(handles.fixture.PostID))
	if !implies(t, selection, read) {
		t.Fatal("selection omitted the selector field reach")
	}
	if implies(t, read, readRow) && !implies(t, readRow, read) {
		return
	}
	t.Fatal("fixture did not make the field reach narrower than the widened read row")
}

func TestSelectorCoveredByFieldScopedReadRuleKeepsSelection(t *testing.T) {
	handles := newWriteReachHandles(t)
	rules := golem.NewRules[reachPost]()
	rules.CanReadFields(handles.author.Eq(uuid(9)), handles.id, handles.title)
	rules.CanDelete(handles.author.Eq(uuid(9)))
	policies := handles.policies(t, rules, nil)
	planned, err := BuildRoot(handles.request(t, policies, mutationir.Delete))
	if err != nil {
		t.Fatalf("selector covered by field-scoped read rule refused: %v", err)
	}
	policy, _ := policies.Policy(policyir.ModelID(handles.fixture.Post))
	write, err := resolve.RowConstraint(policy, policyir.ActionDelete, policyir.ModelID(handles.fixture.Post))
	if err != nil {
		t.Fatal(err)
	}
	assertSelectionIs(t, rootSelection(t, planned), policyir.ModelID(handles.fixture.Post), write, handles.inputs.target.SelectorPredicate())
}

func TestFastPathLeavesProvenSelectionUnchanged(t *testing.T) {
	for _, test := range existingRowOperations {
		t.Run(test.name, func(t *testing.T) {
			handles := newWriteReachHandles(t)
			rules := golem.NewRules[reachPost]()
			rules.CanRead(handles.author.Eq(uuid(9)).Or(handles.title.Eq("public")))
			rules.CanCreate(golem.All[reachPost]())
			grantWrite(rules, test.operation, handles.author.Eq(uuid(9)))
			policies := handles.policies(t, rules, nil)
			planned, err := BuildRoot(handles.request(t, policies, test.operation))
			if err != nil {
				t.Fatal(err)
			}
			policy, _ := policies.Policy(policyir.ModelID(handles.fixture.Post))
			write, err := resolve.RowConstraint(policy, writeAction(test.operation), policyir.ModelID(handles.fixture.Post))
			if err != nil {
				t.Fatal(err)
			}
			assertSelectionIs(t, rootSelection(t, planned), policyir.ModelID(handles.fixture.Post), write, handles.inputs.target.SelectorPredicate())
		})
	}
}

func TestWriteActionRelationHopsRangeOverReadableTargets(t *testing.T) {
	for _, test := range existingRowOperations {
		t.Run(test.name, func(t *testing.T) {
			handles := newWriteReachHandles(t)
			users := golem.NewRules[reachUser]()
			users.CanRead(handles.userName.Contains("a"))
			rules := golem.NewRules[reachPost]()
			rules.CanRead(golem.All[reachPost]())
			grantWrite(rules, test.operation, handles.writer.Is(handles.userName.Contains("b")))
			policies := handles.policies(t, rules, users)
			planned, err := BuildRoot(handles.request(t, policies, test.operation))
			if err != nil {
				t.Fatal(err)
			}
			expected := golem.NewRules[reachPost]()
			expected.CanRead(golem.All[reachPost]())
			grantWrite(expected, test.operation, handles.writer.Is(golem.And(handles.userName.Contains("a"), handles.userName.Contains("b"))))
			expectedPolicy := bindRules(t, handles.fixture, handles.fixture.Post, expected)
			expanded, err := resolve.RowConstraint(expectedPolicy, writeAction(test.operation), policyir.ModelID(handles.fixture.Post))
			if err != nil {
				t.Fatal(err)
			}
			policy, _ := policies.Policy(policyir.ModelID(handles.fixture.Post))
			raw, err := resolve.RowConstraint(policy, writeAction(test.operation), policyir.ModelID(handles.fixture.Post))
			if err != nil {
				t.Fatal(err)
			}
			selection := rootSelection(t, planned)
			if !implies(t, selection, expanded) {
				t.Fatal("write action relation hop was not expanded over the target read reach")
			}
			if implies(t, selection, raw) {
				t.Fatal("selection still carries the raw relation hop")
			}
			if test.operation == mutationir.Update {
				root, _ := planned.Graph().Root()
				post, ok := root.RowPostcondition()
				if !ok || !implies(t, post, expanded) || implies(t, post, raw) {
					t.Fatal("update row postcondition was not expanded over the target read reach")
				}
			}
		})
	}
}

func TestUnprovenPositionCarriesAuthorizationUnproven(t *testing.T) {
	handles := newWriteReachHandles(t)
	model := policyir.ModelID(handles.fixture.Post)
	policy := policyWithRules(t, model, rule(t, model, policyir.ActionDelete, nil, 0))
	_, err := BuildRoot(handles.request(t, policySet(handles.fixture, policy), mutationir.Delete))
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeClassification || failure.Reason != golem.ReasonAuthorizationUnproven {
		t.Fatalf("unproven position error=%#v", err)
	}
}

func assertSelectionIs(t *testing.T, selection policyir.Condition, model policyir.ModelID, conditions ...policyir.Condition) {
	t.Helper()
	want, err := conjoin(model, conditions...)
	if err != nil {
		t.Fatal(err)
	}
	got, err := policyir.CanonicalCondition(selection)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := policyir.CanonicalCondition(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expected) {
		t.Fatal("proven selection gained an extra conjunct")
	}
}
