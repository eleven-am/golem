package p8oracle

import (
	_ "embed"
	"testing"
)

//go:embed testdata/hook_computed_oracle_test.go
var hookComputedOracleSource []byte

func TestP8HookPhaseAndResultCrossSurfaceOracle(t *testing.T) {
	RunExternalScenarioRace(t, hookComputedOracleSource, "hook-phase-result")
}

func TestP8ComputedAndBatchedDependencyDisclosureOracle(t *testing.T) {
	RunExternalScenarioRace(t, hookComputedOracleSource, "computed-batched-disclosure")
}

func TestP8AfterCommitFailureDoesNotChangeCommittedResult(t *testing.T) {
	RunExternalScenarioRace(t, hookComputedOracleSource, "after-commit-failure")
}

func TestP8CustomAndComputedResolverCapabilityInventory(t *testing.T) {
	RunExternalScenarioRace(t, hookComputedOracleSource, "resolver-capability-inventory")
}
