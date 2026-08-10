package golem_test

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

func TestUUIDExactRoundTrip(t *testing.T) {
	const canonical = "550e8400-e29b-41d4-a716-446655440000"
	value, err := golem.ParseUUID(strings.ToUpper(canonical))
	if err != nil {
		t.Fatal(err)
	}
	if value.String() != canonical {
		t.Fatalf("String() = %q", value.String())
	}
	if golem.NewUUID(value.Bytes()) != value {
		t.Fatal("byte round trip changed UUID")
	}
	for _, invalid := range []string{"", "550e8400e29b41d4a716446655440000", "550e8400-e29b-41d4-a716-44665544000g"} {
		if _, err := golem.ParseUUID(invalid); err == nil {
			t.Fatalf("ParseUUID(%q) succeeded", invalid)
		}
	}
}

func TestDecimalIsCheckedNormalizedAndExact(t *testing.T) {
	tests := map[string]string{
		"001.2300": "1.23",
		"-0.00":    "0",
		"+12":      "12",
		".1":       "",
		"1.":       "",
		"1e2":      "",
	}
	for input, expected := range tests {
		value, err := golem.ParseDecimal(input)
		if expected == "" {
			if err == nil {
				t.Fatalf("ParseDecimal(%q) succeeded", input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseDecimal(%q): %v", input, err)
		}
		if value.String() != expected {
			t.Fatalf("ParseDecimal(%q).String() = %q", input, value.String())
		}
	}
	value, err := golem.NewDecimal(12300, 4)
	if err != nil || value.Coefficient() != 123 || value.Scale() != 2 {
		t.Fatalf("NewDecimal normalization = (%d, %d), %v", value.Coefficient(), value.Scale(), err)
	}
	if _, err := golem.NewDecimal(math.MaxInt64, 0); err == nil {
		t.Fatal("19-digit decimal succeeded")
	}
	if _, err := golem.ParseDecimal("0.0000000000000000001"); err == nil {
		t.Fatal("scale 19 decimal succeeded")
	}
}

func TestDateAndTimeValidation(t *testing.T) {
	date, err := golem.ParseDate("2024-02-29")
	if err != nil || date.String() != "2024-02-29" || date.Year() != 2024 || date.Month() != time.February || date.Day() != 29 {
		t.Fatalf("date = %#v, %v", date, err)
	}
	if _, err := golem.ParseDate("2023-02-29"); err == nil {
		t.Fatal("invalid leap day succeeded")
	}
	if _, err := golem.DateFromTime(time.Date(10_000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("out-of-range date succeeded")
	}

	clock, err := golem.ParseTime("23:59:59.1234")
	if err != nil {
		t.Fatal(err)
	}
	hour, minute, second, microsecond := clock.Clock()
	if hour != 23 || minute != 59 || second != 59 || microsecond != 123400 || clock.String() != "23:59:59.1234" {
		t.Fatalf("clock = %d:%d:%d.%d (%s)", hour, minute, second, microsecond, clock.String())
	}
	if _, err := golem.ParseTime("24:00:00"); err == nil {
		t.Fatal("24:00:00 succeeded")
	}
	if _, err := golem.ParseTime("12:00:00.0000001"); err == nil {
		t.Fatal("sub-microsecond time succeeded")
	}
}

func TestJSONNumberCanonicalizationIsExact(t *testing.T) {
	equivalent := []string{"1000", "1e3", "10.00e2", "100000e-2"}
	for _, input := range equivalent {
		value, err := golem.ParseJSONNumber(input)
		if err != nil {
			t.Fatalf("ParseJSONNumber(%q): %v", input, err)
		}
		if value.Canonical() != "1e3" {
			t.Fatalf("ParseJSONNumber(%q) = %q", input, value.Canonical())
		}
	}
	for _, input := range []string{"+1", "01", ".1", "1.", "NaN", "Infinity", "1e2147483648", "1.1e-2147483648"} {
		if _, err := golem.ParseJSONNumber(input); err == nil {
			t.Fatalf("ParseJSONNumber(%q) succeeded", input)
		}
	}
	decimal, err := golem.ParseDecimal("-12.50")
	if err != nil {
		t.Fatal(err)
	}
	if got := golem.JSONNumber(decimal).Canonical(); got != "-125e-1" {
		t.Fatalf("JSONNumber(decimal) = %q", got)
	}
}

func TestParseJSONCanonicalizesWithoutFloat64(t *testing.T) {
	input := []byte("{\"z\":1.2300e+40,\"a\":[null,false,\"x\"]}")
	value, err := golem.ParseJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := golem.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != "{\"a\":[null,false,\"x\"],\"z\":123e38}" {
		t.Fatalf("canonical JSON = %s", canonical)
	}

	for _, invalid := range [][]byte{
		[]byte("{\"a\":1,\"a\":2}"),
		[]byte("true false"),
		{'"', 0xff, '"'},
	} {
		if _, err := golem.ParseJSON(invalid); err == nil {
			t.Fatalf("ParseJSON(%q) succeeded", invalid)
		}
	}
}

func TestJSONConstructorsOwnNestedCollections(t *testing.T) {
	nested := golem.JSONArray(golem.JSONString("before"))
	input := map[string]golem.JSONValue{"nested": nested}
	object := golem.JSONObject(input)
	input["nested"] = golem.JSONBool(true)

	first := object.Values()
	returned := first["nested"].(golem.JSONArrayValue).Values()
	returned[0] = golem.JSONString("changed")
	delete(first, "nested")

	canonical, err := golem.CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, []byte("{\"nested\":[\"before\"]}")) {
		t.Fatalf("object changed through caller-owned collection: %s", canonical)
	}
	if !golem.JSONObject(nil).Valid() || !golem.JSONArray().Valid() {
		t.Fatal("empty containers must be valid")
	}
	if golem.JSONArray(nil).Valid() {
		t.Fatal("nil JSON value must be invalid")
	}
	var zero golem.JSONBoolValue
	if _, err := golem.CanonicalJSON(zero); err == nil {
		t.Fatal("zero JSON value succeeded")
	}
	if got, err := golem.CanonicalJSON(golem.JSONNull); err != nil || string(got) != "null" {
		t.Fatalf("JSONNull encoding = %q, %v", got, err)
	}
}

func TestJSONPathIsTypedAndCopied(t *testing.T) {
	path := golem.NewJSONPath(golem.JSONKey("profile"), golem.JSONIndex(3))
	if !path.Valid() {
		t.Fatal("path is invalid")
	}
	segments := path.Segments()
	if key, ok := segments[0].Key(); !ok || key != "profile" {
		t.Fatalf("key segment = %q, %v", key, ok)
	}
	if index, ok := segments[1].Index(); !ok || index != 3 {
		t.Fatalf("index segment = %d, %v", index, ok)
	}
	segments[0] = golem.JSONKey("changed")
	if key, _ := path.Segments()[0].Key(); key != "profile" {
		t.Fatalf("path changed through returned slice: %q", key)
	}
	if (golem.JSONPath{}).Valid() {
		t.Fatal("zero path must not mean root")
	}
	if golem.NewJSONPath(golem.JSONKey(string([]byte{0xff}))).Valid() {
		t.Fatal("invalid UTF-8 key succeeded")
	}
}
