package key

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestUUIDTextAndBytesHaveOneCanonicalIdentity(t *testing.T) {
	text, err := Encode([]any{"123e4567-e89b-12d3-a456-426614174000"})
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := Encode([]any{[16]byte{0x12, 0x3e, 0x45, 0x67, 0xe8, 0x9b, 0x12, 0xd3, 0xa4, 0x56, 0x42, 0x66, 0x14, 0x17, 0x40, 0x00}})
	if err != nil {
		t.Fatal(err)
	}
	if text != bytes {
		t.Fatalf("text=%q bytes=%q", text, bytes)
	}
}

func TestHexShapedStringsKeepDistinctIdentities(t *testing.T) {
	values := []string{
		"ABCDEF0123456789ABCDEF0123456789",
		"abcdef0123456789abcdef0123456789",
		"abcdef01-2345-6789-abcd-ef0123456789",
		"a-b-c-d-e-f-0-1-2-3-4-5-6-7-8-9abcdef0123456789",
		"ABCDEF01-2345-6789-ABCD-EF0123456789",
		"abcdef01-2345-6789-abcd-ef0123456789 ",
	}
	seen := make(map[string]string, len(values))
	for _, value := range values {
		encoded, err := Encode([]any{value})
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if previous, collides := seen[encoded]; collides {
			t.Fatalf("%q and %q share record key %q", previous, value, encoded)
		}
		seen[encoded] = value
	}
}

func TestOnlyLowercaseCanonicalTextIsTreatedAsUUID(t *testing.T) {
	canonical, err := Encode([]any{"abcdef01-2345-6789-abcd-ef0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "golem-semantic-key:v1|37:uuid:abcdef0123456789abcdef0123456789" {
		t.Fatalf("canonical text encoded as %q", canonical)
	}
	for _, value := range []string{
		"ABCDEF01-2345-6789-ABCD-EF0123456789",
		"abcdef0123456789abcdef0123456789",
		"abcdef01-2345-6789-abcd-ef012345678g",
		"abcdef012345-6789-abcd-ef0123456789",
	} {
		encoded, err := Encode([]any{value})
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if encoded == canonical {
			t.Fatalf("%q was folded onto the canonical UUID identity", value)
		}
		if encoded != "golem-semantic-key:v1|"+opaqueString(value) {
			t.Fatalf("%q encoded as %q", value, encoded)
		}
	}
}

func opaqueString(value string) string {
	body := fmt.Sprintf("s:%d:%s", len(value), value)
	return fmt.Sprintf("%d:%s", len(body), body)
}

func TestMarkTextConversionAgreesWithByteIdentity(t *testing.T) {
	raw := [16]byte{0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee}
	encoded := hex.EncodeToString(raw[:])
	text := encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	fromText, err := Encode([]any{text})
	if err != nil {
		t.Fatal(err)
	}
	fromBytes, err := Encode([]any{raw})
	if err != nil {
		t.Fatal(err)
	}
	if fromText != fromBytes {
		t.Fatalf("text=%q bytes=%q", fromText, fromBytes)
	}
	fromSlice, err := Encode([]any{raw[:]})
	if err != nil {
		t.Fatal(err)
	}
	if fromSlice != fromBytes {
		t.Fatalf("slice=%q bytes=%q", fromSlice, fromBytes)
	}
}

func TestLongStringIdentitiesUseBoundedDistinctKeys(t *testing.T) {
	first, err := Encode([]any{strings.Repeat("a", 600)})
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode([]any{strings.Repeat("a", 600)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode([]any{strings.Repeat("a", 599) + "b"})
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == second || len(first) > maximumInlineBytes || !strings.HasPrefix(first, keyPrefix+"|sha256:") {
		t.Fatalf("long keys first=%q again=%q second=%q", first, again, second)
	}
}
