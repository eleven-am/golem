package adapter

import (
	"testing"

	"github.com/eleven-am/golem/go/observe"
)

func TestNormalizationAndAttributeInventoryAreClosed(t *testing.T) {
	record := Normalize(observe.Observation{})
	want := Record{ModelID: "00000000000000000000000000000000"}
	if record != want {
		t.Fatalf("zero observation normalized to %#v", record)
	}
	if len(AttributeNames) != 14 || AttributeNames[0] != "golem.kind" || AttributeNames[len(AttributeNames)-1] != "golem.queue.type" {
		t.Fatalf("attribute inventory=%v", AttributeNames)
	}
	if SlogMessage != "golem.observation.v1" || SpanName != "golem.operation.v1" || InstrumentationScope != "github.com/eleven-am/golem/go/observe/otel" {
		t.Fatal("observation adapter identities drifted")
	}
}
