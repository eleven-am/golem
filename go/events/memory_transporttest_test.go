package events_test

import (
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/events/transporttest"
)

func TestMemoryTransportCommonConformance(t *testing.T) {
	transporttest.Run(t, func(testing.TB) (events.EventTransport, error) {
		return events.NewMemoryTransport(events.MemoryLimits{Buffer: 8})
	}, transporttest.ExpectedCapabilities{
		Identity: "golem.memory.v1",
		Scope:    events.TransportScopeProcessLocal,
		Durable:  false,
	})
}
