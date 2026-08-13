package transporttest

import (
	"testing"

	"github.com/eleven-am/golem/go/events"
)

func TestConformanceHarnessOwnsMemoryBaseline(t *testing.T) {
	Run(t, func(testing.TB) (events.EventTransport, error) {
		return events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	}, ExpectedCapabilities{Identity: "golem.memory.v1", Scope: events.TransportScopeProcessLocal, Durable: false})
}
