package sql

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type candidateUser struct{}

func semanticCandidateFixture(t *testing.T, provider policyir.Provider, operation string, maximumParameters int) (readplan.Plan, schematest.Fixture, policysql.CapabilityProof) {
	t.Helper()
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[candidateUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[candidateUser, string](fixture.UserName)
	rules := golem.NewRules[candidateUser]()
	rules.CanRead(name.Eq("visible"))
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
	var frozen golem.FrozenReadRequest
	switch operation {
	case "count":
		frozen, err = golem.FreezeCount(descriptor)
	case "paged":
		frozen, err = golem.FreezeFindMany(descriptor, golem.Select[candidateUser](name), golem.Skip[candidateUser](1))
	case "taken":
		frozen, err = golem.FreezeFindMany(descriptor, golem.Select[candidateUser](name), golem.Take[candidateUser](3))
	default:
		frozen, err = golem.FreezeFindMany(descriptor, golem.Select[candidateUser](name))
	}
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	limits := readplan.DefaultLimits()
	limits.MaxStatementParameters = maximumParameters
	planned, err := readplan.Caller(request, fixture.Registry, renderPolicySet{policyir.ModelID(fixture.User): policy}, limits)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	return planned, fixture, proof
}

// TestRenderSemanticCandidatesProjectsIdentityUnderTheAuthorizedPredicate pins
// the two properties ranking depends on: the subquery projects exactly the
// owner's primary-key columns, and its predicate is the same compiled
// authorized predicate an ordinary read executes. It carries no row ceiling.
func TestRenderSemanticCandidatesProjectsIdentityUnderTheAuthorizedPredicate(t *testing.T) {
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		planned, fixture, proof := semanticCandidateFixture(t, provider, "taken", MaxStatementParameters)
		candidates, err := RenderSemanticCandidates(planned, fixture.Registry, provider, proof, 2)
		if err != nil {
			t.Fatal(err)
		}
		table := `"users"`
		if provider == policyir.ProviderPostgreSQL {
			table = `"public"."users"`
		}
		want := `SELECT "golem_r0"."id" AS "id" FROM ` + table + ` AS "golem_r0" WHERE `
		if !strings.HasPrefix(candidates.SQL(), want) {
			t.Fatalf("provider %d candidate SQL=%q", provider, candidates.SQL())
		}
		if !strings.Contains(candidates.SQL(), `"golem_r0"."name"`) {
			t.Fatalf("provider %d candidate predicate is not the authorized fragment: %q", provider, candidates.SQL())
		}
		if strings.Contains(strings.ToUpper(candidates.SQL()), " LIMIT ") {
			t.Fatalf("provider %d candidate subquery is bounded: %q", provider, candidates.SQL())
		}
		if args := candidates.Args(); len(args) != 1 || args[0] != "visible" {
			t.Fatalf("provider %d candidate args=%#v", provider, args)
		}
		if columns := candidates.Columns(); len(columns) != 1 || columns[0] != "id" {
			t.Fatalf("provider %d candidate columns=%#v", provider, columns)
		}
		if fields := candidates.Fields(); len(fields) != 1 || fields[0] != policyir.FieldID(fixture.UserID) {
			t.Fatalf("provider %d candidate fields=%#v", provider, fields)
		}
	}
}

// TestRenderSemanticCandidatesRefusesAnAbsentIdentityProjection keeps candidacy
// fail-closed: a plan that does not carry every primary-key field cannot be
// turned into a candidate set with a partially known identity.
func TestRenderSemanticCandidatesRefusesAnAbsentIdentityProjection(t *testing.T) {
	planned, fixture, proof := semanticCandidateFixture(t, policyir.ProviderSQLite, "count", MaxStatementParameters)
	if _, err := RenderSemanticCandidates(planned, fixture.Registry, policyir.ProviderSQLite, proof, 2); err == nil {
		t.Fatal("candidate rendering accepted a plan without the primary identity")
	}
	if _, err := RenderSemanticCandidates(planned, nil, policyir.ProviderSQLite, proof, 2); err == nil {
		t.Fatal("candidate rendering accepted an absent registry")
	}
	paged, fixture, proof := semanticCandidateFixture(t, policyir.ProviderSQLite, "paged", MaxStatementParameters)
	if _, err := RenderSemanticCandidates(paged, fixture.Registry, policyir.ProviderSQLite, proof, 2); err == nil {
		t.Fatal("candidate rendering accepted a paginated plan")
	}
}

func TestRenderSemanticCandidatesAccountsForEnclosingRankParameters(t *testing.T) {
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		search, fixture, proof := semanticCandidateFixture(t, provider, "taken", 3)
		if _, err := RenderSemanticCandidates(search, fixture.Registry, provider, proof, 2); err != nil {
			t.Fatalf("provider %d rejected exact search parameter budget: %v", provider, err)
		}
		if _, err := RenderSemanticCandidates(search, fixture.Registry, provider, proof, 3); err == nil {
			t.Fatalf("provider %d accepted search parameter overflow", provider)
		}
		similar, fixture, proof := semanticCandidateFixture(t, provider, "taken", 4)
		if _, err := RenderSemanticCandidates(similar, fixture.Registry, provider, proof, 3); err != nil {
			t.Fatalf("provider %d rejected exact similarity parameter budget: %v", provider, err)
		}
		if _, err := RenderSemanticCandidates(similar, fixture.Registry, provider, proof, 4); err == nil {
			t.Fatalf("provider %d accepted similarity parameter overflow", provider)
		}
		if _, err := RenderSemanticCandidates(similar, fixture.Registry, provider, proof, -1); err == nil {
			t.Fatalf("provider %d accepted a negative enclosing parameter count", provider)
		}
	}
}
