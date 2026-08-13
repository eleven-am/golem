package contract

import (
	"reflect"
	"testing"
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
