package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

type topologyBindableTransport struct {
	events.EventTransport

	mu           sync.Mutex
	bindings     int
	available    bool
	payloadLimit int
}

type topologyBindableWithoutPayloadLimit struct {
	events.EventTransport
	bindings int
}

func (transport *topologyBindableWithoutPayloadLimit) TransportCapabilities() events.TransportCapabilities {
	capabilities, _ := events.NewTransportCapabilities("test.topology.missing-payload-limit.v1", events.TransportScopeCrossProcess, false)
	return capabilities
}

func (transport *topologyBindableWithoutPayloadLimit) TransportAvailable() bool { return true }

func (transport *topologyBindableWithoutPayloadLimit) BindEventRuntime(events.RuntimeBinding) error {
	transport.bindings++
	return nil
}

func (transport *topologyBindableTransport) TransportCapabilities() events.TransportCapabilities {
	capabilities, _ := events.NewTransportCapabilities("test.topology.cross-process.v1", events.TransportScopeCrossProcess, false)
	return capabilities
}

func (transport *topologyBindableTransport) TransportAvailable() bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.available
}

func (transport *topologyBindableTransport) MaxEncodedEventBytes() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.payloadLimit
}

func (transport *topologyBindableTransport) BindEventRuntime(binding events.RuntimeBinding) error {
	if binding == nil {
		return events.Failure(events.CodeEventConfig)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.bindings++
	if transport.bindings != 1 {
		return events.Failure(events.CodeEventConfig)
	}
	return nil
}

func (transport *topologyBindableTransport) bindingCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.bindings
}

func (transport *topologyBindableTransport) setAvailable(value bool) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.available = value
}

func topologyEventConfig(fixture p7EventRuntimeFixture, transport events.EventTransport) Config[p7EventPrincipal, p7EventActor] {
	return Config[p7EventPrincipal, p7EventActor]{
		Descriptors: fixture.app.descriptors, EventRegistry: fixture.app.eventRegistry,
		EventFactories: fixture.app.eventFactories, EventTransport: transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
	}
}

func TestOrder7SQLiteRejectsCrossProcessTransportBeforeRuntimeBinding(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	transport := &topologyBindableTransport{EventTransport: fixture.app.eventTransport, available: true, payloadLimit: events.MaximumLimits().MaxEncodedEventBytes}

	_, err := validateEventConfiguration(topologyEventConfig(fixture, transport), fixture.app.registry, golem.SQLite)
	if err == nil || !strings.Contains(err.Error(), "cross-process event transport requires PostgreSQL") {
		t.Fatalf("SQLite cross-process transport error = %v", err)
	}
	if bindings := transport.bindingCount(); bindings != 0 {
		t.Fatalf("SQLite refusal retained %d runtime bindings", bindings)
	}
}

func TestOrder7PostgreSQLAcceptsAvailableCrossProcessTransportAndBindsOnce(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	transport := &topologyBindableTransport{EventTransport: fixture.app.eventTransport, available: true, payloadLimit: events.MaximumLimits().MaxEncodedEventBytes}
	config := topologyEventConfig(fixture, transport)

	if _, err := validateEventConfiguration(config, fixture.app.registry, golem.PostgreSQL); err != nil {
		t.Fatalf("PostgreSQL cross-process transport rejected: %v", err)
	}
	app := &App[p7EventPrincipal, p7EventActor]{
		database: fixture.app.database, registry: fixture.app.registry,
		eventRegistry: fixture.app.eventRegistry, eventSchemas: fixture.app.eventSchemas,
		eventLimits: events.DefaultLimits(), eventTransport: transport,
		eventProvider: golem.PostgreSQL,
	}
	if err := app.initializeEventRuntime(nil, config.ReportEventOperator); err != nil {
		t.Fatal(err)
	}
	if bindings := transport.bindingCount(); bindings != 1 {
		t.Fatalf("PostgreSQL runtime bindings = %d, want 1", bindings)
	}
	if !app.EventCapabilities().TransportAvailable() {
		t.Fatal("available PostgreSQL transport reported unavailable")
	}

	transport.setAvailable(false)
	if app.EventCapabilities().TransportAvailable() {
		t.Fatal("runtime readiness retained stale transport availability")
	}
}

func TestOrder7UnavailableCrossProcessTransportRefusesBeforeRuntimeBinding(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	transport := &topologyBindableTransport{EventTransport: fixture.app.eventTransport, payloadLimit: events.MaximumLimits().MaxEncodedEventBytes}

	_, err := validateEventConfiguration(topologyEventConfig(fixture, transport), fixture.app.registry, golem.PostgreSQL)
	if err == nil || !strings.Contains(err.Error(), "event transport is unavailable") {
		t.Fatalf("unavailable PostgreSQL transport error = %v", err)
	}
	if bindings := transport.bindingCount(); bindings != 0 {
		t.Fatalf("unavailable transport retained %d runtime bindings", bindings)
	}
}

func TestOrder7TransportBecomingUnavailableBeforeBindRetainsNoRuntimeCapability(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	transport := &topologyBindableTransport{EventTransport: fixture.app.eventTransport, available: true, payloadLimit: events.MaximumLimits().MaxEncodedEventBytes}
	config := topologyEventConfig(fixture, transport)
	if _, err := validateEventConfiguration(config, fixture.app.registry, golem.PostgreSQL); err != nil {
		t.Fatal(err)
	}

	transport.setAvailable(false)
	app := &App[p7EventPrincipal, p7EventActor]{
		database: fixture.app.database, registry: fixture.app.registry,
		eventRegistry: fixture.app.eventRegistry, eventSchemas: fixture.app.eventSchemas,
		eventLimits: events.DefaultLimits(), eventTransport: transport,
		eventProvider: golem.PostgreSQL,
	}
	if err := app.initializeEventRuntime(nil, config.ReportEventOperator); err == nil {
		t.Fatal("transport that became unavailable before binding was accepted")
	}
	if bindings := transport.bindingCount(); bindings != 0 {
		t.Fatalf("unavailable transport retained %d runtime bindings", bindings)
	}
}

func TestOrder7CrossProcessPayloadLimitBelowConfiguredMaximumRefusesBeforeBinding(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	transport := &topologyBindableTransport{
		EventTransport: fixture.app.eventTransport,
		available:      true,
		payloadLimit:   events.DefaultLimits().MaxEncodedEventBytes - 1,
	}
	config := topologyEventConfig(fixture, transport)

	_, err := validateEventConfiguration(config, fixture.app.registry, golem.PostgreSQL)
	if err == nil || !strings.Contains(err.Error(), "payload limit is below MaxEncodedEventBytes") {
		t.Fatalf("undersized cross-process payload limit error = %v", err)
	}
	if bindings := transport.bindingCount(); bindings != 0 {
		t.Fatalf("undersized transport retained %d runtime bindings", bindings)
	}
}

func TestOrder7CrossProcessTransportWithoutPayloadLimitRefusesBeforeBinding(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	transport := &topologyBindableWithoutPayloadLimit{EventTransport: fixture.app.eventTransport}
	config := topologyEventConfig(fixture, transport)

	_, err := validateEventConfiguration(config, fixture.app.registry, golem.PostgreSQL)
	if err == nil || !strings.Contains(err.Error(), "must report a positive encoded payload limit") {
		t.Fatalf("missing cross-process payload limit error = %v", err)
	}
	if transport.bindings != 0 {
		t.Fatalf("transport without payload limit retained %d runtime bindings", transport.bindings)
	}
}
