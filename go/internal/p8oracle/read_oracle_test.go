package p8oracle

import (
	_ "embed"
	"testing"
)

//go:embed testdata/read_oracle_test.go
var readOracleSource []byte

func TestP8ReadCrossEntryPointIndependentOracle(t *testing.T) {
	RunExternalScenario(t, readOracleSource, "cross-entry-point")
}

func TestP8ReadMaskErrorAndPaginationParity(t *testing.T) {
	RunExternalScenario(t, readOracleSource, "mask-error-pagination")
}

func TestP8CustomQueryCannotChangeAuthorizationOrSystemCapability(t *testing.T) {
	RunExternalScenario(t, readOracleSource, "custom-capability")
}

func TestP8CallerTransactionReadParity(t *testing.T) {
	RunExternalScenario(t, readOracleSource, "caller-transaction")
}
