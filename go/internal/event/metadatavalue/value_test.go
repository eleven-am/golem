package metadatavalue

import (
	"testing"
	"time"
)

func TestValuePreservesExactMetadataInput(t *testing.T) {
	recorded := time.Unix(17, 23)
	input := Input{EventID: [16]byte{1}, Action: "created", CausationID: [16]byte{2}, Ordinal: 3, RecordedAt: recorded, Generation: [32]byte{4}, EventSchema: [32]byte{5}, HasEventSchema: true, ModelID: [16]byte{6}}
	value := New(input)
	schema, present := value.EventSchema()
	if value.EventID() != input.EventID || value.Action() != input.Action || value.CausationID() != input.CausationID || value.Ordinal() != input.Ordinal || !value.RecordedAt().Equal(recorded) || value.Generation() != input.Generation || schema != input.EventSchema || !present || value.ModelID() != input.ModelID {
		t.Fatalf("metadata value did not preserve input: %#v", value)
	}
}
