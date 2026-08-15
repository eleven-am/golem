package analytics

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestSQLiteDecimalAverageNeverUsesReal(t *testing.T) {
	statement := renderP6ExactAggregateEvidence(t, policyir.ProviderSQLite)
	sql := statement.SQL()

	want := sqliteprovider.AnalyticsDecimalAvgFunction + `("golem_a0"."decimal_value", 13)`
	if !strings.Contains(sql, want) {
		t.Fatalf("SQLite decimal average does not use the exact provider aggregate %q:\n%s", want, sql)
	}
	lower := strings.ToLower(sql)
	for _, forbidden := range []string{"avg(", " real", "cast(", "total("} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("SQLite exact aggregate SQL contains lossy path %q:\n%s", forbidden, sql)
		}
	}
}

func TestPostgreSQLExactNumericRenderer(t *testing.T) {
	statement := renderP6ExactAggregateEvidence(t, policyir.ProviderPostgreSQL)
	sql := statement.SQL()

	for _, want := range []string{
		`SUM("golem_a0"."big_int")::text`,
		`SUM("golem_a0"."decimal_value")::text`,
		`ROUND(AVG("golem_a0"."decimal_value"), 13)::text`,
		`FROM "public"."posts" AS "golem_a0"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("PostgreSQL exact aggregate SQL lacks %q:\n%s", want, sql)
		}
	}
	lower := strings.ToLower(sql)
	for _, forbidden := range []string{"double precision", "::real", "::float", "cast(\"golem_a0\".\"decimal_value\" as real"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("PostgreSQL exact aggregate SQL contains lossy path %q:\n%s", forbidden, sql)
		}
	}
}

func renderP6ExactAggregateEvidence(t *testing.T, provider policyir.Provider) Statement {
	t.Helper()
	fixture := schematest.NewIndexedExact(t)
	bigField := golem.GeneratedOrderedField[analyticsRendererPost, int64](fixture.PostBigInt)
	decimalField := golem.GeneratedOrderedField[analyticsRendererPost, golem.Decimal](fixture.PostDecimal)
	integerSum := golem.GeneratedMeasure[analyticsRendererPost, int64, golem.ExactInteger](fixture.Post, bigField, golem.AggregateSum)
	decimalSum := golem.GeneratedMeasure[analyticsRendererPost, golem.Decimal, golem.ExactDecimal](fixture.Post, decimalField, golem.AggregateSum)
	decimalAverage := golem.GeneratedMeasure[analyticsRendererPost, golem.Decimal, golem.ExactDecimal](fixture.Post, decimalField, golem.AggregateAverage)
	request := golem.GeneratedAggregate(fixture.Post,
		golem.GeneratedAggregateSelect[analyticsRendererPost](integerSum, decimalSum, decimalAverage),
	)
	frozen, err := golem.RuntimeFreezeAggregateRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()),
		policyir.CapabilityBinaryText,
		policyir.CapabilityASCIIInsensitiveText,
		policyir.CapabilityExactJSON,
		policyir.CapabilityScalarListJSON,
		policyir.CapabilityRelationCorrelation,
	)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := Render(planned, fixture.Registry, provider, proof)
	if err != nil {
		t.Fatal(err)
	}
	return statement
}
