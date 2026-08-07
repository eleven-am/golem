package cdc

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/events/cdctest"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
)

func TestP7CDCCommonAdapterConformance(t *testing.T) {
	store := &checkpointStore{value: []byte("start")}
	input := validBatch(t)
	cdctest.Run(t, cdctest.Fixture{
		NewAdapter: func() events.CDCAdapter { return &fakeAdapter{identity: testCDCIdentity(), input: input, store: store} },
		Checkpoint: store.load, InitialCheckpoint: []byte("start"), AcceptedCheckpoint: input.Cursor,
		AssertBatch: func(t testing.TB, batch events.CDCBatchInput) {
			if batch.SourceTransactionID != input.SourceTransactionID || len(batch.Changes) != 3 {
				t.Fatal("adapter emitted wrong causal batch")
			}
		},
	})
}

func TestP7CDCReplayDerivesStableEventIDs(t *testing.T) {
	transport := &captureTransport{}
	emitter := testEmitter(t, transport, &fakeEncoder{}, &fakeCorrelator{})
	input := validBatch(t)
	if err := emitter.Emit(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Emit(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	batches := transport.snapshot()
	if len(batches) != 2 {
		t.Fatalf("published batches = %d", len(batches))
	}
	if batches[0].CausationID() != batches[1].CausationID() {
		t.Fatal("replay changed causation ID")
	}
	left, right := batches[0].Events(), batches[1].Events()
	for index := range left {
		if left[index].EventID() != right[index].EventID() || !bytes.Equal(left[index].Encoded(), right[index].Encoded()) {
			t.Fatalf("replay changed event %d", index)
		}
	}
	if left[0].EventID() == left[1].EventID() {
		t.Fatal("different ordinals shared event ID")
	}
	if deriveEventID(testCDCIdentity(), "wal:44/7", 1) == deriveEventID(testCDCIdentity(), "wal:44/8", 1) {
		t.Fatal("different source transactions shared event ID")
	}
	changed := testCDCIdentity()
	changed.Version = "1.0.1+rebuild"
	if deriveEventID(testCDCIdentity(), "wal:44/7", 1) == deriveEventID(changed, "wal:44/7", 1) {
		t.Fatal("different adapter identities shared event ID")
	}
}

func TestP7CDCCheckpointAdvancesOnlyAfterTransportAcceptance(t *testing.T) {
	store := &checkpointStore{value: []byte("old")}
	input := validBatch(t)
	transport := &captureTransport{failure: errors.New("private broker failure")}
	emitter := testEmitter(t, transport, &fakeEncoder{}, &fakeCorrelator{})
	adapter := &fakeAdapter{identity: testCDCIdentity(), input: input, store: store}
	if err := adapter.Run(context.Background(), emitter); eventCode(t, err) != events.CodeCDCUnavailable {
		t.Fatalf("failed publish error = %v", err)
	}
	if !bytes.Equal(store.load(), []byte("old")) {
		t.Fatal("checkpoint advanced after failed acceptance")
	}
	transport.failure = nil
	if err := adapter.Run(context.Background(), emitter); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(store.load(), input.Cursor) {
		t.Fatal("checkpoint did not advance after acceptance")
	}
}

func TestP7CDCGolemTransactionCorrelationAvoidsSecondEvent(t *testing.T) {
	transport := &captureTransport{}
	encoder := &fakeEncoder{}
	correlator := &fakeCorrelator{correlated: true}
	emitter := testEmitter(t, transport, encoder, correlator)
	if err := emitter.Emit(context.Background(), validBatch(t)); err != nil {
		t.Fatal(err)
	}
	if encoder.calls.Load() != 0 || len(transport.snapshot()) != 0 || correlator.calls.Load() != 1 {
		t.Fatal("correlated Golem transaction entered codec or transport")
	}
}

func TestP7CDCValidatesOrdinalsActionsModelsAndImagesBeforeEncoding(t *testing.T) {
	base := validBatch(t)
	foreign, _ := golem.RuntimeModelReadRow(golem.ModelID{9})
	tests := map[string]func(*events.CDCBatchInput){
		"ordinal gap":           func(input *events.CDCBatchInput) { input.Changes[1].Ordinal = 3 },
		"unknown action":        func(input *events.CDCBatchInput) { input.Changes[0].Action = "merge" },
		"created before":        func(input *events.CDCBatchInput) { input.Changes[0].Before = input.Changes[0].After },
		"updated missing after": func(input *events.CDCBatchInput) { input.Changes[1].After = nil },
		"deleted after":         func(input *events.CDCBatchInput) { input.Changes[2].After = input.Changes[2].Before },
		"row model":             func(input *events.CDCBatchInput) { input.Changes[0].After = &foreign },
		"empty transaction":     func(input *events.CDCBatchInput) { input.SourceTransactionID = "" },
		"zero recorded time":    func(input *events.CDCBatchInput) { input.RecordedAt = time.Time{} },
		"non UTC recorded time": func(input *events.CDCBatchInput) {
			input.RecordedAt = input.RecordedAt.In(time.FixedZone("offset", 3600))
		},
		"submicro recorded time": func(input *events.CDCBatchInput) {
			input.RecordedAt = input.RecordedAt.Add(time.Nanosecond)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := clonePublicBatch(base)
			mutate(&input)
			encoder := &fakeEncoder{}
			transport := &captureTransport{}
			err := testEmitter(t, transport, encoder, &fakeCorrelator{}).Emit(context.Background(), input)
			if eventCode(t, err) != events.CodeCDCInvalid || encoder.calls.Load() != 0 || len(transport.snapshot()) != 0 {
				t.Fatalf("invalid input escaped validation: %v", err)
			}
		})
	}
}

func TestP7CDCEmitterOwnsCursorAndChangeSlices(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	correlator := &fakeCorrelator{entered: entered, release: release}
	encoder := &fakeEncoder{}
	emitter := testEmitter(t, &captureTransport{}, encoder, correlator)
	input := validBatch(t)
	done := make(chan error, 1)
	go func() { done <- emitter.Emit(context.Background(), input) }()
	<-entered
	input.Cursor[0] = 0
	input.Changes[0].Model = golem.ModelID{9}
	input.Changes = nil
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	seen := encoder.snapshot()
	if len(seen) != 3 || seen[0].Model != (golem.ModelID{2}) || !bytes.Equal(seen[0].Cursor, []byte("cursor-2")) {
		t.Fatal("emitter retained caller cursor/change storage")
	}
}

func TestP7CDCRejectsEncoderMetadataMismatch(t *testing.T) {
	encoder := &fakeEncoder{wrongEventID: true}
	transport := &captureTransport{}
	err := testEmitter(t, transport, encoder, &fakeCorrelator{}).Emit(context.Background(), validBatch(t))
	if eventCode(t, err) != events.CodeCDCInvalid || len(transport.snapshot()) != 0 {
		t.Fatalf("mismatched encoded notice accepted: %v", err)
	}
}

func TestP7CDCExactImageValidationFailureFromSharedEncoderStopsPublication(t *testing.T) {
	encoder := &fakeEncoder{failure: errors.New("missing required schema field")}
	transport := &captureTransport{}
	err := testEmitter(t, transport, encoder, &fakeCorrelator{}).Emit(context.Background(), validBatch(t))
	if eventCode(t, err) != events.CodeCDCInvalid || encoder.calls.Load() != 1 || len(transport.snapshot()) != 0 {
		t.Fatalf("shared encoder validation failure escaped: %v", err)
	}
}

func TestP7CDCDisabledCapabilitiesAreExplicit(t *testing.T) {
	identities, observed, err := events.CDCAdapterCapabilities(golem.PostgreSQL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if observed || len(identities) != 0 {
		t.Fatal("no-adapter capabilities claim external writes")
	}
	capabilities := events.RuntimeCapabilities(golem.PostgreSQL, nil, nil, events.TransportCapabilities{}, false, false, nil, identities, observed)
	if capabilities.ExternalWritesObserved() {
		t.Fatal("disabled CDC capability changed")
	}
}

func validBatch(t testing.TB) events.CDCBatchInput {
	t.Helper()
	model := golem.ModelID{2}
	before, err := golem.RuntimeCDCModelRow(model)
	if err != nil {
		t.Fatal(err)
	}
	after, err := golem.RuntimeCDCModelRow(model)
	if err != nil {
		t.Fatal(err)
	}
	return events.CDCBatchInput{SourceTransactionID: "wal:44/7", RecordedAt: time.Date(2026, 8, 7, 12, 34, 56, 987654000, time.UTC), Cursor: []byte("cursor-2"), Changes: []events.CDCChangeInput{
		{Ordinal: 1, Model: model, Action: golem.EventCreated, After: &after},
		{Ordinal: 2, Model: model, Action: golem.EventUpdated, Before: &before, After: &after},
		{Ordinal: 3, Model: model, Action: golem.EventDeleted, Before: &before},
	}}
}

func testCDCIdentity() events.CDCIdentity {
	return events.CDCIdentity{Name: "fake-wal", Version: "1.0.0", Provider: golem.PostgreSQL}
}
func testEmitter(t testing.TB, transport events.EventTransport, encoder Encoder, correlator Correlator) *Emitter {
	t.Helper()
	emitter, err := NewEmitter(Config{Adapter: &emitterTestAdapter{correlator: correlator}, Transport: transport, Encoder: encoder})
	if err != nil {
		t.Fatal(err)
	}
	return emitter
}

type emitterTestAdapter struct{ correlator Correlator }

func (*emitterTestAdapter) Identity() events.CDCIdentity { return testCDCIdentity() }
func (adapter *emitterTestAdapter) CorrelatesGolemTransaction(ctx context.Context, input events.CDCCorrelationInput) (bool, error) {
	return adapter.correlator.GolemTransaction(ctx, CorrelationInput{adapter: testCDCIdentity(), sourceTransactionID: input.SourceTransactionID(), cursor: input.Cursor()})
}
func (*emitterTestAdapter) Run(context.Context, events.CDCEmitter) error { return nil }

type fakeEncoder struct {
	calls        atomic.Int64
	mu           sync.Mutex
	inputs       []EncodeInput
	wrongEventID bool
	failure      error
}

func (encoder *fakeEncoder) EncodeCDC(_ context.Context, input EncodeInput) (events.Notice, error) {
	encoder.calls.Add(1)
	encoder.mu.Lock()
	encoder.inputs = append(encoder.inputs, cloneEncodeInput(input))
	encoder.mu.Unlock()
	if encoder.failure != nil {
		return events.Notice{}, encoder.failure
	}
	eventID := input.EventID
	if encoder.wrongEventID {
		eventID[0] ^= 0xff
	}
	encoded := append(append([]byte(nil), eventID[:]...), byte(input.Ordinal))
	return eventvalue.NewNotice(eventID, golem.SchemaDigest{7}, input.Model, input.Action, input.CausationID, input.Ordinal, encoded)
}
func (encoder *fakeEncoder) snapshot() []EncodeInput {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	result := make([]EncodeInput, len(encoder.inputs))
	for index, input := range encoder.inputs {
		result[index] = cloneEncodeInput(input)
	}
	return result
}
func cloneEncodeInput(input EncodeInput) EncodeInput {
	input.Cursor = append([]byte(nil), input.Cursor...)
	input.Before = cloneRowPointer(input.Before)
	input.After = cloneRowPointer(input.After)
	return input
}

type fakeCorrelator struct {
	correlated       bool
	failure          error
	calls            atomic.Int64
	entered, release chan struct{}
}

func (correlator *fakeCorrelator) GolemTransaction(context.Context, CorrelationInput) (bool, error) {
	correlator.calls.Add(1)
	if correlator.entered != nil {
		close(correlator.entered)
		<-correlator.release
	}
	return correlator.correlated, correlator.failure
}

type captureTransport struct {
	mu      sync.Mutex
	batches []events.EventBatch
	failure error
}

func (transport *captureTransport) Publish(_ context.Context, batch events.EventBatch) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.batches = append(transport.batches, batch)
	return transport.failure
}
func (*captureTransport) Subscribe(context.Context, events.Subscription) (events.Stream, error) {
	return nil, errors.New("unused")
}
func (transport *captureTransport) snapshot() []events.EventBatch {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]events.EventBatch(nil), transport.batches...)
}

type checkpointStore struct {
	mu    sync.Mutex
	value []byte
}

func (store *checkpointStore) load() []byte {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]byte(nil), store.value...)
}
func (store *checkpointStore) save(value []byte) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.value = append([]byte(nil), value...)
}

type fakeAdapter struct {
	identity events.CDCIdentity
	input    events.CDCBatchInput
	store    *checkpointStore
}

func (adapter *fakeAdapter) Identity() events.CDCIdentity { return adapter.identity }
func (*fakeAdapter) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return true, nil
}
func (adapter *fakeAdapter) Run(ctx context.Context, emitter events.CDCEmitter) error {
	input := clonePublicBatch(adapter.input)
	if err := emitter.Emit(ctx, input); err != nil {
		return err
	}
	adapter.store.save(input.Cursor)
	return nil
}
func clonePublicBatch(input events.CDCBatchInput) events.CDCBatchInput {
	result := events.CDCBatchInput{SourceTransactionID: input.SourceTransactionID, RecordedAt: input.RecordedAt, Cursor: append([]byte(nil), input.Cursor...), Changes: make([]events.CDCChangeInput, len(input.Changes))}
	copy(result.Changes, input.Changes)
	return result
}

func eventCode(t testing.TB, err error) events.ErrorCode {
	t.Helper()
	code, ok := events.CodeOf(err)
	if !ok {
		t.Fatalf("error = %v; want events.Error", err)
	}
	return code
}
