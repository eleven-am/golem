package disclosure

import (
	_ "embed"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var oracleSource []byte

func TestP8DisclosureCanaryCorpusCallerGraphQLEvents(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "caller-graphql-events")
}

func TestP8MissingInvisibleAndMaskedIndistinguishabilityOracle(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "missing-invisible-masked")
}

func TestP8HookComputedCustomAndAnalyticsDisclosureCorpus(t *testing.T) {
	p8oracle.RunExternalScenarioRace(t, oracleSource, "hook-computed-custom-analytics")
}

func FuzzP8PublicInputNeverDisclosesProtectedCanary(f *testing.F) {
	const corpus = "external-public-input-corpus-v1"
	f.Add(corpus)
	f.Fuzz(func(t *testing.T, selected string) {
		if selected != corpus {
			return
		}
		p8oracle.RunExternalFuzz(t, oracleSource, "FuzzP8ExternalPublicInput", 3*time.Second)
	})
}
