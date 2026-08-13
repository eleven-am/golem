package contract

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestSpaceAndIndexContractsAreCanonicalAndStrict(t *testing.T) {
	space := Space{Name: "content", Dimensions: 384}
	encoded, err := Encode(space)
	if err != nil || encoded != `{"name":"content","dimensions":384}` {
		t.Fatalf("space=%q err=%v", encoded, err)
	}
	decoded, err := DecodeSpace(encoded)
	if err != nil || decoded != space {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	index := Index{Name: "related", Space: "content", Dimensions: 384, Fields: []string{"field-a", "field-b"}, Metric: "cosine"}
	encoded, err = Encode(index)
	if err != nil {
		t.Fatal(err)
	}
	decodedIndex, err := DecodeIndex(encoded)
	if err != nil || !reflect.DeepEqual(decodedIndex, index) {
		t.Fatalf("decoded=%#v err=%v", decodedIndex, err)
	}
	for _, invalid := range []string{
		`{"dimensions":384,"name":"content"}`,
		`{"name":"content","dimensions":384,"unknown":true}`,
		`{"name":"content","dimensions":384} {}`,
	} {
		if _, err := DecodeSpace(invalid); err == nil {
			t.Fatalf("invalid space accepted: %s", invalid)
		}
	}
}

func TestIndexesByModelOwnsProviderNeutralProjection(t *testing.T) {
	payload, _ := Encode(Index{Name: "content", Space: "text", Dimensions: 3, Fields: []string{"field"}, Metric: "cosine"})
	model := ir.ModelIR{Extensions: []ir.ProviderExtensionIR{
		{Provider: ir.PostgreSQL, Owner: "record", Kind: IndexKind, Payload: payload},
		{Provider: ir.SQLite, Owner: "record", Kind: IndexKind, Payload: payload},
	}}
	indexes, err := IndexesByModel(model)
	if err != nil || len(indexes["record"]) != 1 || indexes["record"][0].Name != "content" {
		t.Fatalf("indexes=%#v err=%v", indexes, err)
	}
	different, _ := Encode(Index{Name: "content", Space: "text", Dimensions: 4, Fields: []string{"field"}, Metric: "cosine"})
	model.Extensions[1].Payload = different
	if _, err := IndexesByModel(model); err == nil {
		t.Fatal("provider semantic-index mismatch was accepted")
	}
}

func TestExportedIndexNameIsSharedByGeneratedSurfaces(t *testing.T) {
	for input, want := range map[string]string{"content": "Content", "record-content": "RecordContent", "related_posts": "RelatedPosts"} {
		got, ok := ExportedIndexName(input)
		if !ok || got != want {
			t.Fatalf("ExportedIndexName(%q)=(%q,%t) want %q", input, got, ok, want)
		}
	}
	if _, ok := ExportedIndexName("content.v2"); ok {
		t.Fatal("invalid semantic index name formed a generated identifier")
	}
}
