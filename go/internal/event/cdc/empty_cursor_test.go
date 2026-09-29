package cdc

import (
	"context"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/events"
)

type cursorRecordingEncoder struct {
	fakeEncoder
	mu      sync.Mutex
	cursors [][]byte
}

func (encoder *cursorRecordingEncoder) EncodeCDC(ctx context.Context, input EncodeInput) (events.Notice, error) {
	encoder.mu.Lock()
	encoder.cursors = append(encoder.cursors, input.Cursor)
	encoder.mu.Unlock()
	return encoder.fakeEncoder.EncodeCDC(ctx, input)
}

type cursorRecordingCorrelator struct {
	cursors [][]byte
}

func (correlator *cursorRecordingCorrelator) GolemTransaction(_ context.Context, input CorrelationInput) (bool, error) {
	correlator.cursors = append(correlator.cursors, input.Cursor())
	return false, nil
}

func TestCDCKeepsAnEmptyCursorEmpty(t *testing.T) {
	encoder, correlator := &cursorRecordingEncoder{}, &cursorRecordingCorrelator{}
	input := validBatch(t)
	input.Cursor = []byte{}
	if err := testEmitter(t, &captureTransport{}, encoder, correlator).Emit(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range append(encoder.cursors, correlator.cursors...) {
		if cursor == nil || len(cursor) != 0 {
			t.Fatalf("cursor=%#v; want a non-nil empty cursor", cursor)
		}
	}
	if len(encoder.cursors) == 0 || len(correlator.cursors) == 0 {
		t.Fatalf("encoder saw %d cursors and correlator %d", len(encoder.cursors), len(correlator.cursors))
	}
}
