package failure

import (
	_ "embed"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var oracleSource []byte

func TestP8CancellationAndSlowClientRecoveryMatrix(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "cancellation-slow-client")
}

func TestP8ProviderContentionAndPoolStarvationRecovery(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "provider-contention-pool-starvation")
}

func TestP8HookComputedAndObserverFailureIsolation(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "hook-computed-observer-failure")
}

func TestP8PublisherCDCAndMigrationCrashRecovery(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "publisher-cdc-migration-crash")
}

func TestP8GracefulAndForcedShutdownSubprocessMatrix(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "graceful-forced-shutdown")
}
