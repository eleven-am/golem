package runtime

import (
	"bytes"
	"context"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestRuntimeTransportBindingDecodesOnlyCanonicalHistoricalEvents(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	encoder, history := testCDCEventEncoder(t, fixture)
	after := testCDCPostRow(t, fixture, false, "broker")
	input := testCDCEncodeInput(fixture, golem.EventCreated, nil, &after)
	want, err := encoder.EncodeCDC(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := newRuntimeEventBinding(history, 0)
	if err != nil {
		t.Fatal(err)
	}
	encoded := want.Encoded()
	got, err := binding.DecodeNotice(context.Background(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventID() != want.EventID() || got.CausationID() != want.CausationID() || !bytes.Equal(got.Encoded(), want.Encoded()) {
		t.Fatal("runtime binding changed canonical notice")
	}
	encoded[0] ^= 0xff
	if bytes.Equal(encoded, got.Encoded()) {
		t.Fatal("runtime binding retained caller-owned bytes")
	}
	if _, err := binding.DecodeNotice(context.Background(), []byte("hostile")); eventErrorCode(err) != events.CodeEventCodec {
		t.Fatalf("hostile decode error=%v", err)
	}
}
