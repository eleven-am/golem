package analytics

import (
	_ "embed"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var externalAnalyticsOracle []byte

func TestP8AnalyticsCrossEntryPointIndependentOracle(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalAnalyticsOracle, "analytics-cross-entry")
}

func TestP8ScopedReadAuthorizationAndAuditRedTeam(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalAnalyticsOracle, "scoped-red-team")
}

func TestP8AnalyticsExactScalarAndLimitParity(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalAnalyticsOracle, "exact-scalar-limit")
}

func TestP8UnsupportedRelationAggregationRefusesEveryEntryPoint(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalAnalyticsOracle, "unsupported-relation")
}
