package diagnostic

import (
	_ "embed"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var oracleSource []byte

func TestP8DiagnosticAndTelemetryRedactionCanaryCorpus(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "diagnostic-telemetry")
}

func TestP8HealthEndpointSafeShape(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "health-safe-shape")
}

func TestP8RawProviderErrorNeverReachesPublicOrObservation(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "raw-provider-error")
}
