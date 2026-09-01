package plan

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

type testPost struct{}

type testPolicies struct {
	generation golem.SchemaDigest
	provider   policyir.Provider
	values     map[policyir.ModelID]policyir.Policy
}

func (set testPolicies) GenerationDigest() golem.SchemaDigest { return set.generation }
func (set testPolicies) Provider() policyir.Provider          { return set.provider }
func (set testPolicies) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	value, ok := set.values[model]
	return value, ok
}

func TestBuildRootCoversCallerScalarMutationShapes(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	policy := allowAllPolicy(t, policyir.ModelID(fixture.Post))
	policies := policySet(fixture, policy)
	predicate := boundBatchPredicate(t, fixture)

	tests := []struct {
		name      string
		operation mutationir.Operation
		configure func(*RootRequest)
		wantNodes int
	}{
		{name: "create", operation: mutationir.Create, wantNodes: 1, configure: func(request *RootRequest) { request.Create = &inputs.create }},
		{name: "update", operation: mutationir.Update, wantNodes: 1, configure: func(request *RootRequest) { request.Target, request.Update = &inputs.target, &inputs.update }},
		{name: "delete", operation: mutationir.Delete, wantNodes: 1, configure: func(request *RootRequest) { request.Target = &inputs.target }},
		{name: "upsert", operation: mutationir.Upsert, wantNodes: 3, configure: func(request *RootRequest) {
			request.Target, request.Create, request.Update = &inputs.target, &inputs.create, &inputs.update
			request.Retry = mutationir.EngineOwnedUpsertRetry
		}},
		{name: "update many", operation: mutationir.UpdateMany, wantNodes: 1, configure: func(request *RootRequest) { request.Predicate, request.Update = &predicate, &inputs.updateMany }},
		{name: "delete many", operation: mutationir.DeleteMany, wantNodes: 1, configure: func(request *RootRequest) { request.Predicate = &predicate }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := baseRequest(t, fixture, policies, mutationir.Caller, test.operation)
			test.configure(&request)
			planned, err := BuildRoot(request)
			if err != nil {
				t.Fatalf("%v (cause: %v)", err, errors.Unwrap(err))
			}
			nodes := planned.Graph().Nodes()
			if len(nodes) != test.wantNodes || nodes[0].Operation() != test.operation {
				t.Fatalf("nodes=%d root=%d", len(nodes), nodes[0].Operation())
			}
			if err := planned.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSystemPlansOmitPoliciesAndHooksButRetainFactsAndValidation(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	inputs := boundInputs(t, fixture)
	request := baseRequest(t, fixture, nil, mutationir.System, mutationir.Update)
	request.Target, request.Update = &inputs.target, &inputs.update
	request.Hooks.Update = []mutationir.HookPhase{mutationir.BeforeHook, mutationir.AfterCommitHook}

	planned, err := BuildRoot(request)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := planned.Graph().Root()
	if _, ok := root.SelectionRequirement(); ok || len(root.FieldAuthorizations()) != 0 || len(root.Hooks()) != 0 {
		t.Fatal("system plan retained caller policy or hooks")
	}
	if !root.Fact().Enabled() || len(root.BeforeRequirements().Fields()) == 0 || len(root.AfterRequirements().Fields()) == 0 {
		t.Fatal("system plan dropped validation images or fact requirement")
	}
}

func TestDeleteSnapshotInventoryReachesRootBatchAndSystemPlans(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	inputs := boundInputs(t, fixture)
	predicate := boundBatchPredicate(t, fixture)
	policy := allowAllPolicy(t, policyir.ModelID(fixture.Post))
	allStored := []policyir.FieldID{
		policyir.FieldID(fixture.PostID),
		policyir.FieldID(fixture.AuthorID),
		policyir.FieldID(fixture.PostTitle),
	}
	for _, test := range []struct {
		name      string
		stance    mutationir.Stance
		operation mutationir.Operation
		configure func(*RootRequest)
	}{
		{"root-caller", mutationir.Caller, mutationir.Delete, func(request *RootRequest) { request.Target = &inputs.target }},
		{"root-system", mutationir.System, mutationir.Delete, func(request *RootRequest) { request.Target = &inputs.target }},
		{"batch-caller", mutationir.Caller, mutationir.DeleteMany, func(request *RootRequest) { request.Predicate = &predicate }},
		{"batch-system", mutationir.System, mutationir.DeleteMany, func(request *RootRequest) { request.Predicate = &predicate }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var policies PolicySet
			if test.stance == mutationir.Caller {
				policies = policySet(fixture, policy)
			}
			request := baseRequest(t, fixture, policies, test.stance, test.operation)
			test.configure(&request)
			plan, err := BuildRoot(request)
			if err != nil {
				t.Fatal(err)
			}
			root, _ := plan.Graph().Root()
			if root.Fact().DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars || len(root.Fact().PrivateDeleteSnapshot()) != len(allStored) {
				t.Fatalf("delete snapshot was not preserved: state=%d fields=%x", root.Fact().DeleteSnapshotState(), root.Fact().PrivateDeleteSnapshot())
			}
			for _, field := range root.Fact().PrivateDeleteSnapshot() {
				if !containsField(root.BeforeRequirements().Fields(), field) {
					t.Fatalf("before image omitted private snapshot field %x", field)
				}
			}
		})
	}

	request := baseRequest(t, fixture, nil, mutationir.System, mutationir.Delete)
	request.Target = &inputs.target
	plan, err := BuildRoot(request)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := plan.Graph().Root()
	for _, field := range root.Fact().PrivateDeleteSnapshot() {
		if field == policyir.FieldID(fixture.PostAuthor) {
			t.Fatal("relation field entered the private stored-scalar snapshot")
		}
	}
}

// TestSubscribedDeleteAlwaysCarriesTheCompilerInventory holds the consequence
// of deriving capture from the registry: a subscription-enabled delete can no
// longer be planned without its complete stored-scalar snapshot, so an
// unverifiable root delete snapshot is unreachable rather than merely unused.
func TestSubscribedDeleteAlwaysCarriesTheCompilerInventory(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	inputs := boundInputs(t, fixture)
	request := baseRequest(t, fixture, nil, mutationir.System, mutationir.Delete)
	request.Target = &inputs.target
	plan, err := BuildRoot(request)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := plan.Graph().Root()
	if root.Fact().DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars || len(root.Fact().PrivateDeleteSnapshot()) == 0 {
		t.Fatalf("subscribed delete snapshot state=%d fields=%x", root.Fact().DeleteSnapshotState(), root.Fact().PrivateDeleteSnapshot())
	}
}

func TestBuildRootCoversEverySystemScalarMutationShape(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	predicate := boundBatchPredicate(t, fixture)
	tests := []struct {
		operation mutationir.Operation
		configure func(*RootRequest)
	}{
		{mutationir.Create, func(request *RootRequest) { request.Create = &inputs.create }},
		{mutationir.Update, func(request *RootRequest) { request.Target, request.Update = &inputs.target, &inputs.update }},
		{mutationir.Delete, func(request *RootRequest) { request.Target = &inputs.target }},
		{mutationir.Upsert, func(request *RootRequest) {
			request.Target, request.Create, request.Update = &inputs.target, &inputs.create, &inputs.update
			request.Retry = mutationir.EngineOwnedUpsertRetry
		}},
		{mutationir.UpdateMany, func(request *RootRequest) { request.Predicate, request.Update = &predicate, &inputs.updateMany }},
		{mutationir.DeleteMany, func(request *RootRequest) { request.Predicate = &predicate }},
	}
	for _, test := range tests {
		request := baseRequest(t, fixture, nil, mutationir.System, test.operation)
		test.configure(&request)
		planned, err := BuildRoot(request)
		if err != nil {
			t.Fatalf("operation=%d: %v (cause: %v)", test.operation, err, errors.Unwrap(err))
		}
		for _, node := range planned.Graph().Nodes() {
			if _, ok := node.SelectionRequirement(); ok || len(node.FieldAuthorizations()) != 0 || len(node.Hooks()) != 0 {
				t.Fatalf("operation=%d retained caller requirements", test.operation)
			}
		}
	}
}

func TestWriteDischargeUsesCanonicalConstraintIdentity(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	model, field := policyir.ModelID(fixture.Post), policyir.FieldID(fixture.AuthorID)
	// Construct equivalent read and write conditions independently. They have
	// distinct Go object provenance but one canonical policy identity.
	readReach := uuidEqual(t, model, field, [16]byte{9})
	writeReach := uuidEqual(t, model, field, [16]byte{9})
	if &readReach == &writeReach {
		t.Fatal("test did not create independent condition values")
	}
	policy := mutationPolicy(t, model, &readReach, &writeReach)
	request := baseRequest(t, fixture, policySet(fixture, policy), mutationir.Caller, mutationir.Update)
	request.Target, request.Update = &inputs.target, &inputs.update
	if _, err := BuildRoot(request); err != nil {
		t.Fatalf("canonically identical independently-built constraints were refused: %v", err)
	}
}

func TestWriteDischargeRefusesWriteWiderThanReadAndAcceptsNarrower(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	mine := uuidEqual(t, policyir.ModelID(fixture.Post), policyir.FieldID(fixture.AuthorID), [16]byte{9})

	wide := mutationPolicy(t, policyir.ModelID(fixture.Post), &mine, nil)
	request := baseRequest(t, fixture, policySet(fixture, wide), mutationir.Caller, mutationir.Update)
	request.Target, request.Update = &inputs.target, &inputs.update
	if _, err := BuildRoot(request); !hasCode(err, CodeClassification) {
		t.Fatalf("write-wider-than-read error=%#v", err)
	}

	narrow := mutationPolicy(t, policyir.ModelID(fixture.Post), &mine, &mine)
	request.Policies = policySet(fixture, narrow)
	if _, err := BuildRoot(request); err != nil {
		t.Fatalf("write-narrower-than-read rejected: %v", err)
	}
}

type emptyClassifier struct{ calls int }

func (classifier *emptyClassifier) Fields(classify.Request) (classify.Plan, error) {
	classifier.calls++
	return classify.Plan{}, nil
}

type recordingClassifier struct{ requests []classify.Request }

func (classifier *recordingClassifier) Fields(request classify.Request) (classify.Plan, error) {
	classifier.requests = append(classifier.requests, request)
	return classify.Fields(request)
}

func TestMutationClassificationCoversEveryValueInfluencingPositionBeforeTransaction(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	predicate := boundBatchPredicate(t, fixture)
	policies := policySet(fixture, allowAllPolicy(t, policyir.ModelID(fixture.Post)))

	tests := []struct {
		name      string
		operation mutationir.Operation
		action    policyir.Action
		field     policyir.FieldID
		calls     int
		configure func(*RootRequest)
	}{
		{name: "update selector", operation: mutationir.Update, action: policyir.ActionUpdate, field: policyir.FieldID(fixture.PostID), calls: 1, configure: func(request *RootRequest) { request.Target, request.Update = &inputs.target, &inputs.update }},
		{name: "delete selector", operation: mutationir.Delete, action: policyir.ActionDelete, field: policyir.FieldID(fixture.PostID), calls: 1, configure: func(request *RootRequest) { request.Target = &inputs.target }},
		{name: "upsert selector", operation: mutationir.Upsert, action: policyir.ActionUpdate, field: policyir.FieldID(fixture.PostID), calls: 2, configure: func(request *RootRequest) {
			request.Target, request.Create, request.Update, request.Retry = &inputs.target, &inputs.create, &inputs.update, mutationir.EngineOwnedUpsertRetry
		}},
		{name: "update-many filter", operation: mutationir.UpdateMany, action: policyir.ActionUpdate, field: policyir.FieldID(fixture.PostTitle), calls: 1, configure: func(request *RootRequest) { request.Predicate, request.Update = &predicate, &inputs.updateMany }},
		{name: "delete-many filter", operation: mutationir.DeleteMany, action: policyir.ActionDelete, field: policyir.FieldID(fixture.PostTitle), calls: 1, configure: func(request *RootRequest) { request.Predicate = &predicate }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spy := &recordingClassifier{}
			request := baseRequest(t, fixture, policies, mutationir.Caller, test.operation)
			request.Classifier = spy
			test.configure(&request)
			if _, err := BuildRoot(request); err != nil {
				t.Fatal(err)
			}
			if len(spy.requests) != test.calls {
				t.Fatalf("classification calls=%d want=%d", len(spy.requests), test.calls)
			}
			for _, classified := range spy.requests {
				if classified.SelectingAction() != test.action || classified.UseKind() != classify.UseSelector && classified.UseKind() != classify.UseFilter || !containsField(classified.Fields(), test.field) {
					t.Fatalf("classification action=%d use=%d fields=%x", classified.SelectingAction(), classified.UseKind(), classified.Fields())
				}
			}
		})
	}
}

func TestMutationClassifierFailsClosedForUnknownPositions(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	spy := &emptyClassifier{}
	request := baseRequest(t, fixture, policySet(fixture, allowAllPolicy(t, policyir.ModelID(fixture.Post))), mutationir.Caller, mutationir.Update)
	request.Target, request.Update, request.Classifier = &inputs.target, &inputs.update, spy
	_, err := BuildRoot(request)
	if !hasCode(err, CodeClassification) || spy.calls != 1 {
		t.Fatalf("error=%#v classifier calls=%d", err, spy.calls)
	}
}

func TestRefusedMutationClassifiesBeforeTransactionCompilationOrProbe(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	spy := &emptyClassifier{}
	boundaryCalls := 0
	request := baseRequest(t, fixture, policySet(fixture, allowAllPolicy(t, policyir.ModelID(fixture.Post))), mutationir.Caller, mutationir.Upsert)
	request.Target, request.Create, request.Update = &inputs.target, &inputs.create, &inputs.update
	request.Retry, request.Classifier = mutationir.EngineOwnedUpsertRetry, spy
	if planned, err := BuildRoot(request); err == nil {
		boundaryCalls++
		_ = planned
	}
	if spy.calls != 1 || boundaryCalls != 0 {
		t.Fatalf("classifier=%d boundary=%d", spy.calls, boundaryCalls)
	}
}

func TestSingleMutationUniqueSelectorsAreClassified(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	model := policyir.ModelID(fixture.Post)
	updateOnly := policyWithRules(t, model,
		rule(t, model, policyir.ActionUpdate, nil, 0),
	)
	for _, operation := range []mutationir.Operation{mutationir.Update, mutationir.Delete, mutationir.Upsert} {
		var rules policyir.Policy
		if operation == mutationir.Delete {
			rules = policyWithRules(t, model, rule(t, model, policyir.ActionDelete, nil, 0))
		} else {
			rules = updateOnly
		}
		request := baseRequest(t, fixture, policySet(fixture, rules), mutationir.Caller, operation)
		request.Target = &inputs.target
		switch operation {
		case mutationir.Update:
			request.Update = &inputs.update
		case mutationir.Upsert:
			request.Create, request.Update, request.Retry = &inputs.create, &inputs.update, mutationir.EngineOwnedUpsertRetry
		}
		if _, err := BuildRoot(request); !hasCode(err, CodeClassification) {
			t.Fatalf("operation=%d error=%#v", operation, err)
		}
	}
}

func TestUpsertProbeUsesUpdateConstraintNotReadConstraint(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	model := policyir.ModelID(fixture.Post)
	mine := uuidEqual(t, model, policyir.FieldID(fixture.AuthorID), [16]byte{9})
	policy := policyWithRules(t, model,
		rule(t, model, policyir.ActionRead, nil, 0),
		rule(t, model, policyir.ActionCreate, nil, 1),
		rule(t, model, policyir.ActionUpdate, &mine, 2),
	)
	request := baseRequest(t, fixture, policySet(fixture, policy), mutationir.Caller, mutationir.Upsert)
	request.Target, request.Create, request.Update = &inputs.target, &inputs.create, &inputs.update
	request.Retry = mutationir.EngineOwnedUpsertRetry
	planned, err := BuildRoot(request)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := planned.Graph().Root()
	selection, ok := root.SelectionRequirement()
	if !ok || selection.Action() != policyir.ActionUpdate || !containsField(conditionFields(selection.Constraint()), policyir.FieldID(fixture.AuthorID)) {
		t.Fatal("upsert probe selection omitted update reach")
	}
}

func TestUpsertPlansCreateAndUpdatePoliciesAsTruthfulBranches(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	model := policyir.ModelID(fixture.Post)
	// Existing rows may update even though the missing/create branch has no
	// grant. Planning must not collapse the two branches before the probe.
	policy := policyWithRules(t, model,
		rule(t, model, policyir.ActionRead, nil, 0),
		rule(t, model, policyir.ActionUpdate, nil, 1),
	)
	request := baseRequest(t, fixture, policySet(fixture, policy), mutationir.Caller, mutationir.Upsert)
	request.Target, request.Create, request.Update = &inputs.target, &inputs.create, &inputs.update
	request.Retry = mutationir.EngineOwnedUpsertRetry
	planned, err := BuildRoot(request)
	if err != nil {
		t.Fatal(err)
	}
	nodes := planned.Graph().Nodes()
	if len(nodes) != 3 || nodes[1].Branch() != mutationir.UpsertCreateBranch || nodes[2].Branch() != mutationir.UpsertUpdateBranch {
		t.Fatalf("upsert branches=%#v", nodes)
	}
	createPost, ok := nodes[1].RowPostcondition()
	if truth, constant := createPost.Constant(); !ok || !constant || truth {
		t.Fatal("ungranted create branch did not retain a false postcondition")
	}
}

func TestBatchWhereFieldsAreClassifiedThroughReadLens(t *testing.T) {
	fixture := schematest.New(t)
	inputs := boundInputs(t, fixture)
	predicate := boundBatchPredicate(t, fixture)
	model := policyir.ModelID(fixture.Post)
	for _, operation := range []mutationir.Operation{mutationir.UpdateMany, mutationir.DeleteMany} {
		action := policyir.ActionUpdate
		if operation == mutationir.DeleteMany {
			action = policyir.ActionDelete
		}
		policy := policyWithRules(t, model, rule(t, model, action, nil, 0))
		request := baseRequest(t, fixture, policySet(fixture, policy), mutationir.Caller, operation)
		request.Predicate = &predicate
		if operation == mutationir.UpdateMany {
			request.Update = &inputs.updateMany
		}
		if _, err := BuildRoot(request); !hasCode(err, CodeClassification) {
			t.Fatalf("operation=%d error=%#v", operation, err)
		}
	}
}

func TestPlannerDerivesImagesProvidersRetryLimitsAndFacts(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	inputs := boundInputs(t, fixture)
	request := baseRequest(t, fixture, policySet(fixture, allowAllPolicy(t, policyir.ModelID(fixture.Post))), mutationir.Caller, mutationir.Update)
	request.Target, request.Update = &inputs.target, &inputs.update
	planned, err := BuildRoot(request)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := planned.Graph().Root()
	if !containsField(root.BeforeRequirements().Fields(), policyir.FieldID(fixture.PostTitle)) || !containsField(root.AfterRequirements().Fields(), policyir.FieldID(fixture.PostTitle)) {
		t.Fatal("update before/after image omitted candidate field")
	}
	if planned.Bounds().MaxParameters() != 999 || planned.Bounds().MaxRows() != 1000 || len(planned.ProviderRequirements()) < 3 {
		t.Fatal("bounds or provider requirements were not retained")
	}
	if _, ok := planned.FactCodecRequirement(); !ok || !root.Fact().Enabled() {
		t.Fatal("fact requirements were not retained")
	}
}

type boundSet struct {
	create     mutationbind.ScalarInput
	update     mutationbind.ScalarInput
	updateMany mutationbind.ScalarInput
	target     mutationbind.BoundTarget
}

func boundInputs(t testing.TB, fixture schematest.Fixture) boundSet {
	t.Helper()
	id := golem.GeneratedEqualField[testPost, golem.UUID](fixture.PostID)
	author := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	uuid := golem.NewUUID([16]byte{4})
	createPublic := golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, author, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, title, "created"),
	)
	createFrozen, err := golem.RuntimeFreezeCreateInput(createPublic)
	if err != nil {
		t.Fatal(err)
	}
	create, err := mutationbind.CreateInput(createFrozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	updateFrozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "updated")))
	if err != nil {
		t.Fatal(err)
	}
	update, err := mutationbind.UpdateInput(updateFrozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	updateManyFrozen, err := golem.RuntimeFreezeUpdateManyInput(golem.GeneratedUpdateManyInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "updated")))
	if err != nil {
		t.Fatal(err)
	}
	updateMany, err := mutationbind.UpdateManyInput(updateManyFrozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	selector := golem.GeneratedUniqueSelectorValue[testPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, uuid))
	targetFrozen, err := golem.RuntimeFreezeMutationTarget[testPost](selector)
	if err != nil {
		t.Fatal(err)
	}
	target, err := mutationbind.Target(targetFrozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	return boundSet{create: create, update: update, updateMany: updateMany, target: target}
}

func boundBatchPredicate(t testing.TB, fixture schematest.Fixture) policyir.Condition {
	t.Helper()
	descriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	frozen, err := golem.GeneratedTextField[testPost, string](fixture.PostTitle).Eq("open").Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := mutationbind.BatchPredicate(frozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func baseRequest(t testing.TB, fixture schematest.Fixture, policies PolicySet, stance mutationir.Stance, operation mutationir.Operation) RootRequest {
	t.Helper()
	bounds, err := mutationir.NewStatementBounds(999, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return RootRequest{Stance: stance, Operation: operation, Model: policyir.ModelID(fixture.Post), Registry: fixture.Registry, Policies: policies, Retry: mutationir.NoRetry, Bounds: bounds}
}

func factCodec(t testing.TB, fixture schematest.Fixture) *mutationir.FactCodecRequirement {
	t.Helper()
	codec, err := mutationir.NewFactCodecRequirement(1, "golem.exact.v1", [32]byte(fixture.Registry.GenerationDigest()))
	if err != nil {
		t.Fatal(err)
	}
	return &codec
}

func policySet(fixture schematest.Fixture, policy policyir.Policy) testPolicies {
	return testPolicies{generation: fixture.Registry.GenerationDigest(), provider: policyir.ProviderSQLite, values: map[policyir.ModelID]policyir.Policy{policy.ModelID(): policy}}
}

func allowAllPolicy(t testing.TB, model policyir.ModelID) policyir.Policy {
	t.Helper()
	return policyWithRules(t, model,
		rule(t, model, policyir.ActionRead, nil, 0),
		rule(t, model, policyir.ActionCreate, nil, 1),
		rule(t, model, policyir.ActionUpdate, nil, 2),
		rule(t, model, policyir.ActionDelete, nil, 3),
	)
}

// mutationPolicy makes all read fields conditional on readCondition while the
// update selecting reach is either unconditional or updateCondition.
func mutationPolicy(t testing.TB, model policyir.ModelID, readCondition, updateCondition *policyir.Condition) policyir.Policy {
	t.Helper()
	return policyWithRules(t, model,
		rule(t, model, policyir.ActionRead, readCondition, 0),
		rule(t, model, policyir.ActionCreate, nil, 1),
		rule(t, model, policyir.ActionUpdate, updateCondition, 2),
	)
}

func policyWithRules(t testing.TB, model policyir.ModelID, rules ...policyir.Rule) policyir.Policy {
	t.Helper()
	policy, err := policyir.NewPolicy(model, rules)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func rule(t testing.TB, model policyir.ModelID, action policyir.Action, condition *policyir.Condition, position uint32) policyir.Rule {
	t.Helper()
	value, err := policyir.NewModelRule(action, policyir.EffectGrant, model, condition, position)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func uuidEqual(t testing.TB, model policyir.ModelID, field policyir.FieldID, value [16]byte) policyir.Condition {
	t.Helper()
	typ, err := policyir.NewTypeRef(policyir.ValueUUID, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := policyir.OneOperand(policyir.UUIDValue(value))
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(model, field, typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, nil)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func containsField(fields []policyir.FieldID, wanted policyir.FieldID) bool {
	for _, field := range fields {
		if field == wanted {
			return true
		}
	}
	return false
}

func hasCode(err error, code ErrorCode) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Code == code
}
