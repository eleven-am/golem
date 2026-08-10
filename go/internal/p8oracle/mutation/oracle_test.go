package mutation

import (
	_ "embed"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var externalMutationOracle []byte

func TestP8MutationCrossEntryPointIndependentOracle(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalMutationOracle, "cross-entry")
}

func TestP8NestedBatchAndUpsertParity(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, externalMutationOracle, "nested-batch-upsert")
}

func TestP8CustomMutationTransactionParity(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalMutationOracle, "custom-transaction")
}

func TestP8MutationDenialAndProviderFailureRollbackParity(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalMutationOracle, "denial-provider-failure")
}
