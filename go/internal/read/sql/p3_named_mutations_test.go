package sql

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

// TestMutationIGNORE_INDEX_METADATAChangesThePhysicalPlan pins the index fact
// that chooses one correlated statement versus a root plus bounded batches.
// Runtime's indexed agreement test separately proves the answer is unchanged.
func TestMutationIGNORE_INDEX_METADATAChangesThePhysicalPlan(t *testing.T) {
	for _, test := range []struct {
		name       string
		fixture    schematest.Fixture
		strategy   RelationStrategy
		correlated int
	}{
		{name: "unindexed batches", fixture: schematest.New(t), strategy: RelationBatch, correlated: 0},
		{name: "indexed correlates", fixture: schematest.NewIndexed(t), strategy: RelationCorrelated, correlated: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			users := golem.GeneratedModelDescriptor[renderUser](test.fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
			title := golem.GeneratedTextField[renderPost, string](test.fixture.PostTitle)
			posts := golem.GeneratedToMany[renderUser, renderPost](test.fixture.UserPosts, test.fixture.Authorship, test.fixture.Post)
			frozen, err := golem.FreezeFindMany(users, golem.Select[renderUser](posts.Args(golem.OrderBy(title.Asc()), golem.Take[renderPost](1), golem.Select[renderPost](title))))
			if err != nil {
				t.Fatal(err)
			}
			request, err := readbind.Request(frozen, test.fixture.Registry, policyir.PortableProviders())
			if err != nil {
				t.Fatal(err)
			}
			planned, err := readplan.System(request, test.fixture.Registry, readplan.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if got := ChooseRelationStrategy(planned, planned.Relations()[0], test.fixture.Registry, policyir.ProviderSQLite); got != test.strategy {
				t.Fatalf("strategy=%d want=%d", got, test.strategy)
			}
			proof, err := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(test.fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
			if err != nil {
				t.Fatal(err)
			}
			statement, err := Render(planned, test.fixture.Registry, policyir.ProviderSQLite, proof)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(statement.CorrelatedColumns()); got != test.correlated {
				t.Fatalf("correlated columns=%d want=%d", got, test.correlated)
			}
		})
	}
}
