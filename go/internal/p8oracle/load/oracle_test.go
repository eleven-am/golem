package load

import (
	_ "embed"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var oracleSource []byte

func TestP8StatementAndConnectionBudgetMatrix(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "statement-connection")
}

func TestP8GoroutineQueueAndEvaluationHardBounds(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "goroutine-queue-evaluation")
}

func TestP8CardinalityRampNoSuperlinearResourceGrowth(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "cardinality-ramp")
}

func BenchmarkP8ReferenceApplicationProfiles(b *testing.B) {
	p8oracle.RunExternalBenchmark(b, oracleSource, "BenchmarkP8ExternalReferenceApplication", 20)
}
