package event

import (
	_ "embed"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var externalEventOracle []byte

func TestP8EventCrossEntryPointIndependentOracle(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalEventOracle, "cross-entry-point")
}

func TestP8EventFreshAuthorizationAndSuppressionParity(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalEventOracle, "fresh-authorization")
}

func TestP8EventOverflowCancellationAndIdentityParity(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalEventOracle, "overflow-cancellation-identity")
}

func TestP8CDCAdapterUsesReleasedRuntimePath(t *testing.T) {
	p8oracle.RunExternalScenario(t, externalEventOracle, "cdc-released-path")
}
