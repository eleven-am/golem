package natslive

import (
	"bytes"
	_ "embed"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8oracle"
)

//go:embed testdata/oracle_test.go
var externalNATSOracle []byte

func TestOrder7LiveNATSOracleSourceAuthority(t *testing.T) {
	required := [][]byte{
		[]byte("MaxInboundPayloadBytes: livePayloadLimit + 1"),
		[]byte("fixture.awaitAvailability(false)"),
		[]byte("!bytes.Equal(encoded, duplicateRaw.Data) || duplicateEvent.ID() != firstEvent.ID() || duplicateEvent.Metadata().EventID() != firstEvent.Metadata().EventID()"),
		[]byte("|| strings.Contains(subject, generationText)"),
		[]byte("if _, err := lateRaw.NextMsg(250 * time.Millisecond); !errors.Is(err, natsclient.ErrTimeout) {\n\t\tfixture.t.Fatalf(\"Core NATS late subscriber replayed history: %v\", err)\n\t}"),
		[]byte("if event, err := lateStream.Recv(quiet); err == nil {\n\t\tfixture.t.Fatalf(\"generated late subscriber replayed event %s\", event.ID())\n\t} else if eventCode(err) != events.CodeSubscriptionCancelled {\n\t\tfixture.t.Fatalf(\"generated late subscriber failed instead of remaining replay-free: %v\", err)\n\t}"),
	}
	for _, fragment := range required {
		if !bytes.Contains(externalNATSOracle, fragment) {
			t.Fatalf("external live NATS oracle lost reviewed assertion %q", fragment)
		}
	}
}

func TestOrder7ExternalGeneratedNATSOutageReconnectAndReadiness(t *testing.T) {
	p8oracle.RunExternalNATSScenarioRace(t, externalNATSOracle, "outage-reconnect-readiness")
}

func TestOrder7ExternalGeneratedNATSDuplicateIdentityAndCoreNoReplay(t *testing.T) {
	p8oracle.RunExternalNATSScenario(t, externalNATSOracle, "duplicate-identity-core-no-replay")
}
