package sql

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestOrdinaryReadStatementPlanMapIsExactDeterministicImmutableAndFailsClosed(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	frozen, err := golem.FreezeFindMany(descriptor, golem.Select[renderUser](name))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
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
	firstFacts, secondFacts := first.PlanMap().AliasFacts(), second.PlanMap().AliasFacts()
	if !reflect.DeepEqual(firstFacts, secondFacts) {
		t.Fatalf("plan-map facts are nondeterministic:\n%#v\n%#v", firstFacts, secondFacts)
	}
	if len(firstFacts) != 1 {
		t.Fatalf("ordinary root facts = %#v, want exact root only", firstFacts)
	}
	assertPlanAliasFact(t, firstFacts[0], "golem_r0", policyir.ModelID(fixture.User), policyir.RelationID{}, nil, PlanAliasPhysicalAccess)
	for _, omitted := range []string{"", "golem_c0", "golem_d0", "users", "provider_guess"} {
		if facts := first.PlanMap().MatchingAliasFacts(omitted); len(facts) != 0 {
			t.Fatalf("unowned/unknown alias %q guessed facts %#v", omitted, facts)
		}
	}

	// Both returned collections are caller-owned. Replacing a fact or mutating
	// one of its field slices cannot alter the statement's private map.
	firstFacts[0] = PlanAliasFact{}
	fresh := first.PlanMap().AliasFacts()
	assertPlanAliasFact(t, fresh[0], "golem_r0", policyir.ModelID(fixture.User), policyir.RelationID{}, nil, PlanAliasPhysicalAccess)
	if (PlanAliasFact{}).Matches("golem_r0") {
		t.Fatal("zero/forged plan alias fact matched a renderer alias")
	}
}

func TestOrdinaryReadStatementPlanMapMergesPolicyRelationAliasesWithoutChangingRender(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	title := golem.GeneratedTextField[renderPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[renderUser, renderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	cursorID, err := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	cursor := golem.GeneratedUniqueSelectorValue[renderUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, cursorID))
	frozen, err := golem.FreezeFindMany(descriptor, golem.OrderBy(name.Asc()), golem.Cursor(cursor), golem.Select[renderUser](name))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	rules := golem.NewRules[renderUser]()
	rules.CanRead(posts.Some(title.Contains("visible")))
	frozenPolicy, err := rules.Freeze(fixture.User)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policybind.Policy(frozenPolicy, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	policy, err = normalize.Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	postRules := golem.NewRules[renderPost]()
	postRules.CanRead(golem.All[renderPost]())
	frozenPostPolicy, err := postRules.Freeze(fixture.Post)
	if err != nil {
		t.Fatal(err)
	}
	postPolicy, err := policybind.Policy(frozenPostPolicy, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	postPolicy, err = normalize.Policy(postPolicy)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.Caller(request, fixture.Registry, renderPolicySet{policyir.ModelID(fixture.User): policy, policyir.ModelID(fixture.Post): postPolicy}, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	if statement.SQL() != repeated.SQL() || !reflect.DeepEqual(statement.Args(), repeated.Args()) {
		t.Fatal("statement-scoped alias allocation changed deterministic SQL or binds")
	}
	facts := statement.PlanMap().AliasFacts()
	if len(facts) != 4 {
		t.Fatalf("root, root-policy, cursor, cursor-policy facts = %#v", facts)
	}
	assertPlanAliasFact(t, facts[0], "golem_r0", policyir.ModelID(fixture.User), policyir.RelationID{}, nil, PlanAliasPhysicalAccess)
	assertPlanAliasFact(t, facts[1], "golem_p1", policyir.ModelID(fixture.Post), policyir.RelationID(fixture.Authorship), nil, PlanAliasCorrelatedRelation)
	if !facts[2].Matches("golem_cp0") || facts[2].ModelID() != policyir.ModelID(fixture.User) {
		t.Fatalf("cursor root fact = %#v", facts[2])
	}
	if facts[2].Role() != PlanAliasPhysicalAccess {
		t.Fatalf("cursor root role = %d", facts[2].Role())
	}
	assertPlanAliasFact(t, facts[3], "golem_p2", policyir.ModelID(fixture.Post), policyir.RelationID(fixture.Authorship), nil, PlanAliasCorrelatedRelation)
	if len(statement.PlanMap().MatchingAliasFacts("golem_p1")) != 1 || len(statement.PlanMap().MatchingAliasFacts("golem_p2")) != 1 {
		t.Fatalf("independent policy fragments did not receive statement-unique aliases: %#v", facts)
	}
}

func TestPlanAliasFactRetainsOnlyOpaqueMatcherAndStableSanitizerIdentities(t *testing.T) {
	model, relation := policyir.ModelID{1}, policyir.RelationID{2}
	fields := []policyir.FieldID{{3}, {4}}
	fact := newPlanAliasFact("golem_test", model, relation, fields, PlanAliasCorrelatedRelation)
	copyFields := fact.FieldIDs()
	copyFields[0] = policyir.FieldID{99}
	assertPlanAliasFact(t, fact, "golem_test", model, relation, fields, PlanAliasCorrelatedRelation)

	typ := reflect.TypeOf(PlanAliasFact{})
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath == "" {
			t.Fatalf("plan alias fact exposes mutable field %s", field.Name)
		}
		if field.Type == reflect.TypeOf(physical.PhysicalName("")) {
			t.Fatalf("plan alias fact retained a physical name in %s", field.Name)
		}
	}
	for _, forbidden := range []string{"Alias", "Name", "SQL", "Table", "Column", "Predicate", "Arguments"} {
		if _, ok := typ.MethodByName(forbidden); ok {
			t.Fatalf("plan alias fact exposes forbidden %s method", forbidden)
		}
	}
}

func assertPlanAliasFact(t *testing.T, fact PlanAliasFact, alias string, model policyir.ModelID, relation policyir.RelationID, fields []policyir.FieldID, role PlanAliasRole) {
	t.Helper()
	if !fact.Matches(alias) || fact.Matches("") || fact.Matches(alias+"_forged") {
		t.Fatalf("alias matcher did not recognize only %q", alias)
	}
	if fact.ModelID() != model {
		t.Fatalf("model = %x, want %x", fact.ModelID(), model)
	}
	gotRelation, hasRelation := fact.RelationID()
	if hasRelation != (relation != (policyir.RelationID{})) || gotRelation != relation {
		t.Fatalf("relation = (%x, %t), want (%x, %t)", gotRelation, hasRelation, relation, relation != (policyir.RelationID{}))
	}
	if gotFields := fact.FieldIDs(); !reflect.DeepEqual(gotFields, fields) {
		t.Fatalf("fields = %x, want %x", gotFields, fields)
	}
	if fact.Role() != role {
		t.Fatalf("role = %d, want %d", fact.Role(), role)
	}
}
