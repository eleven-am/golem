package nats

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/events/transporttest"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	natsclient "github.com/nats-io/nats.go"
)

func TestOrder7CoreNATSPassesTransportConformance(t *testing.T) {
	transporttest.Run(t, func(t testing.TB) (events.EventTransport, error) {
		broker := newFakeConnection()
		transport, err := newTransportForTest(Config{URLs: []string{"nats://test"}, SubjectPrefix: "conformance"}, broker)
		if err != nil {
			return nil, err
		}
		if err := transport.BindEventRuntime(conformanceBinding(t)); err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = transport.Close() })
		return transport, nil
	}, transporttest.ExpectedCapabilities{Identity: "golem.nats.v1", Scope: events.TransportScopeCrossProcess, Durable: false})
}

func TestOrder7SubjectRoutesByEventSchemaAndModelNotGeneration(t *testing.T) {
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "tenant.events"})
	generation := golem.SchemaDigest{9}
	eventSchema := golem.EventSchemaDigest{2}
	model := golem.ModelID{3}
	causation := golem.CausationID{4}
	notice := mustNotice(t, golem.EventID{5}, generation, eventSchema, model, causation, 1, []byte("regenerated"))
	if err := transport.BindEventRuntime(mapBinding{"regenerated": notice}); err != nil {
		t.Fatal(err)
	}
	requested, err := eventvalue.NewRoutedSubscription(golem.SchemaDigest{1}, eventSchema, model)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := transport.Subscribe(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	batch, err := eventvalue.NewEventBatch(causation, []events.Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	receiveContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	received, err := stream.Recv(receiveContext)
	if err != nil {
		t.Fatal(err)
	}
	if received.GenerationDigest() != generation {
		t.Fatalf("generation=%x", received.GenerationDigest())
	}
	wantSubject := "tenant.events.g1.0200000000000000000000000000000000000000000000000000000000000000.03000000000000000000000000000000"
	if len(broker.published) != 1 || broker.published[0].subject != wantSubject {
		t.Fatalf("subjects=%#v", broker.published)
	}
}

func TestOrder7PublishEnqueuesWholeBatchThenFlushesOnceWithDeadline(t *testing.T) {
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", FlushTimeout: time.Second})
	generation, eventSchema, model, causation := golem.SchemaDigest{1}, golem.EventSchemaDigest{2}, golem.ModelID{3}, golem.CausationID{4}
	first := mustNotice(t, golem.EventID{1}, generation, eventSchema, model, causation, 1, []byte("one"))
	second := mustNotice(t, golem.EventID{2}, generation, eventSchema, model, causation, 2, []byte("two"))
	batch, _ := eventvalue.NewEventBatch(causation, []events.Notice{first, second})
	if err := transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if len(broker.published) != 2 || !bytes.Equal(broker.published[0].payload, []byte("one")) || !bytes.Equal(broker.published[1].payload, []byte("two")) {
		t.Fatalf("published=%#v", broker.published)
	}
	if broker.flushes != 1 || !broker.flushDeadline {
		t.Fatalf("flushes=%d deadline=%t", broker.flushes, broker.flushDeadline)
	}
}

func TestOrder7IndependentBatchesPublishConcurrently(t *testing.T) {
	broker := &concurrentPublishConnection{fakeConnection: newFakeConnection(), firstEntered: make(chan struct{}), releaseFirst: make(chan struct{}), secondEntered: make(chan struct{})}
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", FlushTimeout: time.Second})
	batch := func(id byte, payload string) events.EventBatch {
		cause := golem.CausationID{id}
		notice := mustNotice(t, golem.EventID{id}, golem.SchemaDigest{1}, golem.EventSchemaDigest{2}, golem.ModelID{3}, cause, 1, []byte(payload))
		value, err := eventvalue.NewEventBatch(cause, []events.Notice{notice})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	done := make(chan error, 2)
	go func() { done <- transport.Publish(context.Background(), batch(1, "first")) }()
	<-broker.firstEntered
	go func() { done <- transport.Publish(context.Background(), batch(2, "second")) }()
	select {
	case <-broker.secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second causal batch was serialized behind the first")
	}
	close(broker.releaseFirst)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrder7BrokerFailuresAreSealedAndNeverEchoConfiguration(t *testing.T) {
	broker := newFakeConnection()
	broker.flushErr = errors.New("nats://secret:credential@broker private subject payload")
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://secret:credential@broker"}, SubjectPrefix: "deployment"})
	notice := mustNotice(t, golem.EventID{1}, golem.SchemaDigest{2}, golem.EventSchemaDigest{3}, golem.ModelID{4}, golem.CausationID{5}, 1, []byte("private payload"))
	batch, _ := eventvalue.NewEventBatch(golem.CausationID{5}, []events.Notice{notice})
	err := transport.Publish(context.Background(), batch)
	if eventCode(err) != events.CodeEventTransport || err.Error() != string(events.CodeEventTransport) {
		t.Fatalf("unsealed error=%q", err)
	}
	for _, secret := range []string{"secret", "credential", "broker", "private", "payload"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q echoed broker or payload detail", err)
		}
	}
}

func TestOrder7BindExactlyOnceAndStartsNoBrokerWork(t *testing.T) {
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
	binding := mapBinding{}
	if err := transport.BindEventRuntime(binding); err != nil {
		t.Fatal(err)
	}
	if broker.calls() != 0 {
		t.Fatalf("Bind performed %d broker operations", broker.calls())
	}
	if code := eventCode(transport.BindEventRuntime(binding)); code != events.CodeEventConfig {
		t.Fatalf("second bind code=%q", code)
	}
}

func TestOrder7SubscribeFlushesRegistrationBeforeSuccess(t *testing.T) {
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
	_ = transport.BindEventRuntime(mapBinding{})
	requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{1}, golem.EventSchemaDigest{2}, golem.ModelID{3})
	if _, err := transport.Subscribe(context.Background(), requested); err != nil {
		t.Fatal(err)
	}
	if broker.flushes != 1 || !broker.flushDeadline {
		t.Fatalf("subscription flushes=%d deadline=%t", broker.flushes, broker.flushDeadline)
	}
}

func TestOrder7BrokerPayloadCeilingMustCoverConfiguredEnvelope(t *testing.T) {
	broker := newFakeConnection()
	broker.maxPayload = 1024
	transport, err := newTransportForTest(Config{URLs: []string{"nats://credential@broker"}, SubjectPrefix: "deployment", MaxInboundPayloadBytes: 1025}, broker)
	if transport != nil || eventCode(err) != events.CodeEventConfig {
		t.Fatalf("transport=%v error=%v", transport, err)
	}
	for _, secret := range []string{"credential", "broker"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked broker configuration: %q", err)
		}
	}
	for _, number := range []string{"1024", "1025"} {
		if !strings.Contains(err.Error(), number) {
			t.Fatalf("error %q does not name the payload ceiling %s", err, number)
		}
	}
	if !strings.Contains(err.Error(), "MaxInboundPayloadBytes") {
		t.Fatalf("error %q does not name the configured field", err)
	}
}

func TestOrder7PayloadReporterReturnsConfiguredVerifiedCeiling(t *testing.T) {
	broker := newFakeConnection()
	broker.maxPayload = 1025
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", MaxInboundPayloadBytes: 1025, PendingBytes: 2048})
	if transport.MaxEncodedEventBytes() != 1025 {
		t.Fatalf("reported payload ceiling=%d", transport.MaxEncodedEventBytes())
	}
}

func TestOrder7ForeignDecodedRouteAndOverflowCloseOnlyStream(t *testing.T) {
	t.Run("foreign decoded identity", func(t *testing.T) {
		broker := newFakeConnection()
		transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
		schema, model := golem.EventSchemaDigest{1}, golem.ModelID{2}
		foreign := mustNotice(t, golem.EventID{3}, golem.SchemaDigest{4}, golem.EventSchemaDigest{9}, model, golem.CausationID{5}, 1, []byte("foreign"))
		_ = transport.BindEventRuntime(mapBinding{"foreign": foreign})
		requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{7}, schema, model)
		stream, err := transport.Subscribe(context.Background(), requested)
		if err != nil {
			t.Fatal(err)
		}
		broker.emit(transport.subject(schema, model), []byte("foreign"))
		if _, err := stream.Recv(context.Background()); eventCode(err) != events.CodeEventTransport {
			t.Fatalf("foreign route error=%v", err)
		}
	})
	t.Run("foreign decoded model", func(t *testing.T) {
		broker := newFakeConnection()
		transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
		schema, model := golem.EventSchemaDigest{1}, golem.ModelID{2}
		foreign := mustNotice(t, golem.EventID{3}, golem.SchemaDigest{4}, schema, golem.ModelID{9}, golem.CausationID{5}, 1, []byte("foreign-model"))
		_ = transport.BindEventRuntime(mapBinding{"foreign-model": foreign})
		requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{7}, schema, model)
		stream, err := transport.Subscribe(context.Background(), requested)
		if err != nil {
			t.Fatal(err)
		}
		broker.emit(transport.subject(schema, model), []byte("foreign-model"))
		if _, err := stream.Recv(context.Background()); eventCode(err) != events.CodeEventTransport {
			t.Fatalf("foreign model error=%v", err)
		}
	})
	t.Run("bounded queue", func(t *testing.T) {
		broker := newFakeConnection()
		transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", StreamBuffer: 1})
		schema, model := golem.EventSchemaDigest{1}, golem.ModelID{2}
		_ = transport.BindEventRuntime(mapBinding{})
		requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{3}, schema, model)
		stream, err := transport.Subscribe(context.Background(), requested)
		if err != nil {
			t.Fatal(err)
		}
		broker.emit(transport.subject(schema, model), []byte("one"))
		broker.emit(transport.subject(schema, model), []byte("two"))
		if _, err := stream.Recv(context.Background()); eventCode(err) != events.CodeEventTransport {
			t.Fatalf("overflow error=%v", err)
		}
	})
	t.Run("foreign broker subject", func(t *testing.T) {
		broker := newFakeConnection()
		transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
		schema, model := golem.EventSchemaDigest{1}, golem.ModelID{2}
		_ = transport.BindEventRuntime(mapBinding{})
		requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{3}, schema, model)
		stream, err := transport.Subscribe(context.Background(), requested)
		if err != nil {
			t.Fatal(err)
		}
		broker.emitAs(transport.subject(schema, model), "deployment.g1.foreign.foreign", []byte("one"))
		if _, err := stream.Recv(context.Background()); eventCode(err) != events.CodeEventTransport {
			t.Fatalf("foreign subject error=%v", err)
		}
	})
	t.Run("bounded bytes", func(t *testing.T) {
		broker := newFakeConnection()
		transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", MaxInboundPayloadBytes: 4, PendingBytes: 5, PendingMessages: 2, StreamBuffer: 2})
		schema, model := golem.EventSchemaDigest{1}, golem.ModelID{2}
		_ = transport.BindEventRuntime(mapBinding{})
		requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{3}, schema, model)
		stream, err := transport.Subscribe(context.Background(), requested)
		if err != nil {
			t.Fatal(err)
		}
		broker.emit(transport.subject(schema, model), []byte("four"))
		broker.emit(transport.subject(schema, model), []byte("two"))
		if _, err := stream.Recv(context.Background()); eventCode(err) != events.CodeEventTransport {
			t.Fatalf("byte overflow error=%v", err)
		}
	})
}

func TestOrder7CloseIsConcurrentAndIdempotent(t *testing.T) {
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() { defer wait.Done(); _ = transport.Close() }()
	}
	wait.Wait()
	if broker.closes != 1 {
		t.Fatalf("connection closes=%d", broker.closes)
	}
}

func TestOrder7AvailabilityCallbacksAreClosedAndTerminalCloseUnblocksStreams(t *testing.T) {
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
	if !transport.TransportAvailable() || transport.MaxEncodedEventBytes() != defaultMaxInboundPayload {
		t.Fatalf("available=%t payload=%d", transport.TransportAvailable(), transport.MaxEncodedEventBytes())
	}
	transport.markDisconnected()
	if transport.TransportAvailable() {
		t.Fatal("disconnect remained available")
	}
	transport.markReconnected(maximumInboundPayload)
	if !transport.TransportAvailable() {
		t.Fatal("reconnect remained unavailable")
	}
	_ = transport.BindEventRuntime(mapBinding{})
	requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{1}, golem.EventSchemaDigest{2}, golem.ModelID{3})
	stream, err := transport.Subscribe(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	transport.markClosed()
	if transport.TransportAvailable() {
		t.Fatal("terminal close remained available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := stream.Recv(ctx); eventCode(err) != events.CodeEventTransport {
		t.Fatalf("terminal stream error=%v", err)
	}
	transport.markReconnected(maximumInboundPayload)
	if transport.TransportAvailable() {
		t.Fatal("late reconnect revived terminally closed transport")
	}
}

func TestOrder7ReconnectRejectsBrokerWithReducedPayloadCeiling(t *testing.T) {
	observer := &recordingObserver{}
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", MaxInboundPayloadBytes: 1024, Observer: observer})
	_ = transport.BindEventRuntime(mapBinding{})
	requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{1}, golem.EventSchemaDigest{2}, golem.ModelID{3})
	stream, err := transport.Subscribe(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	transport.markDisconnected()
	if transport.markReconnected(1023) {
		t.Fatal("reconnect accepted a smaller broker payload ceiling")
	}
	if transport.TransportAvailable() {
		t.Fatal("incompatible reconnect remained available")
	}
	if _, err := stream.Recv(context.Background()); eventCode(err) != events.CodeEventTransport {
		t.Fatalf("stream error=%v", err)
	}
	if len(observer.observations) != 2 || observer.observations[0].Outcome() != events.OutcomeFailure || observer.observations[1].Outcome() != events.OutcomeFailure {
		t.Fatalf("observations=%#v", observer.observations)
	}
}

func TestOrder7DisconnectCannotBeOverwrittenByOpenCompletion(t *testing.T) {
	normalized, err := normalizeConfig(Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	transport := &Transport{config: normalized, streams: make(map[*stream]struct{})}
	base := newFakeConnection()
	started := make(chan struct{})
	disconnected := make(chan struct{})
	client := &installBarrierConnection{fakeConnection: base, onConnectedCheck: func() {
		go func() {
			close(started)
			transport.markDisconnected()
			close(disconnected)
		}()
		<-started
	}}
	if !transport.installConnectedClient(client) {
		t.Fatal("connected client was not installed")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("disconnect callback remained blocked")
	}
	if transport.TransportAvailable() {
		t.Fatal("Open completion overwrote a concurrent disconnect")
	}
}

func TestOrder7ExplicitSubjectPrefixesIsolateDeployments(t *testing.T) {
	broker := newFakeConnection()
	first := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "database_one"})
	second := mustTestTransport(t, broker, Config{URLs: []string{"nats://test"}, SubjectPrefix: "database_two"})
	schema, model := golem.EventSchemaDigest{1}, golem.ModelID{2}
	firstNotice := mustNotice(t, golem.EventID{3}, golem.SchemaDigest{4}, schema, model, golem.CausationID{5}, 1, []byte("one"))
	_ = first.BindEventRuntime(mapBinding{"one": firstNotice})
	_ = second.BindEventRuntime(mapBinding{"one": firstNotice})
	requested, _ := eventvalue.NewRoutedSubscription(golem.SchemaDigest{4}, schema, model)
	firstStream, _ := first.Subscribe(context.Background(), requested)
	secondStream, _ := second.Subscribe(context.Background(), requested)
	batch, _ := eventvalue.NewEventBatch(golem.CausationID{5}, []events.Notice{firstNotice})
	if err := first.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if _, err := firstStream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := secondStream.Recv(ctx); eventCode(err) != events.CodeSubscriptionCancelled {
		t.Fatalf("foreign deployment received or wrong error: %v", err)
	}
}

func TestOrder7ReconnectObservationsAreExactClosedAndPanicSafe(t *testing.T) {
	observer := &recordingObserver{}
	broker := newFakeConnection()
	transport := mustTestTransport(t, broker, Config{URLs: []string{"nats://secret:credential@broker"}, SubjectPrefix: "private.subject", Observer: observer})
	transport.markDisconnected()
	transport.markReconnected(maximumInboundPayload)
	transport.markClosed()
	if len(observer.observations) != 3 {
		t.Fatalf("observation count=%d", len(observer.observations))
	}
	for index, observation := range observer.observations {
		wantOutcome := events.OutcomeFailure
		if index == 1 {
			wantOutcome = events.OutcomeSuccess
		}
		if observation.Kind() != events.ObservationTransportReconnect || observation.Outcome() != wantOutcome || observation.ModelID() != (golem.ModelID{}) || observation.Action() != "" || observation.SuppressionReason() != "" || observation.Attempt() != 0 || observation.QueueDepth() != 0 || observation.QueueLimit() != 0 || observation.Duration() != 0 || observation.AggregateCount() != 1 {
			t.Fatalf("observation[%d]=(%q,%q,%x,%q,%q,%d,%d,%d,%s,%d)", index, observation.Kind(), observation.Outcome(), observation.ModelID(), observation.Action(), observation.SuppressionReason(), observation.Attempt(), observation.QueueDepth(), observation.QueueLimit(), observation.Duration(), observation.AggregateCount())
		}
	}
	panicTransport := mustTestTransport(t, newFakeConnection(), Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", Observer: panickingObserver{}})
	panicTransport.markDisconnected()
	panicTransport.markReconnected(maximumInboundPayload)
	panicTransport.markClosed()
}

func TestOrder7ReconnectObservationsSerializeTransitionsAndSuppressDuplicates(t *testing.T) {
	observer := &barrierObserver{entered: make(chan struct{}), release: make(chan struct{})}
	defer observer.unblock()
	transport := mustTestTransport(t, newFakeConnection(), Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", Observer: observer})
	disconnected := make(chan struct{})
	go func() {
		transport.markDisconnected()
		close(disconnected)
	}()
	<-observer.entered
	reconnected := make(chan struct{})
	go func() {
		transport.markReconnected(maximumInboundPayload)
		close(reconnected)
	}()
	<-reconnected
	if observations := observer.snapshot(); len(observations) != 1 || observations[0].Outcome() != events.OutcomeFailure {
		t.Fatalf("reconnect observation overtook blocked disconnect: %#v", observations)
	}
	observer.unblock()
	<-disconnected
	deadline := time.Now().Add(time.Second)
	for len(observer.snapshot()) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	transport.markReconnected(maximumInboundPayload)
	observations := observer.snapshot()
	if len(observations) != 2 || observations[0].Outcome() != events.OutcomeFailure || observations[1].Outcome() != events.OutcomeSuccess {
		t.Fatalf("ordered observations=%#v", observations)
	}
}

func TestOrder7ReconnectObserverCanReenterCloseWithoutDeadlock(t *testing.T) {
	observer := &closingObserver{done: make(chan struct{})}
	transport, err := newTransportForTest(Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", Observer: observer}, newFakeConnection())
	if err != nil {
		t.Fatal(err)
	}
	observer.transport = transport
	go transport.markDisconnected()
	select {
	case <-observer.done:
	case <-time.After(time.Second):
		t.Fatal("observer calling Close deadlocked transport callback")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.TransportAvailable() {
		t.Fatal("reentrant Close revived availability")
	}
}

func TestOrder7BlockedObserverCannotGrowReconnectQueueWithoutBound(t *testing.T) {
	const expectedBound = 64
	observer := &barrierObserver{entered: make(chan struct{}), release: make(chan struct{})}
	defer observer.unblock()
	transport := mustTestTransport(t, newFakeConnection(), Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", Observer: observer})
	first := make(chan struct{})
	go func() {
		transport.markDisconnected()
		close(first)
	}()
	<-observer.entered
	for index := 0; index < 2048; index++ {
		transport.markReconnected(maximumInboundPayload)
		transport.markDisconnected()
	}
	transport.observationMu.Lock()
	pending := len(transport.observations)
	transport.observationMu.Unlock()
	if pending != expectedBound {
		t.Fatalf("pending reconnect observations=%d want hard bound %d", pending, expectedBound)
	}
	if transport.TransportAvailable() {
		t.Fatal("observation pressure changed final disconnected state")
	}
	observer.unblock()
	<-first
}

func TestOrder7IntentionalCloseDoesNotReportReconnectFailure(t *testing.T) {
	observer := &recordingObserver{}
	transport := mustTestTransport(t, newFakeConnection(), Config{URLs: []string{"nats://test"}, SubjectPrefix: "deployment", Observer: observer})
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	transport.markDisconnected()
	transport.markReconnected(maximumInboundPayload)
	transport.markClosed()
	if len(observer.observations) != 0 {
		t.Fatalf("intentional close observations=%d", len(observer.observations))
	}
}

type recordingObserver struct {
	mu           sync.Mutex
	observations []events.Observation
}

func (observer *recordingObserver) ObserveEvent(_ context.Context, observation events.Observation) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observations = append(observer.observations, observation)
}

type barrierObserver struct {
	mu           sync.Mutex
	observations []events.Observation
	entered      chan struct{}
	release      chan struct{}
	first        bool
	enterOnce    sync.Once
	releaseOnce  sync.Once
}

func (observer *barrierObserver) ObserveEvent(_ context.Context, observation events.Observation) {
	observer.mu.Lock()
	observer.observations = append(observer.observations, observation)
	first := !observer.first
	observer.first = true
	observer.mu.Unlock()
	if first {
		observer.enterOnce.Do(func() {
			close(observer.entered)
		})
		<-observer.release
	}
}

func (observer *barrierObserver) unblock() {
	observer.releaseOnce.Do(func() { close(observer.release) })
}

func (observer *barrierObserver) snapshot() []events.Observation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]events.Observation(nil), observer.observations...)
}

type panickingObserver struct{}

func (panickingObserver) ObserveEvent(context.Context, events.Observation) { panic("secret") }

type closingObserver struct {
	transport *Transport
	done      chan struct{}
	once      sync.Once
}

func (observer *closingObserver) ObserveEvent(context.Context, events.Observation) {
	observer.once.Do(func() {
		_ = observer.transport.Close()
		close(observer.done)
	})
}

type mapBinding map[string]events.Notice

func (binding mapBinding) DecodeNotice(_ context.Context, payload []byte) (events.Notice, error) {
	result, ok := binding[string(payload)]
	if !ok {
		return events.Notice{}, events.Failure(events.CodeEventCodec)
	}
	return result, nil
}

func conformanceBinding(t testing.TB) mapBinding {
	t.Helper()
	return mapBinding{
		"first":                 mustNotice(t, golem.EventID{1}, golem.SchemaDigest{1}, golem.EventSchemaDigest{1}, golem.ModelID{2}, golem.CausationID{4}, 1, []byte("first")),
		"second":                mustNotice(t, golem.EventID{2}, golem.SchemaDigest{1}, golem.EventSchemaDigest{1}, golem.ModelID{2}, golem.CausationID{4}, 2, []byte("second")),
		"third":                 mustNotice(t, golem.EventID{3}, golem.SchemaDigest{1}, golem.EventSchemaDigest{1}, golem.ModelID{3}, golem.CausationID{4}, 3, []byte("third")),
		string([]byte{9, 8, 7}): mustNotice(t, golem.EventID{8}, golem.SchemaDigest{5}, golem.EventSchemaDigest{5}, golem.ModelID{6}, golem.CausationID{7}, 1, []byte{9, 8, 7}),
	}
}

func mustNotice(t testing.TB, eventID golem.EventID, generation golem.SchemaDigest, eventSchema golem.EventSchemaDigest, model golem.ModelID, causation golem.CausationID, ordinal uint32, payload []byte) events.Notice {
	t.Helper()
	result, err := eventvalue.NewRoutedNotice(eventID, generation, eventSchema, model, golem.EventCreated, causation, ordinal, payload)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustTestTransport(t testing.TB, broker connection, config Config) *Transport {
	t.Helper()
	result, err := newTransportForTest(config, broker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Close() })
	return result
}

func eventCode(err error) events.ErrorCode {
	code, _ := events.CodeOf(err)
	return code
}

type publishedMessage struct {
	subject string
	payload []byte
}

type fakeConnection struct {
	mu            sync.Mutex
	subscribers   map[string][]*fakeSubscription
	published     []publishedMessage
	flushes       int
	flushDeadline bool
	closes        int
	maxPayload    int64
	flushErr      error
}

type installBarrierConnection struct {
	*fakeConnection
	onConnectedCheck func()
}

type concurrentPublishConnection struct {
	*fakeConnection
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondEntered chan struct{}
}

func (connection *concurrentPublishConnection) Publish(subject string, payload []byte) error {
	switch string(payload) {
	case "first":
		close(connection.firstEntered)
		<-connection.releaseFirst
	case "second":
		close(connection.secondEntered)
	}
	return connection.fakeConnection.Publish(subject, payload)
}

func (connection *installBarrierConnection) IsConnected() bool {
	if connection.onConnectedCheck != nil {
		connection.onConnectedCheck()
	}
	return true
}

func newFakeConnection() *fakeConnection {
	return &fakeConnection{subscribers: make(map[string][]*fakeSubscription), maxPayload: maximumInboundPayload}
}

func (connection *fakeConnection) Publish(subject string, payload []byte) error {
	connection.mu.Lock()
	connection.published = append(connection.published, publishedMessage{subject: subject, payload: append([]byte(nil), payload...)})
	subscribers := append([]*fakeSubscription(nil), connection.subscribers[subject]...)
	connection.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber.deliver(subject, payload)
	}
	return nil
}

func (connection *fakeConnection) FlushWithContext(ctx context.Context) error {
	connection.mu.Lock()
	connection.flushes++
	_, connection.flushDeadline = ctx.Deadline()
	connection.mu.Unlock()
	return connection.flushErr
}

func (connection *fakeConnection) Subscribe(subject string, callback natsclient.MsgHandler) (subscription, error) {
	result := &fakeSubscription{owner: connection, subject: subject, callback: callback}
	connection.mu.Lock()
	connection.subscribers[subject] = append(connection.subscribers[subject], result)
	connection.mu.Unlock()
	return result, nil
}

func (connection *fakeConnection) Close() {
	connection.mu.Lock()
	connection.closes++
	connection.mu.Unlock()
}

func (connection *fakeConnection) MaxPayload() int64 { return connection.maxPayload }
func (connection *fakeConnection) IsConnected() bool { return true }

func (connection *fakeConnection) emit(subject string, payload []byte) {
	connection.emitAs(subject, subject, payload)
}

func (connection *fakeConnection) emitAs(registeredSubject, messageSubject string, payload []byte) {
	connection.mu.Lock()
	subscribers := append([]*fakeSubscription(nil), connection.subscribers[registeredSubject]...)
	connection.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber.deliver(messageSubject, payload)
	}
}

func (connection *fakeConnection) calls() int {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return len(connection.published) + connection.flushes + len(connection.subscribers) + connection.closes
}

type fakeSubscription struct {
	owner        *fakeConnection
	subject      string
	callback     natsclient.MsgHandler
	mu           sync.Mutex
	unsubscribed bool
}

func (subscription *fakeSubscription) SetPendingLimits(_, _ int) error { return nil }
func (subscription *fakeSubscription) Unsubscribe() error {
	subscription.mu.Lock()
	subscription.unsubscribed = true
	subscription.mu.Unlock()
	return nil
}
func (subscription *fakeSubscription) deliver(subject string, payload []byte) {
	subscription.mu.Lock()
	closed := subscription.unsubscribed
	subscription.mu.Unlock()
	if !closed {
		subscription.callback(&natsclient.Msg{Subject: subject, Data: append([]byte(nil), payload...)})
	}
}
