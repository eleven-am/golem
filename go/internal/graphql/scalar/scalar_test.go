package scalar

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestExactScalarCoercion(t *testing.T) {
	if value, err := BigInt("9223372036854775807"); err != nil || value != math.MaxInt64 {
		t.Fatalf("BigInt=%d err=%v", value, err)
	}
	for _, invalid := range []any{float64(1), "01", "+1", "9223372036854775808"} {
		if _, err := BigInt(invalid); err == nil {
			t.Errorf("accepted BigInt %#v", invalid)
		}
	}
	if value, err := Decimal("123.45"); err != nil || value.String() != "123.45" {
		t.Fatalf("Decimal=%v err=%v", value, err)
	}
	if _, err := Decimal(json.Number("1.2")); err == nil {
		t.Fatal("Decimal accepted JSON number")
	}
	if _, err := UUID("018f0000-0000-7000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bytes("YQ=="); err != nil {
		t.Fatal(err)
	}
	if _, err := Bytes("YQ"); err == nil {
		t.Fatal("accepted noncanonical base64")
	}
}

func TestBoundedJSONPreservesNumbers(t *testing.T) {
	value, err := JSON([]byte(`{"n":9007199254740993,"items":[1,2]}`), JSONLimits{MaxBytes: 100, MaxDepth: 3, MaxNodes: 6})
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["n"].(json.Number).String() != "9007199254740993" {
		t.Fatal("JSON number lost exact text")
	}
	if _, err := JSON([]byte(`[[[1]]]`), JSONLimits{MaxDepth: 2}); err == nil {
		t.Fatal("JSON depth limit was not enforced")
	}
}

func TestGraphQLExactScalarRoundTripAndInvalidCorpus(t *testing.T) {
	uuid, err := UUID("018f0000-0000-7000-8000-000000000001")
	if err != nil || SerializeUUID(uuid) != "018f0000-0000-7000-8000-000000000001" {
		t.Fatalf("UUID round trip = %q, %v", SerializeUUID(uuid), err)
	}
	decimal, err := Decimal("-123.45")
	if err != nil || SerializeDecimal(decimal) != "-123.45" {
		t.Fatalf("Decimal round trip = %q, %v", SerializeDecimal(decimal), err)
	}
	date, err := Date("2024-02-29")
	if err != nil || SerializeDate(date) != "2024-02-29" {
		t.Fatalf("Date round trip = %q, %v", SerializeDate(date), err)
	}
	clock, err := Time("23:59:59.123456")
	if err != nil || SerializeTime(clock) != "23:59:59.123456" {
		t.Fatalf("Time round trip = %q, %v", SerializeTime(clock), err)
	}
	instant, err := DateTime("2024-02-29T23:59:59.123456+01:30")
	if err != nil || SerializeDateTime(instant) != "2024-02-29T22:29:59.123456Z" {
		t.Fatalf("DateTime round trip = %q, %v", SerializeDateTime(instant), err)
	}
	bytes, err := Bytes("AAEC/w==")
	if err != nil || SerializeBytes(bytes) != "AAEC/w==" {
		t.Fatalf("Bytes round trip = %q, %v", SerializeBytes(bytes), err)
	}

	invalid := []struct {
		name string
		call func() error
	}{
		{"uuid uppercase", func() error { _, err := UUID("018F0000-0000-7000-8000-000000000001"); return err }},
		{"decimal exponent", func() error { _, err := Decimal("1e3"); return err }},
		{"date impossible", func() error { _, err := Date("2023-02-29"); return err }},
		{"time noncanonical", func() error { _, err := Time("01:02:03.000000"); return err }},
		{"datetime nanoseconds", func() error { _, err := DateTime("2024-01-01T00:00:00.000000001Z"); return err }},
		{"bytes noncanonical", func() error { _, err := Bytes("YQ"); return err }},
	}
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			if err := item.call(); err == nil {
				t.Fatal("invalid scalar was accepted")
			}
		})
	}
	if _, err := Float(math.Inf(1), 64); err == nil {
		t.Fatal("infinite Float was accepted")
	}
	if SerializeDateTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("offset", 3600))) != "2023-12-31T23:00:00Z" {
		t.Fatal("DateTime output was not UTC-normalized")
	}
}

func TestGraphQLBigIntDecimalAndJSONNeverPassThroughFloat64(t *testing.T) {
	if value, err := BigInt(json.Number("9007199254740993")); err != nil || SerializeBigInt(value) != "9007199254740993" {
		t.Fatalf("BigInt lost exactness: %d, %v", value, err)
	}
	if _, err := BigInt(float64(9_007_199_254_740_992)); err == nil {
		t.Fatal("BigInt accepted float64")
	}
	if _, err := Decimal(json.Number("123.45")); err == nil {
		t.Fatal("Decimal accepted a JSON-number transport")
	}
	value, err := JSON([]byte(`{"integer":9007199254740993,"decimal":1.0000000000000001}`), JSONLimits{})
	if err != nil {
		t.Fatal(err)
	}
	object := value.(map[string]any)
	if object["integer"].(json.Number).String() != "9007199254740993" || object["decimal"].(json.Number).String() != "1.0000000000000001" {
		t.Fatalf("JSON numbers lost their source tokens: %#v", object)
	}
	for _, input := range [][]byte{[]byte("{} {}"), append([]byte{'"'}, []byte{0xff, '"'}...)} {
		if _, err := JSON(input, JSONLimits{}); err == nil {
			t.Fatalf("invalid JSON was accepted: %q", input)
		}
	}
}
