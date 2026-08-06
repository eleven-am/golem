package nested

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type nestedUser struct{}
type nestedPost struct{}

func TestEveryNestedWriteExpandsToTypedMutationNodes(t *testing.T) {
	fixture := schematest.New(t)
	mutations := allNestedMutations(t, fixture)
	result, err := Build(Request{Root: systemRoot(t, fixture), Mutations: mutations, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 5, MaxRows: 10})
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	nodes := result.Graph().Nodes()
	if len(nodes) != 18 {
		t.Fatalf("nodes=%d want 18", len(nodes))
	}
	for index, node := range nodes {
		if node.Ordinal() != uint32(index) {
			t.Fatalf("node %d ordinal=%d", index, node.Ordinal())
		}
	}
	want := map[mutationir.Operation]bool{mutationir.Create: true, mutationir.CreateMany: true, mutationir.Connect: true, mutationir.ConnectOrCreate: true, mutationir.Disconnect: true, mutationir.SetRelation: true, mutationir.Update: true, mutationir.UpdateMany: true, mutationir.Upsert: true, mutationir.Delete: true, mutationir.DeleteMany: true}
	for _, node := range nodes[1:] {
		delete(want, node.Operation())
	}
	if len(want) != 0 {
		t.Fatalf("operations missing from graph: %#v", want)
	}
	for _, node := range nodes[1:] {
		if position, ok := node.RelationPosition(); ok && position.Kind() == mutationir.PositionRelatedPredicate {
			expansion, present := position.Expansion()
			if !present || expansion.MaxRows() != 10 {
				t.Fatal("dynamic batch expansion is not explicitly bounded")
			}
		}
	}
}

func TestM3NestedWritePositionsAreClassifiedBeforeTransaction(t *testing.T) {
	testNestedMutationClassificationRecursesEveryAcceptedOperation(t)
}

func TestNestedMutationClassificationRecursesEveryAcceptedOperation(t *testing.T) {
	testNestedMutationClassificationRecursesEveryAcceptedOperation(t)
}

func testNestedMutationClassificationRecursesEveryAcceptedOperation(t *testing.T) {
	t.Helper()
	fixture := schematest.New(t)
	policies := allowPolicies(t, fixture)
	spy := &classificationSpy{refuseAt: 4}
	_, err := Build(Request{Root: systemRoot(t, fixture), Mutations: allNestedMutations(t, fixture), Stance: mutationir.Caller, Registry: fixture.Registry, Policies: policies, Classifier: spy, MaxDepth: 5, MaxRows: 10})
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeClassification || spy.calls != 4 {
		t.Fatalf("classification fail-closed err=%v calls=%d", err, spy.calls)
	}
	spy = &classificationSpy{}
	result, err := Build(Request{Root: callerRoot(t, fixture), Mutations: allNestedMutations(t, fixture), Stance: mutationir.Caller, Registry: fixture.Registry, Policies: policies, Classifier: spy, MaxDepth: 5, MaxRows: 10})
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	if spy.calls != 9 || len(result.PositionAudits()) != 9 {
		t.Fatalf("classified calls/audits=%d/%d want 9", spy.calls, len(result.PositionAudits()))
	}
	providers, _ := policyir.NewProviderSet(policyir.ProviderSQLite, policyir.ProviderPostgreSQL)
	requirement, _ := mutationir.NewProviderRequirement(providers, mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(99, 10)
	image, _ := mutationir.NewImageRequirements(policyir.ModelID(fixture.User), nil, nil)
	if _, err := mutationir.NewPlan(mutationir.PlanInput{Stance: mutationir.Caller, Graph: result.Graph(), Result: image, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds}); err != nil {
		t.Fatalf("nested graph is not a complete caller plan graph: %v", err)
	}
}

func TestRelationTraversingPositionClassifiesEveryModelBoundary(t *testing.T) {
	fixture := schematest.New(t)
	policies := allowPolicies(t, fixture)
	author := golem.GeneratedToOne[nestedPost, nestedUser](fixture.PostAuthor, fixture.Authorship)
	name := golem.GeneratedTextField[nestedUser, string](fixture.UserName)
	predicate := author.Is(name.Eq("Ada"))
	mutations := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedDeleteMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, predicate),
	))
	spy := &classificationSpy{}
	result, err := Build(Request{Root: callerRoot(t, fixture), Mutations: mutations, Stance: mutationir.Caller, Registry: fixture.Registry, Policies: policies, Classifier: spy, MaxDepth: 3, MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	audits := result.PositionAudits()
	if spy.calls != 2 || len(audits) != 2 {
		t.Fatalf("classification calls/audits=%d/%d want 2", spy.calls, len(audits))
	}
	if audits[0].ModelID() != policyir.ModelID(fixture.Post) || audits[0].Fields()[0] != policyir.FieldID(fixture.PostAuthor) {
		t.Fatal("relation-bearing Post position was not classified first")
	}
	if audits[1].ModelID() != policyir.ModelID(fixture.User) || audits[1].Fields()[0] != policyir.FieldID(fixture.UserName) {
		t.Fatal("related User predicate was not independently classified")
	}
}

func TestNestedRelationOwnershipAndRequiredToOneAreExact(t *testing.T) {
	fixture := schematest.New(t)
	postTarget := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{1})))
	// User.Posts is inverse: the Post target owns author_id.
	inverse := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User, golem.GeneratedNestedConnect[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, postTarget)))
	result, err := Build(Request{Root: systemRoot(t, fixture), Mutations: inverse, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.Graph().Nodes()[1].ModelID() != policyir.ModelID(fixture.Post) {
		t.Fatal("inverse connect did not authorize the FK-owning Post")
	}
	// Post.Author is source and required; optional-only disconnect/delete are refused.
	bad := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post, golem.GeneratedNestedDisconnectOne[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User)))
	postRoot := rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID)
	if _, err := Build(Request{Root: postRoot, Mutations: bad, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5}); err == nil {
		t.Fatal("required to-one disconnect accepted")
	}
}

func TestCompositeCorrelationOverlapIsRejectedAsOneOwnedTuple(t *testing.T) {
	fixture := schematest.NewCompositeRelation(t)
	tenant := golem.GeneratedUniqueSelectorValue[nestedTenant](fixture.Tenant, fixture.TenantKey,
		golem.GeneratedSelectorComponent(fixture.TenantRegion, golem.NewUUID([16]byte{1})),
		golem.GeneratedSelectorComponent(fixture.TenantID, golem.NewUUID([16]byte{2})))
	input := golem.GeneratedUpdateInput[nestedItem](fixture.Item,
		golem.GeneratedSetFieldValue(fixture.Item, golem.GeneratedEqualField[nestedItem, golem.UUID](fixture.OwnerRegion), golem.NewUUID([16]byte{3})),
		golem.GeneratedNestedConnect[nestedItem, nestedTenant](fixture.Item, fixture.ItemOwner, fixture.Ownership, fixture.Tenant, tenant))
	frozen, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	scalar, err := mutationbind.UpdateInput(frozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	item := golem.GeneratedUniqueSelectorValue[nestedItem](fixture.Item, fixture.ItemKey,
		golem.GeneratedSelectorComponent(fixture.ItemRegion, golem.NewUUID([16]byte{4})),
		golem.GeneratedSelectorComponent(fixture.ItemID, golem.NewUUID([16]byte{5})))
	publicTarget, err := golem.RuntimeFreezeMutationTarget(item)
	if err != nil {
		t.Fatal(err)
	}
	boundTarget, err := mutationbind.Target(publicTarget, fixture.Item, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	target := boundTarget.Target()
	root := mutationir.NodeInput{Operation: mutationir.Update, Model: policyir.ModelID(fixture.Item), Target: &target, ScalarOperations: scalar.Operations(), Identity: mutationir.IdentityUnchanged}
	_, err = Build(Request{Root: root, Mutations: frozen.Relations(), Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeShape || failure.Field != fixture.ItemOwner {
		t.Fatalf("composite correlation overlap err=%#v raw=%v", failure, err)
	}
}

func TestToOneDeleteCapabilitiesFollowCorrelationOwnershipDirection(t *testing.T) {
	t.Run("required source refuses", func(t *testing.T) {
		fixture := schematest.New(t)
		input := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post,
			golem.GeneratedNestedDelete[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, nil)))
		root := rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID)
		if _, err := Build(Request{Root: root, Mutations: input, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5}); err == nil {
			t.Fatal("required source to-one Delete was accepted")
		}
	})
	t.Run("optional source disconnects owner before target delete", func(t *testing.T) {
		fixture := schematest.NewSubscribedIndexedOptionalSource(t)
		input := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post,
			golem.GeneratedNestedDelete[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, nil)))
		root := rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID)
		result, err := Build(Request{Root: root, Mutations: input, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
		if err != nil {
			t.Fatal(err)
		}
		nodes := result.Graph().Nodes()
		if len(nodes) != 3 || nodes[1].Operation() != mutationir.Disconnect || nodes[1].ModelID() != policyir.ModelID(fixture.Post) || nodes[2].Operation() != mutationir.Delete || nodes[2].ModelID() != policyir.ModelID(fixture.User) {
			t.Fatalf("optional source delete graph=%#v", nodes)
		}
	})
	t.Run("required inverse deletes owning child directly", func(t *testing.T) {
		fixture := schematest.NewSubscribedIndexedInverseRequiredHasOne(t)
		input := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
			golem.GeneratedNestedDelete[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, nil)))
		root := rootForModel(t, fixture, fixture.User, fixture.UserKey, fixture.UserID)
		result, err := Build(Request{Root: root, Mutations: input, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
		if err != nil {
			t.Fatal(err)
		}
		nodes := result.Graph().Nodes()
		if len(nodes) != 2 || nodes[1].Operation() != mutationir.Delete || nodes[1].ModelID() != policyir.ModelID(fixture.Post) {
			t.Fatalf("required inverse delete graph=%#v", nodes)
		}
	})
}

func TestDuplicateExplicitToOneRelationValuesRefuseBeforePlanning(t *testing.T) {
	t.Run("source connect plus connect", func(t *testing.T) {
		fixture := schematest.New(t)
		first := golem.GeneratedUniqueSelectorValue[nestedUser](fixture.User, fixture.UserKey,
			golem.GeneratedSelectorComponent(fixture.UserID, golem.NewUUID([16]byte{15: 1})))
		second := golem.GeneratedUniqueSelectorValue[nestedUser](fixture.User, fixture.UserKey,
			golem.GeneratedSelectorComponent(fixture.UserID, golem.NewUUID([16]byte{15: 2})))
		input := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post,
			golem.GeneratedNestedConnect[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, first),
			golem.GeneratedNestedConnect[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, second)))
		root := rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID)
		_, err := Build(Request{Root: root, Mutations: input, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != CodeShape || failure.Field != fixture.PostAuthor {
			t.Fatalf("duplicate source to-one err=%#v raw=%v", failure, err)
		}
	})
	t.Run("inverse has-one create plus create", func(t *testing.T) {
		fixture := schematest.NewSubscribedIndexedInverseRequiredHasOne(t)
		post := func(id byte) golem.CreateInput[nestedPost] {
			return golem.GeneratedCreateInput[nestedPost](fixture.Post,
				golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[nestedPost, golem.UUID](fixture.PostID), golem.NewUUID([16]byte{15: id})),
				golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedTextField[nestedPost, string](fixture.PostTitle), "duplicate"))
		}
		input := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User,
			golem.GeneratedNestedCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, post(1)),
			golem.GeneratedNestedCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, post(2))))
		root := rootForModel(t, fixture, fixture.User, fixture.UserKey, fixture.UserID)
		_, err := Build(Request{Root: root, Mutations: input, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != CodeShape || failure.Field != fixture.UserPosts {
			t.Fatalf("duplicate inverse has-one err=%#v raw=%v", failure, err)
		}
	})
}

func TestSourceOwnedConnectOrCreateHasConditionalAuthorizedOwnerEffects(t *testing.T) {
	fixture := schematest.New(t)
	userTarget := golem.GeneratedUniqueSelectorValue[nestedUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, golem.NewUUID([16]byte{2})))
	create := golem.GeneratedCreateInput[nestedUser](fixture.User,
		golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedEqualField[nestedUser, golem.UUID](fixture.UserID), golem.NewUUID([16]byte{3})),
		golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedTextField[nestedUser, string](fixture.UserName), "created"),
	)
	mutations := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post,
		golem.GeneratedNestedConnectOrCreate[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, userTarget, create),
	))
	postRoot := rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID)
	rootRow, _ := policyir.NewConstant(policyir.ModelID(fixture.Post), true)
	rootSelection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, rootRow)
	postRoot.Selection, postRoot.RowPostcondition = &rootSelection, &rootRow
	policies := allowPolicies(t, fixture)
	result, err := Build(Request{Root: postRoot, Mutations: mutations, Stance: mutationir.Caller, Registry: fixture.Registry, Policies: policies, MaxDepth: 3, MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	nodes := result.Graph().Nodes()
	if len(nodes) != 6 || nodes[1].Operation() != mutationir.ConnectOrCreate || nodes[2].Operation() != mutationir.BranchProbe || nodes[4].Operation() != mutationir.Create {
		t.Fatalf("unexpected conditional owner graph operations: %#v", []mutationir.Operation{nodes[1].Operation(), nodes[2].Operation(), nodes[4].Operation()})
	}
	for _, ordinal := range []int{3, 5} {
		effect := nodes[ordinal]
		anchor, anchored := effect.RelationAnchorOrdinal()
		position, positioned := effect.RelationPosition()
		if effect.Operation() != mutationir.Connect || effect.ModelID() != policyir.ModelID(fixture.Post) || !anchored || anchor != 0 || !positioned || position.Kind() != mutationir.PositionBranchResult {
			t.Fatalf("branch owner effect %d is not anchored to the Post root", ordinal)
		}
		if len(effect.FieldAuthorizations()) != 1 || effect.FieldAuthorizations()[0].FieldID() != policyir.FieldID(fixture.AuthorID) {
			t.Fatalf("branch owner effect %d lacks author_id authorization", ordinal)
		}
	}
	providers, _ := policyir.NewProviderSet(policyir.ProviderSQLite)
	requirement, _ := mutationir.NewProviderRequirement(providers, mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(99, 5)
	image, _ := mutationir.NewImageRequirements(policyir.ModelID(fixture.Post), nil, nil)
	if _, err := mutationir.NewPlan(mutationir.PlanInput{Stance: mutationir.Caller, Graph: result.Graph(), Result: image, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds}); err != nil {
		t.Fatalf("source connect-or-create graph is not a complete caller plan: %v", err)
	}
}

func TestNestedDepthBoundAndEmptySetRemainExplicit(t *testing.T) {
	fixture := schematest.New(t)
	emptySet := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User, golem.GeneratedNestedSet[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post)))
	result, err := Build(Request{Root: systemRoot(t, fixture), Mutations: emptySet, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 1, MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	position, ok := result.Graph().Nodes()[1].RelationPosition()
	if !ok || position.Kind() != mutationir.PositionSetDifference || len(position.DesiredTargets()) != 0 {
		t.Fatal("empty set became wildcard/absent position")
	}
	recursive := golem.GeneratedCreateInput[nestedPost](fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[nestedPost, golem.UUID](fixture.PostID), golem.NewUUID([16]byte{1})),
		golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedTextField[nestedPost, string](fixture.PostTitle), "nested"),
		golem.GeneratedNestedCreate[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, golem.GeneratedCreateInput[nestedUser](fixture.User,
			golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedEqualField[nestedUser, golem.UUID](fixture.UserID), golem.NewUUID([16]byte{2})),
			golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedTextField[nestedUser, string](fixture.UserName), "u"),
		)),
	)
	tooDeep := freezeRelations(t, golem.GeneratedUpdateInput[nestedUser](fixture.User, golem.GeneratedNestedCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, recursive)))
	if _, err := Build(Request{Root: systemRoot(t, fixture), Mutations: tooDeep, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 1, MaxRows: 5}); err == nil {
		t.Fatal("recursive relation exceeded depth without refusal")
	}
}

type classificationSpy struct{ calls, refuseAt int }

func (spy *classificationSpy) Fields(request classify.Request) (classify.Plan, error) {
	spy.calls++
	if spy.refuseAt != 0 && spy.calls == spy.refuseAt {
		return classify.Plan{}, errors.New("injected nested classification refusal")
	}
	return classify.Fields(request)
}

type testPolicies struct {
	generation golem.SchemaDigest
	values     map[policyir.ModelID]policyir.Policy
}

func (set testPolicies) GenerationDigest() golem.SchemaDigest { return set.generation }
func (set testPolicies) Provider() policyir.Provider          { return policyir.ProviderSQLite }
func (set testPolicies) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	value, ok := set.values[model]
	return value, ok
}

func allowPolicies(t *testing.T, fixture schematest.Fixture) testPolicies {
	values := map[policyir.ModelID]policyir.Policy{}
	for _, model := range []policyir.ModelID{policyir.ModelID(fixture.User), policyir.ModelID(fixture.Post)} {
		var rules []policyir.Rule
		for index, action := range []policyir.Action{policyir.ActionRead, policyir.ActionCreate, policyir.ActionUpdate, policyir.ActionDelete} {
			rule, _ := policyir.NewModelRule(action, policyir.EffectGrant, model, nil, uint32(index))
			rules = append(rules, rule)
		}
		policy, err := policyir.NewPolicy(model, rules)
		if err != nil {
			t.Fatal(err)
		}
		values[model] = policy
	}
	return testPolicies{generation: fixture.Registry.GenerationDigest(), values: values}
}

func allNestedMutations(t *testing.T, fixture schematest.Fixture) []golem.FrozenNestedMutation {
	create := nestedPostCreate(t, fixture)
	update := golem.GeneratedUpdateInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, golem.GeneratedTextField[nestedPost, string](fixture.PostTitle), "updated"))
	updateMany := golem.GeneratedUpdateManyInput[nestedPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, golem.GeneratedTextField[nestedPost, string](fixture.PostTitle), "many"))
	selector := golem.GeneratedUniqueSelectorValue[nestedPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{3})))
	predicate := golem.GeneratedEqualField[nestedPost, golem.UUID](fixture.PostID).Eq(golem.NewUUID([16]byte{3}))
	input := golem.GeneratedUpdateInput[nestedUser](fixture.User,
		golem.GeneratedNestedCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, create),
		golem.GeneratedNestedCreateMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, create, create),
		golem.GeneratedNestedConnect[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector),
		golem.GeneratedNestedConnectOrCreate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, create),
		golem.GeneratedNestedDisconnect[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector),
		golem.GeneratedNestedSet[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector),
		golem.GeneratedNestedUpdate[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, update),
		golem.GeneratedNestedUpdateMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, predicate, updateMany),
		golem.GeneratedNestedUpsert[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector, create, update),
		golem.GeneratedNestedDelete[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, selector),
		golem.GeneratedNestedDeleteMany[nestedUser, nestedPost](fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, predicate),
	)
	return freezeRelations(t, input)
}

func nestedPostCreate(t *testing.T, fixture schematest.Fixture) golem.CreateInput[nestedPost] {
	return golem.GeneratedCreateInput[nestedPost](fixture.Post, golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedEqualField[nestedPost, golem.UUID](fixture.PostID), golem.NewUUID([16]byte{4})), golem.GeneratedCreateFieldValue(fixture.Post, golem.GeneratedTextField[nestedPost, string](fixture.PostTitle), "title"))
}
func freezeRelations[M any](t *testing.T, input golem.UpdateInput[M]) []golem.FrozenNestedMutation {
	t.Helper()
	frozen, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	return frozen.Relations()
}

func systemRoot(t *testing.T, fixture schematest.Fixture) mutationir.NodeInput {
	return rootForModel(t, fixture, fixture.User, fixture.UserKey, fixture.UserID)
}
func rootForModel(t *testing.T, fixture schematest.Fixture, model golem.ModelID, key golem.KeyID, field golem.FieldID) mutationir.NodeInput {
	t.Helper()
	public := golem.GeneratedUniqueSelectorValue[nestedUser](model, key, golem.GeneratedSelectorComponent(field, golem.NewUUID([16]byte{9})))
	frozen, _ := golem.RuntimeFreezeMutationTarget[nestedUser](public)
	bound, err := mutationbind.Target(frozen, model, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	target := bound.Target()
	return mutationir.NodeInput{Operation: mutationir.Update, Model: policyir.ModelID(model), Target: &target, Identity: mutationir.IdentityUnchanged}
}
func callerRoot(t *testing.T, fixture schematest.Fixture) mutationir.NodeInput {
	root := systemRoot(t, fixture)
	row, _ := policyir.NewConstant(root.Model, true)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, row)
	root.Selection, root.RowPostcondition = &selection, &row
	return root
}
