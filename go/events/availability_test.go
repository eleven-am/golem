package events

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

type availabilityProbeTransport struct {
	available    bool
	payloadLimit int
}

func (transport availabilityProbeTransport) Publish(context.Context, EventBatch) error { return nil }
func (transport availabilityProbeTransport) Subscribe(context.Context, Subscription) (Stream, error) {
	return nil, Failure(CodeEventSourceClosed)
}
func (transport availabilityProbeTransport) TransportAvailable() bool  { return transport.available }
func (transport availabilityProbeTransport) MaxEncodedEventBytes() int { return transport.payloadLimit }

func TestOrder7TransportAvailabilityIsClosedAndOptional(t *testing.T) {
	if AvailabilityOf(nil) {
		t.Fatal("nil transport reported available")
	}
	if !AvailabilityOf(availabilityProbeTransport{available: true}) || AvailabilityOf(availabilityProbeTransport{}) {
		t.Fatal("explicit transport availability was not preserved")
	}
	memory, err := NewMemoryTransport(MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if !AvailabilityOf(memory) {
		t.Fatal("transport without an external availability SPI reported unavailable")
	}
	if RuntimeCapabilities(golem.SQLite, nil, nil, TransportCapabilities{}, false, false, nil, nil, false).TransportAvailable() {
		t.Fatal("capability snapshot without a configured transport reported available")
	}
}

func TestOrder7TransportPayloadLimitIsClosedPositiveAndOptional(t *testing.T) {
	for _, transport := range []EventTransport{
		nil,
		availabilityProbeTransport{},
		availabilityProbeTransport{payloadLimit: -1},
	} {
		if limit, ok := PayloadLimitOf(transport); ok || limit != 0 {
			t.Fatalf("invalid transport payload limit = (%d,%t), want (0,false)", limit, ok)
		}
	}
	const want = 2 << 20
	if limit, ok := PayloadLimitOf(availabilityProbeTransport{payloadLimit: want}); !ok || limit != want {
		t.Fatalf("transport payload limit = (%d,%t), want (%d,true)", limit, ok, want)
	}
	memory, err := NewMemoryTransport(MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if limit, ok := PayloadLimitOf(memory); ok || limit != 0 {
		t.Fatalf("process-local transport payload limit = (%d,%t), want optional absence", limit, ok)
	}
}
