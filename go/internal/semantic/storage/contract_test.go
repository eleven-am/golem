package storage

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func TestLowerDecodeSemanticStorageRoundTrip(t *testing.T) {
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{"field-a", "field-b"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	extension := ir.ProviderExtensionIR{ID: "semantic-id", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}
	physical, err := Lower(extension)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := Decode(physical)
	if err != nil {
		t.Fatal(err)
	}
	want := Descriptor{ID: "semantic-id", ModelID: "model-id", Name: "related", Space: "content", Dimensions: 3, Fields: []ir.FieldID{"field-a", "field-b"}, Metric: "cosine", Storage: "_golem_semantic_semantic-id"}
	if !reflect.DeepEqual(descriptor, want) {
		t.Fatalf("descriptor=%#v", descriptor)
	}
	physical.Attributes[0].Value.Integer = 0
	if _, err := Decode(physical); err == nil {
		t.Fatal("invalid physical semantic descriptor accepted")
	}
}
