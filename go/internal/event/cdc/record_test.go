package cdc

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

type orderingRecorder struct {
	fakeRecorder
	transport *captureTransport
	published int
}

func (recorder *orderingRecorder) RecordCDC(ctx context.Context, causation golem.CausationID, changes []Change, build func([]Stamp) ([]mutationfact.OutboxRow, error)) error {
	recorder.published = len(recorder.transport.snapshot())
	return recorder.fakeRecorder.RecordCDC(ctx, causation, changes, build)
}

func TestCDCRecordsTheCanonicalFactsBeforePublishing(t *testing.T) {
	transport := &captureTransport{}
	recorder := &orderingRecorder{transport: transport}
	input := validBatch(t)
	if err := testRecordingEmitter(t, transport, &fakeEncoder{}, &fakeCorrelator{}, recorder).Emit(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	recorded := recorder.snapshot()
	if len(recorded) != 1 || len(recorded[0]) != len(input.Changes) || recorder.published != 0 {
		t.Fatalf("recorded=%d published before record=%d", len(recorded), recorder.published)
	}
	batches := transport.snapshot()
	if len(batches) != 1 {
		t.Fatalf("published batches=%d", len(batches))
	}
	for index, row := range recorded[0] {
		notice := batches[0].Events()[index]
		if row.EventID != golem.UUID(notice.EventID()).String() || row.CausationID != golem.UUID(batches[0].CausationID()).String() || row.TransactionOrdinal != int64(index+1) {
			t.Fatalf("recorded fact %d differs from the published notice", index)
		}
	}
}

func TestCDCRecordFailurePublishesNothing(t *testing.T) {
	for name, failure := range map[string]struct {
		err  error
		code events.ErrorCode
	}{
		"unavailable": {err: errors.New("database is closed"), code: events.CodeCDCUnavailable},
		"rejected":    {err: events.Failure(events.CodeCDCInvalid), code: events.CodeCDCInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &captureTransport{}
			recorder := &fakeRecorder{failure: failure.err}
			err := testRecordingEmitter(t, transport, &fakeEncoder{}, &fakeCorrelator{}, recorder).Emit(context.Background(), validBatch(t))
			if eventCode(t, err) != failure.code || len(transport.snapshot()) != 0 || len(recorder.snapshot()) != 1 {
				t.Fatalf("record failure error=%v published=%d", err, len(transport.snapshot()))
			}
		})
	}
}

func TestCDCRejectsEncoderFactMismatchBeforeRecording(t *testing.T) {
	transport, recorder := &captureTransport{}, &fakeRecorder{}
	err := testRecordingEmitter(t, transport, &fakeEncoder{wrongFact: true}, &fakeCorrelator{}, recorder).Emit(context.Background(), validBatch(t))
	if eventCode(t, err) != events.CodeCDCInvalid || len(recorder.snapshot()) != 0 || len(transport.snapshot()) != 0 {
		t.Fatalf("mismatched outbox fact accepted: %v", err)
	}
}

func TestCDCOversizedBatchIsNeitherRecordedNorPublished(t *testing.T) {
	transport, recorder := &captureTransport{}, &fakeRecorder{}
	emitter, err := NewEmitter(Config{Adapter: &emitterTestAdapter{correlator: &fakeCorrelator{}}, Transport: transport, Encoder: &fakeEncoder{encodedBytes: 32}, Recorder: recorder, MaxBatchBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	if err := emitter.Emit(context.Background(), validBatch(t)); eventCode(t, err) != events.CodeCDCInvalid || len(recorder.snapshot()) != 0 || len(transport.snapshot()) != 0 {
		t.Fatalf("oversized batch error=%v", err)
	}
}

func TestCDCCorrelatedGolemTransactionIsNotRecorded(t *testing.T) {
	transport, recorder := &captureTransport{}, &fakeRecorder{}
	if err := testRecordingEmitter(t, transport, &fakeEncoder{}, &fakeCorrelator{correlated: true}, recorder).Emit(context.Background(), validBatch(t)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.snapshot()) != 0 || len(transport.snapshot()) != 0 {
		t.Fatal("correlated Golem transaction was recorded or published")
	}
}

func TestCDCEmitterRequiresARecorder(t *testing.T) {
	_, err := NewEmitter(Config{Adapter: &emitterTestAdapter{correlator: &fakeCorrelator{}}, Transport: &captureTransport{}, Encoder: &fakeEncoder{}})
	if eventCode(t, err) != events.CodeCDCInvalid {
		t.Fatalf("emitter without a recorder error=%v", err)
	}
}

func TestCDCEncodesEveryFactWithTheVersionTheRecorderDrew(t *testing.T) {
	encoder := &fakeEncoder{}
	if err := testRecordingEmitter(t, &captureTransport{}, encoder, &fakeCorrelator{}, &fakeRecorder{}).Emit(context.Background(), validBatch(t)); err != nil {
		t.Fatal(err)
	}
	inputs := encoder.snapshot()
	if len(inputs) != 3 {
		t.Fatalf("encoded %d changes, want 3", len(inputs))
	}
	for index, input := range inputs {
		if input.Version != int64(100+index) {
			t.Fatalf("change %d was encoded with version %d, want %d", index+1, input.Version, 100+index)
		}
	}
}
