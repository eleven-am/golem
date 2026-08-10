package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

type lifecyclePublisher struct {
	started chan struct{}
	once    sync.Once
}

func (publisher *lifecyclePublisher) Run(ctx context.Context) error {
	publisher.once.Do(func() { close(publisher.started) })
	<-ctx.Done()
	return nil
}

type lifecycleAdapter struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (adapter *lifecycleAdapter) Identity() events.CDCIdentity {
	return events.CDCIdentity{Name: "lifecycle", Version: "v1", Provider: golem.SQLite}
}
func (*lifecycleAdapter) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}
func (adapter *lifecycleAdapter) Run(ctx context.Context, _ events.CDCEmitter) error {
	adapter.once.Do(func() { close(adapter.started) })
	if adapter.release != nil {
		<-adapter.release
		return nil
	}
	<-ctx.Done()
	return nil
}

func TestP7RunEventPublisherOwnsLifecycleRejectsDuplicateAndReportsDynamicCapability(t *testing.T) {
	publisher := &lifecyclePublisher{started: make(chan struct{})}
	adapter := &lifecycleAdapter{started: make(chan struct{})}
	app := &App[struct{}, struct{}]{
		eventProvider: golem.SQLite, eventPublisher: publisher,
		eventLimits: events.DefaultLimits(), eventModels: []golem.ModelID{{1}},
		eventAdapterNames: []string{"sqlite:lifecycle@v1"},
		eventCDCWorkers:   []eventCDCWorker{{adapter: adapter}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunEventPublisher(ctx) }()
	select {
	case <-publisher.started:
	case <-time.After(time.Second):
		t.Fatal("publisher did not start")
	}
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("CDC adapter did not start")
	}
	if capabilities := app.EventCapabilities(); !capabilities.PublisherEnabled() || !capabilities.PublisherRunning() || !capabilities.ExternalWritesObserved() {
		t.Fatal("running capabilities are incomplete")
	}
	if code, ok := events.CodeOf(app.RunEventPublisher(context.Background())); !ok || code != events.CodeEventPublisherRunning {
		t.Fatalf("duplicate run code=%q ok=%t", code, ok)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("publisher did not stop")
	}
	if app.EventCapabilities().PublisherRunning() {
		t.Fatal("publisher remained running after complete cancellation")
	}
}

func TestP7RunEventPublisherCancellationGraceDoesNotBlockOnHostileAdapter(t *testing.T) {
	release := make(chan struct{})
	publisher := &lifecyclePublisher{started: make(chan struct{})}
	adapter := &lifecycleAdapter{started: make(chan struct{}), release: release}
	app := &App[struct{}, struct{}]{
		eventProvider: golem.SQLite, eventPublisher: publisher,
		eventLimits:     events.Limits{ShutdownGrace: time.Millisecond},
		eventCDCWorkers: []eventCDCWorker{{adapter: adapter}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunEventPublisher(ctx) }()
	<-adapter.started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("shutdown grace did not bound hostile adapter")
	}
	if !app.EventCapabilities().PublisherRunning() {
		t.Fatal("hostile adapter was incorrectly reported stopped")
	}
	if code, ok := events.CodeOf(app.RunEventPublisher(context.Background())); !ok || code != events.CodeEventPublisherRunning {
		t.Fatalf("hostile duplicate run code=%q ok=%t", code, ok)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for app.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if app.EventCapabilities().PublisherRunning() {
		t.Fatal("running capability did not clear after hostile adapter exited")
	}
}

type crossProcessWithoutBinding struct{}

func (crossProcessWithoutBinding) Publish(context.Context, events.EventBatch) error { return nil }
func (crossProcessWithoutBinding) Subscribe(context.Context, events.Subscription) (events.Stream, error) {
	return nil, events.Failure(events.CodeEventSourceClosed)
}
func (crossProcessWithoutBinding) TransportCapabilities() events.TransportCapabilities {
	capabilities, _ := events.NewTransportCapabilities("test.cross-process.v1", events.TransportScopeCrossProcess, true)
	return capabilities
}

func TestP7CrossProcessTransportWithoutRuntimeBindingIsRejected(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	_, err := validateEventConfiguration(Config[p7EventPrincipal, p7EventActor]{
		Descriptors:   fixture.app.descriptors,
		EventRegistry: fixture.app.eventRegistry, EventFactories: fixture.app.eventFactories,
		EventTransport: crossProcessWithoutBinding{}, ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
	}, fixture.app.registry, fixture.app.eventProvider)
	if err == nil {
		t.Fatal("cross-process transport without runtime binding was accepted")
	}
}

type lifecycleBindableTransport struct {
	events.EventTransport
	mu       sync.Mutex
	bindings int
}

func (transport *lifecycleBindableTransport) TransportCapabilities() events.TransportCapabilities {
	capabilities, _ := events.NewTransportCapabilities("test.bound-cross-process.v1", events.TransportScopeCrossProcess, true)
	return capabilities
}
func (transport *lifecycleBindableTransport) BindEventRuntime(binding events.RuntimeBinding) error {
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

func TestP7EventRuntimeBindsCrossProcessTransportExactlyOnce(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	transport := &lifecycleBindableTransport{EventTransport: fixture.app.eventTransport}
	app := &App[p7EventPrincipal, p7EventActor]{
		database: fixture.app.database, registry: fixture.app.registry,
		eventRegistry: fixture.app.eventRegistry, eventSchemas: fixture.app.eventSchemas,
		eventLimits: events.DefaultLimits(), eventTransport: transport,
		eventProvider: golem.SQLite,
	}
	if err := app.initializeEventRuntime(nil, func(context.Context, events.OperatorAuditRecord) {}); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	bindings := transport.bindings
	transport.mu.Unlock()
	if bindings != 1 {
		t.Fatalf("runtime bindings=%d", bindings)
	}
}
