// Package cdctest provides the common checkpoint/replay contract harness that
// every optional CDC adapter must pass before it can be called supported.
package cdctest

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

type Fixture struct {
	NewAdapter         func() events.CDCAdapter
	Checkpoint         func() []byte
	InitialCheckpoint  []byte
	AcceptedCheckpoint []byte
	AssertBatch        func(testing.TB, events.CDCBatchInput)
}

// Run proves the adapter does not advance its adapter-owned checkpoint before
// Emit accepts the batch, replay is stable, and the required adapter-owned
// correlation capability suppresses a source transaction classified as a
// Golem write before it can become a publication.
func Run(t testing.TB, fixture Fixture) {
	t.Helper()
	if fixture.NewAdapter == nil || fixture.Checkpoint == nil {
		t.Fatal("CDC conformance fixture is incomplete")
	}
	failed := &probeEmitter{result: errors.New("transport rejected batch")}
	adapter := fixture.NewAdapter()
	if adapter == nil || events.ValidateCDCIdentity(adapter.Identity()) != nil {
		t.Fatal("adapter identity is invalid")
	}
	if err := adapter.Run(context.Background(), failed); err == nil {
		t.Fatal("adapter ignored failed Emit")
	}
	first := failed.batch(t)
	if !bytes.Equal(fixture.Checkpoint(), fixture.InitialCheckpoint) {
		t.Fatal("checkpoint advanced before transport acceptance")
	}

	accepted := &probeEmitter{}
	if err := fixture.NewAdapter().Run(context.Background(), accepted); err != nil {
		t.Fatalf("accepted adapter replay: %v", err)
	}
	second := accepted.batch(t)
	if !sameBatchShape(first, second) {
		t.Fatal("adapter replay changed the stable batch")
	}
	if !bytes.Equal(fixture.Checkpoint(), fixture.AcceptedCheckpoint) {
		t.Fatal("checkpoint did not advance after transport acceptance")
	}
	if fixture.AssertBatch != nil {
		fixture.AssertBatch(t, second)
	}

	correlatedAdapter := fixture.NewAdapter()
	gate := &correlationGateEmitter{adapter: correlatedAdapter}
	if err := correlatedAdapter.Run(context.Background(), gate); err != nil {
		t.Fatalf("correlated Golem-write replay: %v", err)
	}
	if gate.received != 1 || gate.suppressed != 1 || gate.published != 0 {
		t.Fatalf("correlated Golem write received=%d suppressed=%d published=%d", gate.received, gate.suppressed, gate.published)
	}
}

type correlationGateEmitter struct {
	adapter    events.CDCAdapter
	received   int
	suppressed int
	published  int
}

func (emitter *correlationGateEmitter) Emit(ctx context.Context, input events.CDCBatchInput) error {
	emitter.received++
	cursor := append([]byte(nil), input.Cursor...)
	correlation := events.RuntimeCDCCorrelationInput(input.SourceTransactionID, cursor)
	if len(cursor) != 0 {
		cursor[0] ^= 0xff
	}
	first := correlation.Cursor()
	if len(first) != 0 {
		first[0] ^= 0xff
	}
	if !bytes.Equal(correlation.Cursor(), input.Cursor) {
		return errors.New("correlation input did not own stable cursor bytes")
	}
	correlated, err := emitter.adapter.CorrelatesGolemTransaction(ctx, correlation)
	if err != nil {
		return err
	}
	if correlated {
		emitter.suppressed++
		return nil
	}
	emitter.published++
	return errors.New("fixture source transaction was not classified as a Golem write")
}

type probeEmitter struct {
	mu     sync.Mutex
	input  events.CDCBatchInput
	called int
	result error
}

func (emitter *probeEmitter) Emit(_ context.Context, input events.CDCBatchInput) error {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	emitter.called++
	emitter.input = cloneBatch(input)
	if !canonicalRecordedAt(input.RecordedAt) {
		return errors.New("CDC batch recorded time is not canonical UTC microsecond precision")
	}
	return emitter.result
}

func (emitter *probeEmitter) batch(t testing.TB) events.CDCBatchInput {
	t.Helper()
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.called != 1 {
		t.Fatalf("adapter called Emit %d times; harness requires one causal batch", emitter.called)
	}
	return cloneBatch(emitter.input)
}

func cloneBatch(input events.CDCBatchInput) events.CDCBatchInput {
	result := events.CDCBatchInput{SourceTransactionID: input.SourceTransactionID, RecordedAt: input.RecordedAt, Cursor: append([]byte(nil), input.Cursor...), Changes: make([]events.CDCChangeInput, len(input.Changes))}
	copy(result.Changes, input.Changes)
	return result
}

func sameBatchShape(left, right events.CDCBatchInput) bool {
	if left.SourceTransactionID != right.SourceTransactionID || left.RecordedAt != right.RecordedAt || !bytes.Equal(left.Cursor, right.Cursor) || len(left.Changes) != len(right.Changes) {
		return false
	}
	for index := range left.Changes {
		a, b := left.Changes[index], right.Changes[index]
		if a.Ordinal != b.Ordinal || a.Model != b.Model || a.Action != b.Action || !sameExactRow(a.Before, b.Before) || !sameExactRow(a.After, b.After) {
			return false
		}
	}
	return true
}

func canonicalRecordedAt(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%int(time.Microsecond) == 0
}

func sameExactRow(left, right *golem.RuntimeModelRow) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.ModelID() != right.ModelID() {
		return false
	}
	leftFields, leftExact := golem.RuntimeCDCModelRowInventory(*left)
	rightFields, rightExact := golem.RuntimeCDCModelRowInventory(*right)
	if !leftExact || !rightExact || len(leftFields) != len(rightFields) {
		return false
	}
	for index, field := range leftFields {
		if rightFields[index] != field {
			return false
		}
		leftValue := golem.RuntimeTransportField(*left, field)
		rightValue := golem.RuntimeTransportField(*right, field)
		if leftValue.State() != rightValue.State() {
			return false
		}
		leftRaw, leftPresent := leftValue.Get()
		rightRaw, rightPresent := rightValue.Get()
		if leftPresent != rightPresent || leftPresent && !reflect.DeepEqual(leftRaw, rightRaw) {
			return false
		}
	}
	return true
}
