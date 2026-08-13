package analytics

import (
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestAnalyticsStatementPlanMapOwnsEveryProviderVisibleAggregateAlias(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	titleField := golem.GeneratedOrderedField[analyticsRendererPost, string](fixture.PostTitle)
	bigField := golem.GeneratedOrderedField[analyticsRendererPost, int64](fixture.PostBigInt)
	title := golem.GeneratedDimension[analyticsRendererPost](fixture.Post, titleField, true)
	sum := golem.GeneratedMeasure[analyticsRendererPost, int64, golem.ExactInteger](fixture.Post, bigField, golem.AggregateSum)
	request := golem.GeneratedGroupBy(fixture.Post,
		golem.GeneratedGroupDimensions[analyticsRendererPost](title),
		golem.GeneratedGroupMeasures[analyticsRendererPost](sum),
		golem.GeneratedGroupTake[analyticsRendererPost](-2),
	)
	frozen, err := golem.RuntimeFreezeGroupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof := analyticsPlanMapProof(t, fixture.Registry, policyir.ProviderSQLite)
	options := RenderOptions{MaxContributionRows: 8, MaxIntermediateGroups: 7, MaxResultRows: 6}
	first, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.SQL() != second.SQL() || !reflect.DeepEqual(first.Args(), second.Args()) || !reflect.DeepEqual(first.Columns(), second.Columns()) {
		t.Fatal("plan-map registration changed deterministic SQL, binds, or decode columns")
	}
	if !reflect.DeepEqual(first.PlanMap().AliasFacts(), second.PlanMap().AliasFacts()) {
		t.Fatal("analytics plan-map facts are nondeterministic")
	}

	wantAliases := []string{
		"golem_a0",
		"golem_contributions",
		"golem_limited_contributions",
		"golem_grouped",
		"golem_limited_groups",
		"golem_groups",
		"golem_page",
		"golem_visible",
		"golem_guard_groups",
		"golem_guarded",
	}
	wantRoles := map[string]AnalyticsPlanAliasRole{
		"golem_a0":                    AnalyticsPlanAliasPhysicalAccess,
		"golem_contributions":         AnalyticsPlanAliasMaterialize,
		"golem_limited_contributions": AnalyticsPlanAliasStructural,
		"golem_grouped":               AnalyticsPlanAliasAggregate,
		"golem_limited_groups":        AnalyticsPlanAliasAggregate,
		"golem_groups":                AnalyticsPlanAliasMaterialize,
		"golem_page":                  AnalyticsPlanAliasStructural,
		"golem_visible":               AnalyticsPlanAliasStructural,
		"golem_guard_groups":          AnalyticsPlanAliasMaterialize,
		"golem_guarded":               AnalyticsPlanAliasStructural,
	}
	facts := first.PlanMap().AliasFacts()
	if len(facts) != len(wantAliases) {
		t.Fatalf("aggregate alias facts = %#v, want exactly %d provider-visible aliases", facts, len(wantAliases))
	}
	wantFields := []policyir.FieldID{policyir.FieldID(fixture.PostTitle), policyir.FieldID(fixture.PostBigInt)}
	for _, alias := range wantAliases {
		matching := first.PlanMap().MatchingAliasFacts(alias)
		if len(matching) != 1 {
			t.Fatalf("provider-visible alias %q facts = %#v, want one unambiguous fact", alias, matching)
		}
		fields := wantFields
		if alias == "golem_a0" {
			fields = analyticsAuthorizedPlanFieldIDs(planned.AuthorizedRead())
		}
		assertAnalyticsAliasFact(t, matching[0], alias, policyir.ModelID(fixture.Post), policyir.RelationID{}, fields, wantRoles[alias])
	}
	for _, omitted := range []string{"", "posts", "golem_d0", "golem_m0", "golem_c0", "golem_guard_row", "provider_guess"} {
		if matching := first.PlanMap().MatchingAliasFacts(omitted); len(matching) != 0 {
			t.Fatalf("unowned projection/physical/unknown alias %q guessed facts %#v", omitted, matching)
		}
	}
	for _, alias := range wantAliases {
		if !strings.Contains(first.SQL(), `"`+alias+`"`) {
			t.Fatalf("registered alias %q is absent from SQL", alias)
		}
	}
}

func TestAnalyticsRelationAndPolicyAliasesAreTypedAndStatementUnique(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	rootEndpoint, present := fixture.Registry.ForwardToOneRelation(fixture.Post, fixture.Authorship)
	if !present {
		t.Fatal("post-author endpoint is absent")
	}
	targetEndpoint := analyticsInverseEndpoint(t, fixture.Registry, rootEndpoint)
	rootWhere := analyticsRelationCondition(t, rootEndpoint, policyir.RelationToOne)
	targetWhere := analyticsRelationCondition(t, targetEndpoint, policyir.RelationToMany)
	rootRead := analyticsSystemReadPlan(t, fixture.Registry, fixture.Post, rootWhere, fixture.AuthorID)
	targetRead := analyticsSystemReadPlan(t, fixture.Registry, fixture.User, targetWhere, fixture.UserID, fixture.UserName)

	dimension := golem.GeneratedRelationDimension[analyticsRendererPost, string](fixture.Post, "authorName", []golem.RelationID{fixture.Authorship}, fixture.UserName, true)
	count := golem.GeneratedCountAll[analyticsRendererPost](fixture.Post)
	request := golem.GeneratedRelationGroupBy(fixture.Post,
		golem.GeneratedRelationGroupDimensions[analyticsRendererPost](dimension),
		golem.GeneratedRelationGroupMeasures[analyticsRendererPost](count),
	)
	frozen, err := golem.RuntimeFreezeRelationGroupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	terminal, present := fixture.Registry.Field(fixture.User, fixture.UserName)
	if !present {
		t.Fatal("terminal relation field is absent")
	}
	planned := Plan{
		request: frozen,
		read:    rootRead,
		fields:  map[golem.FieldID]schema.Field{fixture.UserName: terminal},
		relation: []RelationHop{{
			Endpoint:   rootEndpoint,
			Authorized: targetRead,
		}},
	}
	proof := analyticsPlanMapProof(t, fixture.Registry, policyir.ProviderSQLite)
	first, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	if first.SQL() != second.SQL() || !reflect.DeepEqual(first.Args(), second.Args()) || !reflect.DeepEqual(first.PlanMap().AliasFacts(), second.PlanMap().AliasFacts()) {
		t.Fatal("relation analytics render or plan map is nondeterministic")
	}

	assertAnalyticsAliasFact(t, onlyAnalyticsAliasFact(t, first.PlanMap(), "golem_a0"), "golem_a0", policyir.ModelID(fixture.Post), policyir.RelationID{}, analyticsAuthorizedPlanFieldIDs(rootRead), AnalyticsPlanAliasPhysicalAccess)
	assertAnalyticsAliasFact(t, onlyAnalyticsAliasFact(t, first.PlanMap(), "golem_j0"), "golem_j0", policyir.ModelID(fixture.User), policyir.RelationID(fixture.Authorship), analyticsAuthorizedPlanFieldIDs(targetRead), AnalyticsPlanAliasPhysicalAccess)
	assertAnalyticsAliasFact(t, onlyAnalyticsAliasFact(t, first.PlanMap(), "golem_p1"), "golem_p1", policyir.ModelID(fixture.User), policyir.RelationID(fixture.Authorship), nil, AnalyticsPlanAliasCorrelatedRelation)
	assertAnalyticsAliasFact(t, onlyAnalyticsAliasFact(t, first.PlanMap(), "golem_p2"), "golem_p2", policyir.ModelID(fixture.Post), policyir.RelationID(fixture.Authorship), nil, AnalyticsPlanAliasCorrelatedRelation)
	if first.PlanMap().MatchingAliasFacts("golem_p1")[0].ModelID() == first.PlanMap().MatchingAliasFacts("golem_p2")[0].ModelID() {
		t.Fatal("independently compiled root and join policies collapsed onto one flat alias identity")
	}
}

func TestAnalyticsPlanAliasFactIsPrivateImmutableAndAmbiguityFailsClosed(t *testing.T) {
	model, relation := policyir.ModelID{1}, policyir.RelationID{2}
	fields := []policyir.FieldID{{3}, {4}}
	fact := newAnalyticsPlanAliasFact("golem_test", model, relation, fields, AnalyticsPlanAliasPhysicalAccess)
	if newAnalyticsPlanAliasFact("golem_test", model, relation, fields, 0).Matches("golem_test") {
		t.Fatal("alias fact with an invalid provenance role matched")
	}
	copyFields := fact.FieldIDs()
	copyFields[0] = policyir.FieldID{99}
	assertAnalyticsAliasFact(t, fact, "golem_test", model, relation, fields, AnalyticsPlanAliasPhysicalAccess)

	typ := reflect.TypeOf(AnalyticsPlanAliasFact{})
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath == "" {
			t.Fatalf("analytics alias fact exposes mutable field %s", field.Name)
		}
		if field.Type == reflect.TypeOf(physical.PhysicalName("")) {
			t.Fatalf("analytics alias fact retains a physical name in %s", field.Name)
		}
	}
	for _, forbidden := range []string{"Alias", "Name", "SQL", "Table", "Column", "Predicate", "Arguments"} {
		if _, ok := typ.MethodByName(forbidden); ok {
			t.Fatalf("analytics alias fact exposes forbidden %s method", forbidden)
		}
	}

	var builder analyticsPlanMapBuilder
	if err := builder.add("golem_same", model, relation, fields, AnalyticsPlanAliasPhysicalAccess); err != nil {
		t.Fatal(err)
	}
	if err := builder.add("golem_same", policyir.ModelID{5}, policyir.RelationID{6}, nil, AnalyticsPlanAliasPhysicalAccess); err == nil {
		t.Fatal("ambiguous provider alias registration was accepted")
	}
	plan := builder.freeze()
	snapshot := plan.AliasFacts()
	snapshot[0] = AnalyticsPlanAliasFact{}
	assertAnalyticsAliasFact(t, plan.AliasFacts()[0], "golem_same", model, relation, fields, AnalyticsPlanAliasPhysicalAccess)
	if (AnalyticsPlanAliasFact{}).Matches("golem_same") || len(plan.MatchingAliasFacts("unknown")) != 0 {
		t.Fatal("zero or unknown analytics alias guessed a stable identity")
	}
}

func analyticsPlanMapProof(t *testing.T, registry *schema.Registry, provider policyir.Provider) policysql.CapabilityProof {
	t.Helper()
	proof, err := policysql.NewCapabilityProof(provider, [32]byte(registry.ModelFingerprint()),
		policyir.CapabilityBinaryText,
		policyir.CapabilityASCIIInsensitiveText,
		policyir.CapabilityExactJSON,
		policyir.CapabilityScalarListJSON,
		policyir.CapabilityRelationCorrelation,
	)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func analyticsSystemReadPlan(t *testing.T, registry *schema.Registry, model golem.ModelID, where policyir.Condition, fields ...golem.FieldID) readplan.Plan {
	t.Helper()
	input := readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(model), Projection: readir.ProjectionSelect, Where: &where}
	for _, field := range fields {
		selection, err := readir.NewScalarSelection(policyir.FieldID(field))
		if err != nil {
			t.Fatal(err)
		}
		input.Selection = append(input.Selection, selection)
	}
	request, err := readir.NewRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.System(request, registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

func analyticsRelationCondition(t *testing.T, endpoint schema.RelationEndpoint, cardinality policyir.RelationCardinality) policyir.Condition {
	t.Helper()
	child, err := policyir.NewConstant(policyir.ModelID(endpoint.TargetModelID()), true)
	if err != nil {
		t.Fatal(err)
	}
	operator := policyir.OperatorRelationIs
	if cardinality == policyir.RelationToMany {
		operator = policyir.OperatorRelationSome
	}
	requirement, err := policyir.NewRequirement(policyir.PortableProviders(), policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewRelation(
		policyir.ModelID(endpoint.ModelID()),
		policyir.FieldID(endpoint.FieldID()),
		policyir.RelationID(endpoint.RelationID()),
		policyir.ModelID(endpoint.TargetModelID()),
		cardinality,
		operator,
		&child,
		[]policyir.Requirement{requirement},
	)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func analyticsInverseEndpoint(t *testing.T, registry *schema.Registry, endpoint schema.RelationEndpoint) schema.RelationEndpoint {
	t.Helper()
	model, present := registry.Model(endpoint.TargetModelID())
	if !present {
		t.Fatal("relation target model is absent")
	}
	for _, field := range model.Fields() {
		candidate, ok := registry.RelationEndpoint(endpoint.TargetModelID(), field, endpoint.RelationID())
		if ok && candidate.TargetModelID() == endpoint.ModelID() {
			return candidate
		}
	}
	t.Fatal("inverse relation endpoint is absent")
	return schema.RelationEndpoint{}
}

func onlyAnalyticsAliasFact(t *testing.T, plan AnalyticsPlanMap, alias string) AnalyticsPlanAliasFact {
	t.Helper()
	facts := plan.MatchingAliasFacts(alias)
	if len(facts) != 1 {
		t.Fatalf("alias %q facts = %#v, want exactly one", alias, facts)
	}
	return facts[0]
}

func assertAnalyticsAliasFact(t *testing.T, fact AnalyticsPlanAliasFact, alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role AnalyticsPlanAliasRole) {
	t.Helper()
	if !fact.Matches(alias) || fact.Matches("") || fact.Matches(alias+"_forged") {
		t.Fatalf("analytics alias matcher did not recognize only %q", alias)
	}
	if fact.ModelID() != model {
		t.Fatalf("model = %x, want %x", fact.ModelID(), model)
	}
	if fact.Role() != role {
		t.Fatalf("role = %d, want %d", fact.Role(), role)
	}
	gotRelation, present := fact.RelationID()
	if present != (relation != (policyir.RelationID{})) || gotRelation != relation {
		t.Fatalf("relation = (%x,%t), want (%x,%t)", gotRelation, present, relation, relation != (policyir.RelationID{}))
	}
	if got := fact.FieldIDs(); !reflect.DeepEqual(got, fields) {
		t.Fatalf("fields = %x, want %x", got, fields)
	}
}
