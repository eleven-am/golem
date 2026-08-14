package sql

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestCorrelatedKeyKindGateCoversEveryLegalScalarRelationKind(t *testing.T) {
	legal := []compilerir.LogicalTypeKind{
		compilerir.TypeBool, compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64,
		compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal, compilerir.TypeString,
		compilerir.TypeBytes, compilerir.TypeUUID, compilerir.TypeDate, compilerir.TypeTime,
		compilerir.TypeDateTime, compilerir.TypeEnum,
	}
	for _, kind := range legal {
		if !correlatedKeyKindAllowed(kind) {
			t.Errorf("legal relation-key kind %q was excluded", kind)
		}
	}
	for _, kind := range []compilerir.LogicalTypeKind{compilerir.TypeJSON, compilerir.TypeScalarList, ""} {
		if correlatedKeyKindAllowed(kind) {
			t.Errorf("non-key kind %q was accepted", kind)
		}
	}
}

func TestIndexedToManyRendersAuthorizedCorrelatedJSONAcrossProviders(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	users := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	title := golem.GeneratedTextField[renderPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[renderUser, renderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	frozen, err := golem.FreezeFindMany(users, golem.Select[renderUser](posts.Args(golem.OrderBy(title.Desc()), golem.Take[renderPost](2), golem.Select[renderPost](title))))
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
	if ChooseRelationStrategy(planned, planned.Relations()[0], fixture.Registry, policyir.ProviderSQLite) != RelationCorrelated {
		t.Fatal("indexed UUID relation did not select correlated strategy")
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		proof, _ := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
		statement, err := Render(planned, fixture.Registry, provider, proof)
		if err != nil {
			t.Fatal(err)
		}
		if len(statement.CorrelatedColumns()) != 1 {
			t.Fatalf("provider %d correlated columns=%d", provider, len(statement.CorrelatedColumns()))
		}
		correlatedFacts := statement.PlanMap().MatchingAliasFacts("golem_cr0")
		if len(correlatedFacts) != 1 {
			t.Fatalf("provider %d correlated access facts=%#v", provider, correlatedFacts)
		}
		assertPlanAliasFact(t, correlatedFacts[0], "golem_cr0", policyir.ModelID(fixture.Post), policyir.RelationID(fixture.Authorship), nil, PlanAliasCorrelatedRelation)
		for _, omitted := range []string{"golem_cd0", "golem_cf0", "golem_cg0", "golem_rel0", "golem_payload"} {
			if facts := statement.PlanMap().MatchingAliasFacts(omitted); len(facts) != 0 {
				t.Fatalf("provider %d correlated derived/projection alias %q retained facts %#v", provider, omitted, facts)
			}
		}
		if provider == policyir.ProviderPostgreSQL && (!strings.Contains(statement.SQL(), "json_agg") || !strings.Contains(statement.SQL(), "::text") || !strings.Contains(statement.SQL(), `ORDER BY "golem_cg0"."golem_ordinal"`)) {
			t.Errorf("PostgreSQL correlated exact text boundary missing: %s", statement.SQL())
		}
	}
}

func TestCorrelatedJSONExactTextAndEmptyArrayBoundariesArePinned(t *testing.T) {
	column := `"t"."value"`
	for _, kind := range []compilerir.LogicalTypeKind{compilerir.TypeInt64, compilerir.TypeDecimal} {
		got := correlatedJSONValue(policyir.ProviderPostgreSQL, compilerir.LogicalTypeIR{Kind: kind}, column)
		if !strings.Contains(got, "CAST("+column+" AS TEXT)") {
			t.Fatalf("PostgreSQL %s lost exact text cast: %s", kind, got)
		}
	}
	t.Run("mutation_NO_TEXT_CAST_BIGINT", func(t *testing.T) {
		accepts := func(expression string) bool {
			return strings.Contains(expression, "CAST("+column+" AS TEXT)")
		}
		expression := correlatedJSONValue(policyir.ProviderPostgreSQL, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, column)
		if !accepts(expression) {
			t.Fatalf("known-good bigint boundary was rejected: %s", expression)
		}
		mutated := strings.ReplaceAll(expression, "CAST("+column+" AS TEXT)", column)
		if accepts(mutated) {
			t.Fatalf("NO_TEXT_CAST_BIGINT mutation escaped acceptance: %s", mutated)
		}
	})
	if got := correlatedJSONValue(policyir.ProviderSQLite, compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}, column); !strings.Contains(got, "printf('%.17g'") {
		t.Fatalf("SQLite float64 lost exact round-trip formatting: %s", got)
	}
	fixture := schematest.NewIndexed(t)
	users := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	title := golem.GeneratedTextField[renderPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[renderUser, renderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	frozen, err := golem.FreezeFindMany(users, golem.Select[renderUser](posts.Args(golem.Select[renderPost](title))))
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
	proof, _ := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statement.SQL(), "COALESCE(json_group_array") || !strings.Contains(statement.SQL(), ", '[]')") {
		t.Fatalf("to-many empty-array boundary is not pinned: %s", statement.SQL())
	}
	t.Run("mutation_DROP_COALESCE", func(t *testing.T) {
		accepts := func(text string) bool {
			return strings.Contains(text, "COALESCE(json_group_array") && strings.Contains(text, ", '[]')")
		}
		if !accepts(statement.SQL()) {
			t.Fatal("known-good empty-array boundary was rejected")
		}
		mutated := strings.Replace(statement.SQL(), "COALESCE(json_group_array", "json_group_array", 1)
		if accepts(mutated) {
			t.Fatalf("DROP_COALESCE mutation escaped acceptance: %s", mutated)
		}
	})
}
