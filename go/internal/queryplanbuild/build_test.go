package queryplanbuild

import (
	"math"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/analytics"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/eleven-am/golem/go/internal/queryplanreport"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/scoped"
)

type builderUser struct{}
type builderPost struct{}

type builderPolicies map[policyir.ModelID]policyir.Policy

func (policies builderPolicies) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	value, ok := policies[model]
	return value, ok
}

func TestOperationPrimaryPurposeParityComesOnlyFromTypedPlans(t *testing.T) {
	readCases := []struct {
		operation readir.Operation
		want      string
	}{
		{readir.FindUnique, "findUnique"},
		{readir.FindFirst, "findFirst"},
		{readir.FindMany, "findMany"},
		{readir.Count, "count"},
	}
	for _, test := range readCases {
		operation, purpose, ok := readOperation(test.operation)
		if !ok || operation != test.want || purpose != "root" {
			t.Fatalf("read operation %d=(%q,%q,%t)", test.operation, operation, purpose, ok)
		}
	}
	analyticsCases := []struct {
		operation golem.AnalyticsOperation
		want      string
	}{
		{golem.AnalyticsAggregate, "aggregate"},
		{golem.AnalyticsGroupBy, "groupBy"},
		{golem.AnalyticsRelationGroupBy, "relationGroupBy"},
	}
	for _, test := range analyticsCases {
		operation, purpose, ok := analyticsOperation(test.operation)
		if !ok || operation != test.want || purpose != "analytics" {
			t.Fatalf("analytics operation %d=(%q,%q,%t)", test.operation, operation, purpose, ok)
		}
	}
	if operation, purpose, ok := scopedOperation(); !ok || operation != "scoped" || purpose != "scoped" {
		t.Fatalf("scoped operation=(%q,%q,%t)", operation, purpose, ok)
	}
	for _, invalid := range []readir.Operation{0, math.MaxUint8} {
		if _, _, ok := readOperation(invalid); ok {
			t.Fatalf("invalid read operation %d accepted", invalid)
		}
	}
	if _, _, ok := analyticsOperation(0); ok {
		t.Fatal("invalid analytics operation accepted")
	}
}

func TestCorrelatedProviderFactAndDeferredNestedHydrationAreTruthful(t *testing.T) {
	fixture := schematest.New(t)
	root := nestedCorrelatedThenBatchPlan(t, fixture, 5, 2)
	providerRoot := correlatedProviderPlan(t, fixture.Post, fixture.User, fixture.Authorship)
	report, err := BuildRead(ReadInput{
		Provider: policyir.ProviderSQLite, Plan: root, ProviderPlan: providerRoot,
		Registry: fixture.Registry, Capabilities: proof(t, fixture, policyir.ProviderSQLite),
	})
	if err != nil {
		t.Fatal(err)
	}
	statements := report.Statements()
	if len(statements) != 2 || statements[0].Purpose() != "root" || statements[1].Purpose() != "relationBatch" {
		t.Fatalf("statements=%#v", statements)
	}
	providerNode := statements[0].Root()
	if providerNode.Kind() != "correlatedRelation" {
		t.Fatalf("provider root kind=%q", providerNode.Kind())
	}
	if relation, ok := providerNode.RelationID(); !ok || relation != fixture.Authorship {
		t.Fatalf("provider correlated identity=(%x,%t)", relation, ok)
	}
	deferred := statements[1].Root()
	if deferred.Kind() != "deferredBatch" || deferred.Access() != "none" || len(deferred.Children()) != 0 {
		t.Fatalf("deferred branch claimed provider structure: %#v", deferred)
	}
	if model, ok := deferred.ModelID(); !ok || model != fixture.Post {
		t.Fatalf("deferred model=(%x,%t)", model, ok)
	}
	if relation, ok := deferred.RelationID(); !ok || relation != fixture.Authorship {
		t.Fatalf("deferred relation=(%x,%t)", relation, ok)
	}
	if capacity, ok := deferred.BatchCapacity(); !ok || capacity != 2 {
		t.Fatalf("capacity=(%d,%t)", capacity, ok)
	}
	if minimum, _ := deferred.MinimumExecutionStatements(); minimum != 0 {
		t.Fatalf("minimum=%d", minimum)
	}
	if maximum, _ := deferred.MaximumExecutionStatements(); maximum != 3 {
		t.Fatalf("maximum=%d", maximum)
	}
	if report.MinimumExecutionStatements() != 1 || report.MaximumExecutionStatements() != 4 {
		t.Fatalf("report bounds=%d..%d", report.MinimumExecutionStatements(), report.MaximumExecutionStatements())
	}
}

func TestDeferredBatchZeroParentAndUnboundedParentRefusalReturnNoPartialReport(t *testing.T) {
	fixture := schematest.New(t)
	providerRoot := fullScanPlan(t, fixture.User)
	zero := rootBatchPlan(t, fixture, 0, true, 4)
	report, err := BuildRead(ReadInput{
		Provider: policyir.ProviderPostgreSQL, Plan: zero, ProviderPlan: providerRoot,
		Registry: fixture.Registry, Capabilities: proof(t, fixture, policyir.ProviderPostgreSQL),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Statements()) != 2 || report.MaximumExecutionStatements() != 1 {
		t.Fatalf("zero-parent statements/bound=%d/%d", len(report.Statements()), report.MaximumExecutionStatements())
	}
	if maximum, ok := report.Statements()[1].Root().MaximumExecutionStatements(); !ok || maximum != 0 {
		t.Fatalf("zero-parent branch maximum=(%d,%t)", maximum, ok)
	}

	unbounded := rootBatchPlan(t, fixture, 0, false, 4)
	partial, err := BuildRead(ReadInput{
		Provider: policyir.ProviderPostgreSQL, Plan: unbounded, ProviderPlan: providerRoot,
		Registry: fixture.Registry, Capabilities: proof(t, fixture, policyir.ProviderPostgreSQL),
	})
	if code, ok := queryplanreport.CodeOf(err); !ok || code != queryplanreport.CodeUnavailable {
		t.Fatalf("unbounded error=%v code=(%q,%t)", err, code, ok)
	}
	if !reflect.DeepEqual(partial, queryplanreport.Report{}) {
		t.Fatal("unbounded refusal returned a partial report")
	}
}

func TestConfiguredRootLimitUsesSuccessfulRowBoundNotCapPlusOneProbe(t *testing.T) {
	fixture := schematest.NewWithMaxTake(t, 3, 0)
	planned := rootBatchPlan(t, fixture, 0, false, 3)
	if take, ok := planned.Take(); !ok || take != 4 || planned.ResultLimit() != 3 {
		t.Fatalf("fixture take/result limit=(%d,%t)/%d", take, ok, planned.ResultLimit())
	}
	report, err := BuildRead(ReadInput{
		Provider: policyir.ProviderSQLite, Plan: planned, ProviderPlan: fullScanPlan(t, fixture.User),
		Registry: fixture.Registry, Capabilities: proof(t, fixture, policyir.ProviderSQLite),
	})
	if err != nil {
		t.Fatal(err)
	}
	if maximum, ok := report.Statements()[1].Root().MaximumExecutionStatements(); !ok || maximum != 1 {
		t.Fatalf("configured successful-row branch maximum=(%d,%t)", maximum, ok)
	}
}

func TestTypedTraversalRefusesDepthBeyondThirtyTwoWithoutRecursiveFrameCopies(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	planned := alternatingRelationPlan(t, fixture, 32)
	report, err := BuildRead(ReadInput{
		Provider: policyir.ProviderSQLite, Plan: planned, ProviderPlan: fullScanPlan(t, fixture.User),
		Registry: fixture.Registry, Capabilities: proof(t, fixture, policyir.ProviderSQLite),
	})
	if code, ok := queryplanreport.CodeOf(err); !ok || code != queryplanreport.CodeTooComplex {
		t.Fatalf("deep typed graph error=%v code=(%q,%t)", err, code, ok)
	}
	if !reflect.DeepEqual(report, queryplanreport.Report{}) {
		t.Fatal("deep typed graph refusal returned a partial report")
	}
}

func TestTypedTraversalWalksPublicRelationsAndPrivateHydrationsIteratively(t *testing.T) {
	fixture := schematest.New(t)
	users := golem.GeneratedModelDescriptor[builderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[builderUser, string](fixture.UserName)
	posts := golem.GeneratedToMany[builderUser, builderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedToOne[builderPost, builderUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	title := golem.GeneratedTextField[builderPost, string](fixture.PostTitle)

	userRules := golem.NewRules[builderUser]()
	userRules.CanRead(golem.All[builderUser]())
	userRules.CannotReadFields(golem.All[builderUser](), name)
	userRules.CanReadFields(posts.Some(author.Is(name.Eq("opaque-policy-value"))), name)
	policies := builderPolicies{
		policyir.ModelID(fixture.User): bindRules(t, fixture, fixture.User, userRules),
		policyir.ModelID(fixture.Post): allowRules[builderPost](t, fixture, fixture.Post),
	}
	frozen, err := golem.FreezeFindMany(users, golem.Select[builderUser](name, posts.Args(golem.Take[builderPost](1), golem.Select[builderPost](title))))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	limits := readplan.DefaultLimits()
	limits.MaxTake = 3
	limits.MaxRelationFanout = 2
	limits.MaxStatementParameters = 10
	planned, err := readplan.Caller(request, fixture.Registry, policies, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Relations()) != 1 || len(planned.Hydrations()) != 1 || len(planned.Hydrations()[0].Child().Hydrations()) != 1 {
		t.Fatal("fixture did not contain the public and nested private branches under test")
	}
	report, err := BuildRead(ReadInput{
		Provider: policyir.ProviderSQLite, Plan: planned, ProviderPlan: fullScanPlan(t, fixture.User),
		Registry: fixture.Registry, Capabilities: proof(t, fixture, policyir.ProviderSQLite),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"root", "relationBatch", "policyHydration", "policyHydration"}
	statements := report.Statements()
	if len(statements) != len(want) {
		t.Fatalf("statements=%d want=%d", len(statements), len(want))
	}
	for index, statement := range statements {
		if statement.Purpose() != want[index] {
			t.Fatalf("purpose[%d]=%q want=%q", index, statement.Purpose(), want[index])
		}
	}
	if report.MinimumExecutionStatements() != 1 || report.MaximumExecutionStatements() != 4 {
		t.Fatalf("hydration bounds=%d..%d", report.MinimumExecutionStatements(), report.MaximumExecutionStatements())
	}
}

func TestAnalyticsAndScopedTypedPlansOwnOperationRootAndPrimaryPurpose(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	count := golem.GeneratedCountAll[builderPost](fixture.Post)
	frozenAggregate, err := golem.RuntimeFreezeAggregateRequest(golem.GeneratedAggregate(fixture.Post, golem.GeneratedAggregateSelect[builderPost](count)))
	if err != nil {
		t.Fatal(err)
	}
	analyticsPlan, err := analytics.System(frozenAggregate, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	analyticsReport, err := BuildAnalytics(AnalyticsInput{Provider: policyir.ProviderSQLite, Plan: analyticsPlan, ProviderPlan: fullScanPlan(t, fixture.Post)})
	if err != nil {
		t.Fatal(err)
	}
	if analyticsReport.Operation() != "aggregate" || analyticsReport.RootModelID() != fixture.Post || analyticsReport.Statements()[0].Purpose() != "analytics" {
		t.Fatalf("analytics report operation/root/purpose=%q/%x/%q", analyticsReport.Operation(), analyticsReport.RootModelID(), analyticsReport.Statements()[0].Purpose())
	}

	posts := golem.GeneratedScope[builderPost](fixture.Post)
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[builderPost, string](fixture.PostTitle))
	frozenScoped, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Select(title))
	if err != nil {
		t.Fatal(err)
	}
	scopedPlan, err := scoped.System(frozenScoped, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	scopedReport, err := BuildScoped(ScopedInput{Provider: policyir.ProviderPostgreSQL, Plan: scopedPlan, ProviderPlan: fullScanPlan(t, fixture.Post)})
	if err != nil {
		t.Fatal(err)
	}
	if scopedReport.Operation() != "scoped" || scopedReport.RootModelID() != fixture.Post || scopedReport.Statements()[0].Purpose() != "scoped" {
		t.Fatalf("scoped report operation/root/purpose=%q/%x/%q", scopedReport.Operation(), scopedReport.RootModelID(), scopedReport.Statements()[0].Purpose())
	}
}

func TestTypedAssemblyBoundaryContainsNoSQLOrProviderVocabulary(t *testing.T) {
	assertFields := func(value any, want ...string) {
		t.Helper()
		typ := reflect.TypeOf(value)
		got := make([]string, typ.NumField())
		for index := range got {
			got[index] = typ.Field(index).Name
			if typ.Field(index).Type.Kind() == reflect.String || typ.Field(index).Type == reflect.TypeOf([]byte(nil)) {
				t.Fatalf("%s retains raw diagnostic vocabulary in %s", typ, typ.Field(index).Name)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s fields=%v want=%v", typ, got, want)
		}
	}
	assertFields(ReadInput{}, "Provider", "Plan", "ProviderPlan", "Registry", "Capabilities")
	assertFields(AnalyticsInput{}, "Provider", "Plan", "ProviderPlan")
	assertFields(ScopedInput{}, "Provider", "Plan", "ProviderPlan")
}

func nestedCorrelatedThenBatchPlan(t *testing.T, fixture schematest.Fixture, rootTake, parameterLimit int) readplan.Plan {
	t.Helper()
	postTitle, _ := readir.NewScalarSelection(policyir.FieldID(fixture.PostTitle))
	postRequest, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.Post), Projection: readir.ProjectionSelect, Selection: []readir.Selection{postTitle}})
	if err != nil {
		t.Fatal(err)
	}
	posts, err := readir.NewRelationSelection(policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship), policyir.ModelID(fixture.Post), postRequest)
	if err != nil {
		t.Fatal(err)
	}
	userName, _ := readir.NewScalarSelection(policyir.FieldID(fixture.UserName))
	userRequest, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.User), Projection: readir.ProjectionSelect, Selection: []readir.Selection{userName, posts}})
	if err != nil {
		t.Fatal(err)
	}
	author, err := readir.NewRelationSelection(policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship), policyir.ModelID(fixture.User), userRequest)
	if err != nil {
		t.Fatal(err)
	}
	take := rootTake
	postID, _ := readir.NewScalarSelection(policyir.FieldID(fixture.PostID))
	rootRequest, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.Post), Take: &take, Projection: readir.ProjectionSelect, Selection: []readir.Selection{postID, author}})
	if err != nil {
		t.Fatal(err)
	}
	limits := readplan.DefaultLimits()
	limits.MaxStatementParameters = parameterLimit
	planned, err := readplan.System(rootRequest, fixture.Registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

func rootBatchPlan(t *testing.T, fixture schematest.Fixture, take int, hasTake bool, parameterLimit int) readplan.Plan {
	t.Helper()
	postTitle, _ := readir.NewScalarSelection(policyir.FieldID(fixture.PostTitle))
	child, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.Post), Projection: readir.ProjectionSelect, Selection: []readir.Selection{postTitle}})
	if err != nil {
		t.Fatal(err)
	}
	posts, err := readir.NewRelationSelection(policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship), policyir.ModelID(fixture.Post), child)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := readir.NewScalarSelection(policyir.FieldID(fixture.UserName))
	input := readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.User), Projection: readir.ProjectionSelect, Selection: []readir.Selection{name, posts}}
	if hasTake {
		input.Take = &take
	}
	request, err := readir.NewRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	limits := readplan.DefaultLimits()
	limits.MaxStatementParameters = parameterLimit
	planned, err := readplan.System(request, fixture.Registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

func alternatingRelationPlan(t *testing.T, fixture schematest.Fixture, edges int) readplan.Plan {
	t.Helper()
	if edges < 1 || edges%2 != 0 {
		t.Fatal("test graph requires a positive even edge count rooted at User")
	}
	leaf, err := readir.NewScalarSelection(policyir.FieldID(fixture.UserName))
	if err != nil {
		t.Fatal(err)
	}
	current, err := readir.NewRequest(readir.RequestInput{
		Operation: readir.FindMany, Model: policyir.ModelID(fixture.User), Projection: readir.ProjectionSelect,
		Selection: []readir.Selection{leaf},
	})
	if err != nil {
		t.Fatal(err)
	}
	take := 1
	for level := edges - 1; level >= 0; level-- {
		parentUser := level%2 == 0
		var parentModel, targetModel golem.ModelID
		var scalarField, relationField golem.FieldID
		if parentUser {
			parentModel, targetModel = fixture.User, fixture.Post
			scalarField, relationField = fixture.UserName, fixture.UserPosts
		} else {
			parentModel, targetModel = fixture.Post, fixture.User
			scalarField, relationField = fixture.PostTitle, fixture.PostAuthor
		}
		scalar, scalarErr := readir.NewScalarSelection(policyir.FieldID(scalarField))
		if scalarErr != nil {
			t.Fatal(scalarErr)
		}
		relation, relationErr := readir.NewRelationSelection(policyir.FieldID(relationField), policyir.RelationID(fixture.Authorship), policyir.ModelID(targetModel), current)
		if relationErr != nil {
			t.Fatal(relationErr)
		}
		current, err = readir.NewRequest(readir.RequestInput{
			Operation: readir.FindMany, Model: policyir.ModelID(parentModel), Take: &take,
			Projection: readir.ProjectionSelect, Selection: []readir.Selection{scalar, relation},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	limits := readplan.DefaultLimits()
	limits.MaxRelationDepth = edges
	limits.MaxRelationFanout = 1
	planned, err := readplan.System(current, fixture.Registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

func proof(t *testing.T, fixture schematest.Fixture, provider policyir.Provider) policysql.CapabilityProof {
	t.Helper()
	value, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	return value
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
	result, err := normalize.Policy(bound)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func allowRules[M any](t *testing.T, fixture schematest.Fixture, model golem.ModelID) policyir.Policy {
	t.Helper()
	rules := golem.NewRules[M]()
	rules.CanRead(golem.All[M]())
	return bindRules(t, fixture, model, rules)
}

func fullScanPlan(t *testing.T, model golem.ModelID) queryplancapture.Plan {
	t.Helper()
	identity, ok := queryplancapture.PhysicalIdentity(model)
	if !ok {
		t.Fatal("physical identity")
	}
	plan, err := queryplancapture.NewPlan(queryplancapture.Access(queryplancapture.AccessFullScan, identity, queryplancapture.IndexID{}))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func correlatedProviderPlan(t *testing.T, root, target golem.ModelID, relation golem.RelationID) queryplancapture.Plan {
	t.Helper()
	fact, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == "opaque" }, target, relation, nil, queryplancapture.AliasCorrelatedRelation)
	if err != nil {
		t.Fatal(err)
	}
	identity, status := queryplancapture.NewAliasMap(fact).Resolve("opaque")
	if status != queryplancapture.MatchExact {
		t.Fatal("correlated identity")
	}
	rootIdentity, _ := queryplancapture.PhysicalIdentity(root)
	providerRoot := queryplancapture.Structural(queryplancapture.NodeCorrelatedRelation, identity,
		queryplancapture.Access(queryplancapture.AccessFullScan, rootIdentity, queryplancapture.IndexID{}))
	plan, err := queryplancapture.NewPlan(providerRoot)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
