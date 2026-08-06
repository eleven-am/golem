package nested

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type nestedTenant struct{}
type nestedItem struct{}

func TestNestedSemanticDecorationCoversAllElevenOperations(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	inventory := allHookPhases()
	request := Request{
		Root: systemRoot(t, fixture), Mutations: allNestedMutations(t, fixture), Stance: mutationir.Caller,
		Registry: fixture.Registry, Policies: allowPolicies(t, fixture), MaxDepth: 5, MaxRows: 10,
		HookInventory: func(model policyir.ModelID) mutationplan.HookInventory {
			if model != policyir.ModelID(fixture.Post) {
				return mutationplan.HookInventory{}
			}
			return inventory
		},
	}
	rootRow, _ := policyir.NewConstant(policyir.ModelID(fixture.User), true)
	rootSelection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, rootRow)
	request.Root.Selection, request.Root.RowPostcondition = &rootSelection, &rootRow
	result, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}

	wantVocabulary := map[mutationir.Operation]bool{
		mutationir.Create: true, mutationir.CreateMany: true, mutationir.Connect: true,
		mutationir.ConnectOrCreate: true, mutationir.Disconnect: true, mutationir.SetRelation: true,
		mutationir.Update: true, mutationir.UpdateMany: true, mutationir.Upsert: true,
		mutationir.Delete: true, mutationir.DeleteMany: true,
	}
	retainedSources := 0
	for _, node := range result.Graph().Nodes()[1:] {
		if _, hasSource := node.RuntimeSourceID(); hasSource {
			source, ok := result.HookSource(node)
			if !ok || source.ParentModelID() == (golem.ModelID{}) || source.FieldID() == (golem.FieldID{}) || source.RelationID() == (golem.RelationID{}) {
				t.Fatalf("operation %d has invalid retained hook source", node.Operation())
			}
			if branch, ok := source.Branch(); ok && branch.ModelID() == (golem.ModelID{}) {
				t.Fatalf("operation %d retained a zero branch", node.Operation())
			}
			retainedSources++
		}
		delete(wantVocabulary, node.Operation())
		assertCallerNodeAuthorization(t, node, fixture)
		wantHook, hookOperation := semanticHookOperation(node.Operation())
		if node.ModelID() == policyir.ModelID(fixture.Post) && wantHook {
			hooks := node.Hooks()
			if len(hooks) != 3 {
				t.Fatalf("operation %d hooks=%d want 3", node.Operation(), len(hooks))
			}
			for index, phase := range []mutationir.HookPhase{mutationir.BeforeHook, mutationir.TransactionAfterHook, mutationir.AfterCommitHook} {
				if hooks[index].Phase() != phase || hooks[index].Operation() != hookOperation {
					t.Fatalf("operation %d hook %d is out of phase/family", node.Operation(), index)
				}
			}
		} else if len(node.Hooks()) != 0 {
			t.Fatalf("non-row/container operation %d acquired hooks", node.Operation())
		}

		if node.ModelID() != policyir.ModelID(fixture.Post) || !writesSemanticRow(node.Operation()) {
			continue
		}
		fact := node.Fact()
		if !fact.Enabled() {
			t.Fatalf("subscribed row-changing operation %d has no fact", node.Operation())
		}
		assertFactImages(t, node)
		assertCompleteHookSnapshot(t, node, fixture)
	}
	if retainedSources == 0 {
		t.Fatal("nested compiler retained no typed child hook sources")
	}
	if len(wantVocabulary) != 0 {
		t.Fatalf("semantic coverage omitted nested operations: %#v", wantVocabulary)
	}
}

func assertCallerNodeAuthorization(t *testing.T, node mutationir.Node, fixture schematest.Fixture) {
	t.Helper()
	if node.ModelID() != policyir.ModelID(fixture.Post) || !writesSemanticRow(node.Operation()) {
		return
	}
	_, selected := node.SelectionRequirement()
	_, postcondition := node.RowPostcondition()
	authorizations := node.FieldAuthorizations()
	switch node.Operation() {
	case mutationir.Create:
		if selected || !postcondition || len(authorizations) != 3 {
			t.Fatalf("create authorization selection/post/fields=%t/%t/%d want false/true/3", selected, postcondition, len(authorizations))
		}
	case mutationir.Update, mutationir.UpdateMany:
		if !selected || !postcondition || len(authorizations) != 1 || authorizations[0].FieldID() != policyir.FieldID(fixture.PostTitle) {
			t.Fatalf("update authorization selection/post/fields=%t/%t/%#v", selected, postcondition, authorizations)
		}
	case mutationir.Delete, mutationir.DeleteMany:
		if !selected || postcondition || len(authorizations) != 0 {
			t.Fatalf("delete authorization selection/post/fields=%t/%t/%d", selected, postcondition, len(authorizations))
		}
	case mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
		if !selected || !postcondition || len(authorizations) != 1 || authorizations[0].FieldID() != policyir.FieldID(fixture.AuthorID) {
			t.Fatalf("membership authorization selection/post/fields=%t/%t/%#v", selected, postcondition, authorizations)
		}
	}
}

func TestNestedSystemOmitsPolicyAndHooksButRetainsConfiguredFacts(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	result, err := Build(Request{
		Root: systemRoot(t, fixture), Mutations: allNestedMutations(t, fixture), Stance: mutationir.System,
		Registry: fixture.Registry, MaxDepth: 5, MaxRows: 10,
		HookInventory: func(policyir.ModelID) mutationplan.HookInventory { return allHookPhases() },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range result.Graph().Nodes()[1:] {
		if len(node.Hooks()) != 0 || len(node.FieldAuthorizations()) != 0 {
			t.Fatalf("system node %d retained caller hooks/field policy", node.Ordinal())
		}
		if _, ok := node.SelectionRequirement(); ok {
			t.Fatalf("system node %d retained caller selection policy", node.Ordinal())
		}
		if _, ok := node.RowPostcondition(); ok {
			t.Fatalf("system node %d retained caller row policy", node.Ordinal())
		}
		if node.ModelID() == policyir.ModelID(fixture.Post) && writesSemanticRow(node.Operation()) {
			if !node.Fact().Enabled() {
				t.Fatalf("system subscribed operation %d lost its configured fact", node.Operation())
			}
			assertFactImages(t, node)
		}
	}
}

func TestCompositeMembershipDecoratesEveryOwningFieldAndCompleteHookImage(t *testing.T) {
	fixture := schematest.NewCompositeRelation(t)
	itemTarget := golem.GeneratedUniqueSelectorValue[nestedItem](fixture.Item, fixture.ItemKey,
		golem.GeneratedSelectorComponent(fixture.ItemRegion, golem.NewUUID([16]byte{1})),
		golem.GeneratedSelectorComponent(fixture.ItemID, golem.NewUUID([16]byte{2})),
	)
	input := golem.GeneratedUpdateInput[nestedTenant](fixture.Tenant,
		golem.GeneratedNestedConnect[nestedTenant, nestedItem](fixture.Tenant, fixture.TenantItems, fixture.Ownership, fixture.Item, itemTarget),
	)
	frozen, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	rootTarget := golem.GeneratedUniqueSelectorValue[nestedTenant](fixture.Tenant, fixture.TenantKey,
		golem.GeneratedSelectorComponent(fixture.TenantRegion, golem.NewUUID([16]byte{3})),
		golem.GeneratedSelectorComponent(fixture.TenantID, golem.NewUUID([16]byte{4})),
	)
	publicRoot, err := golem.RuntimeFreezeMutationTarget[nestedTenant](rootTarget)
	if err != nil {
		t.Fatal(err)
	}
	boundRoot, err := mutationbind.Target(publicRoot, fixture.Tenant, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	target := boundRoot.Target()
	truth, _ := policyir.NewConstant(policyir.ModelID(fixture.Tenant), true)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	root := mutationir.NodeInput{Operation: mutationir.Update, Model: policyir.ModelID(fixture.Tenant), Target: &target, Selection: &selection, RowPostcondition: &truth, Identity: mutationir.IdentityUnchanged}
	policies := allowModelPolicies(t, fixture.Registry.GenerationDigest(), policyir.ModelID(fixture.Tenant), policyir.ModelID(fixture.Item))
	result, err := Build(Request{
		Root: root, Mutations: frozen.Relations(), Stance: mutationir.Caller, Registry: fixture.Registry,
		Policies: policies, MaxDepth: 3, MaxRows: 5,
		HookInventory: func(model policyir.ModelID) mutationplan.HookInventory {
			if model == policyir.ModelID(fixture.Item) {
				return allHookPhases()
			}
			return mutationplan.HookInventory{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	node := result.Graph().Nodes()[1]
	if node.Operation() != mutationir.Connect || node.ModelID() != policyir.ModelID(fixture.Item) {
		t.Fatalf("membership owner is not the composite Item: operation=%d model=%x", node.Operation(), node.ModelID())
	}
	authorizations := node.FieldAuthorizations()
	if len(authorizations) != 2 || authorizations[0].FieldID() != policyir.FieldID(fixture.OwnerRegion) || authorizations[1].FieldID() != policyir.FieldID(fixture.OwnerID) {
		t.Fatalf("composite owner authorization is incomplete: %#v", authorizations)
	}
	wantFields := []policyir.FieldID{
		policyir.FieldID(fixture.ItemRegion), policyir.FieldID(fixture.ItemID),
		policyir.FieldID(fixture.OwnerRegion), policyir.FieldID(fixture.OwnerID),
	}
	assertFieldsContain(t, node.BeforeRequirements().Fields(), wantFields...)
	assertFieldsContain(t, node.AfterRequirements().Fields(), wantFields...)
	if hooks := node.Hooks(); len(hooks) != 3 || hooks[0].Operation() != mutationir.HookUpdate {
		t.Fatalf("composite membership update hooks are incomplete: %#v", hooks)
	}
}

func allHookPhases() mutationplan.HookInventory {
	all := []mutationir.HookPhase{mutationir.AfterCommitHook, mutationir.BeforeHook, mutationir.TransactionAfterHook, mutationir.BeforeHook}
	return mutationplan.HookInventory{Create: all, Update: all, Delete: all, UpdateMany: all, DeleteMany: all}
}

func semanticHookOperation(operation mutationir.Operation) (bool, mutationir.HookOperation) {
	switch operation {
	case mutationir.Create:
		return true, mutationir.HookCreate
	case mutationir.Update, mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
		return true, mutationir.HookUpdate
	case mutationir.Delete:
		return true, mutationir.HookDelete
	case mutationir.UpdateMany:
		return true, mutationir.HookUpdateMany
	case mutationir.DeleteMany:
		return true, mutationir.HookDeleteMany
	default:
		return false, 0
	}
}

func writesSemanticRow(operation mutationir.Operation) bool {
	writes, _ := semanticHookOperation(operation)
	return writes
}

func assertFactImages(t *testing.T, node mutationir.Node) {
	t.Helper()
	fact := node.Fact()
	assertFieldsContain(t, node.BeforeRequirements().Fields(), fact.BeforeIdentity()...)
	assertFieldsContain(t, node.AfterRequirements().Fields(), fact.AfterIdentity()...)
}

func assertCompleteHookSnapshot(t *testing.T, node mutationir.Node, fixture schematest.Fixture) {
	t.Helper()
	if len(node.Hooks()) == 0 {
		return
	}
	all := []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)}
	switch node.Operation() {
	case mutationir.Update, mutationir.Delete, mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
		assertFieldsContain(t, node.BeforeRequirements().Fields(), all...)
	}
	switch node.Operation() {
	case mutationir.Create, mutationir.Update, mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
		assertFieldsContain(t, node.AfterRequirements().Fields(), all...)
	}
}

func assertFieldsContain(t *testing.T, got []policyir.FieldID, want ...policyir.FieldID) {
	t.Helper()
	set := make(map[policyir.FieldID]bool, len(got))
	for _, field := range got {
		set[field] = true
	}
	for _, field := range want {
		if !set[field] {
			t.Fatalf("image fields %#v omit %x", got, field)
		}
	}
}

func allowModelPolicies(t *testing.T, generation golem.SchemaDigest, models ...policyir.ModelID) testPolicies {
	t.Helper()
	values := make(map[policyir.ModelID]policyir.Policy, len(models))
	for _, model := range models {
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
	return testPolicies{generation: generation, values: values}
}
