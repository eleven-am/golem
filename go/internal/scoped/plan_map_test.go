package scoped

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	posts := golem.GeneratedScope[scopedSQLPost](fixture.Post)
	author := golem.InnerJoin(posts, golem.GeneratedToOne[scopedSQLPost, scopedSQLUser](fixture.PostAuthor, fixture.Authorship, fixture.User))
	title := golem.GeneratedScopedTextField(posts, golem.GeneratedTextField[scopedSQLPost, string](fixture.PostTitle))
	name := golem.GeneratedScopedTextField(author, golem.GeneratedTextField[scopedSQLUser, string](fixture.UserName))
	frozen, err := golem.RuntimeFreezeScopedQuery(golem.From(posts).Join(author).Select(title, name))
	if err != nil {
		t.Fatal(err)
	}
	rootEndpoint, present := fixture.Registry.RelationEndpoint(fixture.Post, fixture.PostAuthor, fixture.Authorship)
	if !present {
		t.Fatal("root relation endpoint is absent")
	}
	targetEndpoint := scopedInverseEndpoint(t, fixture.Registry, rootEndpoint)
	policies := policyMap{
		policyir.ModelID(fixture.Post): scopedConditionalFieldRelationPolicy(t, rootEndpoint, fixture.PostTitle),
		policyir.ModelID(fixture.User): scopedRelationPolicy(t, targetEndpoint, policyir.RelationToMany),
	}
	planned, err := Caller(frozen, fixture.Registry, policyir.PortableProviders(), policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := scopedProof(policyir.ProviderSQLite, fixture)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	if first.SQL() != second.SQL() || !reflect.DeepEqual(first.Args(), second.Args()) || !reflect.DeepEqual(first.PlanMap().AliasFacts(), second.PlanMap().AliasFacts()) {
		t.Fatal("scoped render or plan-map facts are nondeterministic")
	}

	assertScopedAliasFact(t, onlyScopedAliasFact(t, first.PlanMap(), "golem_s0"), "golem_s0", policyir.ModelID(fixture.Post), policyir.RelationID{}, scopedAuthorizedFieldIDs(planned.occurrences[0].authorized), ScopedPlanAliasPhysicalAccess)
	assertScopedAliasFact(t, onlyScopedAliasFact(t, first.PlanMap(), "golem_s1"), "golem_s1", policyir.ModelID(fixture.User), policyir.RelationID(fixture.Authorship), scopedAuthorizedFieldIDs(planned.occurrences[1].authorized), ScopedPlanAliasPhysicalAccess)
	assertScopedAliasFact(t, onlyScopedAliasFact(t, first.PlanMap(), "golem_p1"), "golem_p1", policyir.ModelID(fixture.User), policyir.RelationID(fixture.Authorship), nil, ScopedPlanAliasCorrelatedRelation)
	assertScopedAliasFact(t, onlyScopedAliasFact(t, first.PlanMap(), "golem_p2"), "golem_p2", policyir.ModelID(fixture.Post), policyir.RelationID(fixture.Authorship), nil, ScopedPlanAliasCorrelatedRelation)
	if first.PlanMap().MatchingAliasFacts("golem_p1")[0].ModelID() == first.PlanMap().MatchingAliasFacts("golem_p2")[0].ModelID() {
		t.Fatal("independently compiled scoped policies collapsed onto one flat alias identity")
	}
	for _, unknown := range []string{"", "golem_p3", "golem_s9", "posts", "provider_guess"} {
		if facts := first.PlanMap().MatchingAliasFacts(unknown); len(facts) != 0 {
			t.Fatalf("unknown alias %q guessed facts %#v", unknown, facts)
		}
	}
}

func TestScopedPlanMapIsDeepCopiedOpaqueAndRejectsAmbiguousAliases(t *testing.T) {
	model, relation := policyir.ModelID{1}, policyir.RelationID{2}
	fields := []policyir.FieldID{{3}, {4}}
	var builder scopedPlanMapBuilder
	if err := builder.add("golem_s0", model, relation, fields, ScopedPlanAliasPhysicalAccess); err != nil {
		t.Fatal(err)
	}
	if err := builder.add("golem_s0", policyir.ModelID{9}, policyir.RelationID{}, nil, ScopedPlanAliasPhysicalAccess); err == nil {
		t.Fatal("duplicate renderer alias was not rejected as ambiguous")
	}
	if err := builder.add("golem_s1", model, relation, []policyir.FieldID{{}}, ScopedPlanAliasPhysicalAccess); err == nil {
		t.Fatal("zero renderer field identity was not rejected")
	}
	if err := builder.add("golem_s2", model, relation, []policyir.FieldID{{5}, {5}}, ScopedPlanAliasPhysicalAccess); err == nil {
		t.Fatal("duplicate renderer field identity was not rejected as ambiguous")
	}
	plan := builder.freeze()
	facts := plan.AliasFacts()
	facts[0] = ScopedPlanAliasFact{}
	fieldCopy := plan.AliasFacts()[0].FieldIDs()
	fieldCopy[0] = policyir.FieldID{99}
	assertScopedAliasFact(t, plan.AliasFacts()[0], "golem_s0", model, relation, fields, ScopedPlanAliasPhysicalAccess)
	if (ScopedPlanAliasFact{}).Matches("golem_s0") {
		t.Fatal("zero/forged scoped alias fact matched a renderer alias")
	}

	typ := reflect.TypeOf(ScopedPlanAliasFact{})
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath == "" {
			t.Fatalf("scoped alias fact exposes mutable field %s", field.Name)
		}
		if field.Type == reflect.TypeOf(physical.PhysicalName("")) {
			t.Fatalf("scoped alias fact retained a physical name in %s", field.Name)
		}
	}
	for _, forbidden := range []string{"Alias", "Name", "SQL", "Table", "Column", "Predicate", "Arguments"} {
		if _, ok := typ.MethodByName(forbidden); ok {
			t.Fatalf("scoped alias fact exposes forbidden %s method", forbidden)
		}
	}
}

func scopedRelationPolicy(t *testing.T, endpoint schema.RelationEndpoint, cardinality policyir.RelationCardinality) policyir.Policy {
	t.Helper()
	child, err := policyir.NewConstant(policyir.ModelID(endpoint.TargetModelID()), true)
	if err != nil {
		t.Fatal(err)
	}
	operator := policyir.OperatorRelationIs
	if cardinality == policyir.RelationToMany {
		operator = policyir.OperatorRelationSome
	}
	requirements, err := policyoperator.ValidateShape(operator, policyoperator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: cardinality, HasChild: true, Providers: policyir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewRelation(policyir.ModelID(endpoint.ModelID()), policyir.FieldID(endpoint.FieldID()), policyir.RelationID(endpoint.RelationID()), policyir.ModelID(endpoint.TargetModelID()), cardinality, operator, &child, requirements)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(endpoint.ModelID()), &condition, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(endpoint.ModelID()), []policyir.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func scopedConditionalFieldRelationPolicy(t *testing.T, endpoint schema.RelationEndpoint, field golem.FieldID) policyir.Policy {
	t.Helper()
	all, err := policyir.NewConstant(policyir.ModelID(endpoint.ModelID()), true)
	if err != nil {
		t.Fatal(err)
	}
	child, err := policyir.NewConstant(policyir.ModelID(endpoint.TargetModelID()), true)
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := policyoperator.ValidateShape(policyir.OperatorRelationIs, policyoperator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToOne, HasChild: true, Providers: policyir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewRelation(
		policyir.ModelID(endpoint.ModelID()),
		policyir.FieldID(endpoint.FieldID()),
		policyir.RelationID(endpoint.RelationID()),
		policyir.ModelID(endpoint.TargetModelID()),
		policyir.RelationToOne,
		policyir.OperatorRelationIs,
		&child,
		requirements,
	)
	if err != nil {
		t.Fatal(err)
	}
	modelRule, err := policyir.NewModelRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(endpoint.ModelID()), &all, 0)
	if err != nil {
		t.Fatal(err)
	}
	denyRule, err := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectDeny, policyir.ModelID(endpoint.ModelID()), &all, policyir.FieldID(field), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	grantRule, err := policyir.NewFieldRule(policyir.ActionRead, policyir.EffectGrant, policyir.ModelID(endpoint.ModelID()), &condition, policyir.FieldID(field), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policyir.NewPolicy(policyir.ModelID(endpoint.ModelID()), []policyir.Rule{modelRule, denyRule, grantRule})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func scopedInverseEndpoint(t *testing.T, registry *schema.Registry, endpoint schema.RelationEndpoint) schema.RelationEndpoint {
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

func scopedAuthorizedFieldIDs(plan readplan.Plan) []policyir.FieldID {
	fields := plan.Fields()
	result := make([]policyir.FieldID, len(fields))
	for index, field := range fields {
		result[index] = field.FieldID()
	}
	return result
}

func onlyScopedAliasFact(t *testing.T, plan ScopedPlanMap, alias string) ScopedPlanAliasFact {
	t.Helper()
	facts := plan.MatchingAliasFacts(alias)
	if len(facts) != 1 {
		t.Fatalf("alias %q facts = %#v, want exactly one", alias, facts)
	}
	return facts[0]
}

func assertScopedAliasFact(t *testing.T, fact ScopedPlanAliasFact, alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role ScopedPlanAliasRole) {
	t.Helper()
	if !fact.Matches(alias) || fact.Matches("") || fact.Matches(alias+"_forged") {
		t.Fatalf("scoped alias matcher did not recognize only %q", alias)
	}
	if fact.ModelID() != model {
		t.Fatalf("model = %x, want %x", fact.ModelID(), model)
	}
	gotRelation, present := fact.RelationID()
	if present != (relation != (policyir.RelationID{})) || gotRelation != relation {
		t.Fatalf("relation = (%x,%t), want (%x,%t)", gotRelation, present, relation, relation != (policyir.RelationID{}))
	}
	if got := fact.FieldIDs(); !reflect.DeepEqual(got, fields) {
		t.Fatalf("fields = %x, want %x", got, fields)
	}
	if fact.Role() != role {
		t.Fatalf("role = %d, want %d", fact.Role(), role)
	}
}
