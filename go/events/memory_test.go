package events

import (
	"bytes"
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/event/value"
)

func TestTransportCausalBatchConformance(t *testing.T) {
	generation, model, causation := golem.SchemaDigest{1}, golem.ModelID{2}, golem.CausationID{3}
	notices := []Notice{
		mustNotice(t, golem.EventID{1}, generation, model, causation, 1, []byte("first")),
		mustNotice(t, golem.EventID{2}, generation, model, causation, 2, []byte("second")),
	}
	batch, err := internalvalue.NewEventBatch(causation, notices)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := NewMemoryTransport(MemoryLimits{Buffer: 2})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := internalvalue.NewSubscription(generation, model)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := transport.Subscribe(context.Background(), subscription)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if err := transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	for index, want := range notices {
		got, err := stream.Recv(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.EventID() != want.EventID() || got.TransactionOrdinal() != uint32(index+1) || !bytes.Equal(got.Encoded(), want.Encoded()) {
			t.Fatalf("notice %d changed across transport", index)
		}
	}
}

func TestRetryReusesExactIDsAndBytes(t *testing.T) {
	generation, model, causation := golem.SchemaDigest{1}, golem.ModelID{2}, golem.CausationID{3}
	encoded := []byte{9, 8, 7}
	notice := mustNotice(t, golem.EventID{4}, generation, model, causation, 1, encoded)
	encoded[0] = 0
	batch, err := internalvalue.NewEventBatch(causation, []Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	transport, _ := NewMemoryTransport(MemoryLimits{Buffer: 2})
	subscription, _ := internalvalue.NewSubscription(generation, model)
	stream, err := transport.Subscribe(context.Background(), subscription)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	for attempt := 0; attempt < 2; attempt++ {
		if err := transport.Publish(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		got, err := stream.Recv(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.EventID() != notice.EventID() || !bytes.Equal(got.Encoded(), []byte{9, 8, 7}) {
			t.Fatalf("retry %d changed event ID or bytes", attempt)
		}
		owned := got.Encoded()
		owned[0] = 0
		if !bytes.Equal(got.Encoded(), []byte{9, 8, 7}) {
			t.Fatal("Encoded returned aliased storage")
		}
	}
}

func TestMemoryTransportRoutesCompatibleHistoricalGenerationByEventSchema(t *testing.T) {
	activeGeneration := golem.SchemaDigest{1}
	historicalGeneration := golem.SchemaDigest{2}
	eventSchema := golem.EventSchemaDigest{3}
	model, causation := golem.ModelID{4}, golem.CausationID{5}
	notice, err := internalvalue.NewRoutedNotice(golem.EventID{6}, historicalGeneration, eventSchema, model, golem.EventCreated, causation, 1, []byte{7})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := internalvalue.NewEventBatch(causation, []Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := internalvalue.NewRoutedSubscription(activeGeneration, eventSchema, model)
	if err != nil {
		t.Fatal(err)
	}
	transport, _ := NewMemoryTransport(MemoryLimits{Buffer: 1})
	stream, err := transport.Subscribe(context.Background(), subscription)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	got, err := stream.Recv(context.Background())
	if err != nil || got.GenerationDigest() != historicalGeneration || got.EventSchemaDigest() != eventSchema {
		t.Fatalf("historical routed notice generation=%x schema=%x err=%v", got.GenerationDigest(), got.EventSchemaDigest(), err)
	}
}

func TestMemoryTransportIsBoundedAndCapabilityIsProcessLocal(t *testing.T) {
	generation, model, causation := golem.SchemaDigest{1}, golem.ModelID{2}, golem.CausationID{3}
	notice := mustNotice(t, golem.EventID{4}, generation, model, causation, 1, []byte{1})
	batch, _ := internalvalue.NewEventBatch(causation, []Notice{notice})
	transport, err := NewMemoryTransport(MemoryLimits{Buffer: 1})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := CapabilitiesOf(transport)
	if capabilities.Scope() != TransportScopeProcessLocal || capabilities.Durable() || capabilities.Identity() == "" {
		t.Fatalf("memory capabilities = (%q, %q, %t)", capabilities.Identity(), capabilities.Scope(), capabilities.Durable())
	}
	subscription, _ := internalvalue.NewSubscription(generation, model)
	stream, err := transport.Subscribe(context.Background(), subscription)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if err := transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if code := errorCode(t, transport.Publish(context.Background(), batch)); code != CodeEventTransport {
		t.Fatalf("full buffer error = %q", code)
	}
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := transport.Publish(context.Background(), batch); err != nil {
		t.Fatalf("exact capacity was not reusable: %v", err)
	}
}

func TestTransportCapabilitiesRequireCanonicalIdentityAndClosedScope(t *testing.T) {
	valid, err := NewTransportCapabilities("acme.kafka-v2", TransportScopeCrossProcess, true)
	if err != nil {
		t.Fatal(err)
	}
	if valid.Identity() != "acme.kafka-v2" || valid.Scope() != TransportScopeCrossProcess || !valid.Durable() {
		t.Fatalf("validated capabilities changed: (%q,%q,%t)", valid.Identity(), valid.Scope(), valid.Durable())
	}
	for name, input := range map[string]struct {
		identity string
		scope    TransportScope
	}{
		"empty identity":     {scope: TransportScopeCrossProcess},
		"noncanonical":       {identity: "broker with spaces", scope: TransportScopeCrossProcess},
		"oversized identity": {identity: string(bytes.Repeat([]byte{'a'}, MaximumTransportIdentityBytes+1)), scope: TransportScopeCrossProcess},
		"unknown scope":      {identity: "broker.v1", scope: "internet"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTransportCapabilities(input.identity, input.scope, false); errorCode(t, err) != CodeEventConfig {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestExternalTransportRuntimeBindingSPIFreezesDecoderCapability(t *testing.T) {
	var _ RuntimeBindableTransport = (*bindingProbeTransport)(nil)
	var _ RuntimeBinding = bindingProbeDecoder{}
	transport := &bindingProbeTransport{}
	decoder := bindingProbeDecoder{}
	if err := transport.BindEventRuntime(decoder); err != nil {
		t.Fatal(err)
	}
	if transport.binding == nil {
		t.Fatal("runtime binding was not retained")
	}
	if err := transport.BindEventRuntime(decoder); errorCode(t, err) != CodeEventConfig {
		t.Fatalf("second binding error=%v", err)
	}
}

func TestMemoryTransportRejectsPartialCausalBatchAtBoundary(t *testing.T) {
	generation, model, causation := golem.SchemaDigest{1}, golem.ModelID{2}, golem.CausationID{3}
	first := mustNotice(t, golem.EventID{1}, generation, model, causation, 1, []byte{1})
	second := mustNotice(t, golem.EventID{2}, generation, model, causation, 2, []byte{2})
	batch, _ := internalvalue.NewEventBatch(causation, []Notice{first, second})
	transport, _ := NewMemoryTransport(MemoryLimits{Buffer: 1})
	subscription, _ := internalvalue.NewSubscription(generation, model)
	stream, err := transport.Subscribe(context.Background(), subscription)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if code := errorCode(t, transport.Publish(context.Background(), batch)); code != CodeEventTransport {
		t.Fatalf("code = %q", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Recv(ctx); errorCode(t, err) != CodeSubscriptionCancelled {
		t.Fatal("failed batch left a partial notice queued")
	}
}

func TestMemoryTransportCancellationClosesMembership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport, _ := NewMemoryTransport(MemoryLimits{Buffer: 1})
	subscription, _ := internalvalue.NewSubscription(golem.SchemaDigest{1}, golem.ModelID{2})
	stream, err := transport.Subscribe(ctx, subscription)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := stream.Recv(context.Background()); errorCode(t, err) != CodeSubscriptionCancelled {
		t.Fatalf("Recv after owner cancellation = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryStreamStopInstallerHandlesBothCloseOrderings(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		name := "install-before-close"
		if closeFirst {
			name = "close-before-install"
		}
		t.Run(name, func(t *testing.T) {
			transport := &memoryTransport{buffer: 1, streams: make(map[uint64]*memoryStream)}
			stream := &memoryStream{transport: transport, id: 1, queue: make(chan Notice, 1)}
			transport.streams[stream.id] = stream
			stops := 0
			stop := func() bool {
				stops++
				return true
			}
			if closeFirst {
				if err := stream.closeWith(CodeSubscriptionCancelled); err != nil {
					t.Fatal(err)
				}
				stream.installStop(stop)
			} else {
				stream.installStop(stop)
				if err := stream.closeWith(CodeSubscriptionCancelled); err != nil {
					t.Fatal(err)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if stops != 1 {
				t.Fatalf("stop callback count=%d", stops)
			}
			if _, present := transport.streams[stream.id]; present {
				t.Fatal("closed memory stream remains registered")
			}
			stream.stopMu.Lock()
			closed, retainedStop := stream.closed, stream.stop
			stream.stopMu.Unlock()
			if !closed || retainedStop != nil {
				t.Fatalf("closed=%t retainedStop=%t", closed, retainedStop != nil)
			}
		})
	}
}

func TestLimitsRejectInsteadOfClamping(t *testing.T) {
	if _, err := NormalizeLimits(Limits{SubscriberQueue: 4097}); errorCode(t, err) != CodeEventConfig {
		t.Fatal("oversize subscriber queue accepted")
	}
	if _, err := NormalizeLimits(Limits{RetryBase: 2, RetryCap: 1}); errorCode(t, err) != CodeEventConfig {
		t.Fatal("contradictory retry limits accepted")
	}
	normalized, err := NormalizeLimits(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SubscriberQueue != 64 || normalized.HubInputQueue != 256 || normalized.EvaluationConcurrency != 32 {
		t.Fatalf("unexpected defaults: %+v", normalized)
	}
}

func mustNotice(t testing.TB, eventID golem.EventID, generation golem.SchemaDigest, model golem.ModelID, causation golem.CausationID, ordinal uint32, encoded []byte) Notice {
	t.Helper()
	notice, err := internalvalue.NewNotice(eventID, generation, model, golem.EventCreated, causation, ordinal, encoded)
	if err != nil {
		t.Fatal(err)
	}
	return notice
}

func errorCode(t testing.TB, err error) ErrorCode {
	t.Helper()
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("error = %v; want closed events error", err)
	}
	return code
}

type bindingProbeTransport struct{ binding RuntimeBinding }

func (transport *bindingProbeTransport) Publish(context.Context, EventBatch) error { return nil }
func (transport *bindingProbeTransport) Subscribe(context.Context, Subscription) (Stream, error) {
	return nil, Failure(CodeEventTransport)
}
func (transport *bindingProbeTransport) BindEventRuntime(binding RuntimeBinding) error {
	if binding == nil || transport.binding != nil {
		return Failure(CodeEventConfig)
	}
	transport.binding = binding
	return nil
}

type bindingProbeDecoder struct{}

func (bindingProbeDecoder) DecodeNotice(context.Context, []byte) (Notice, error) {
	return Notice{}, Failure(CodeEventCodec)
}
