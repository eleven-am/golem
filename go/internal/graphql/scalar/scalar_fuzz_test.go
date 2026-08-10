package scalar

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func FuzzGraphQLExactScalarTextRoundTrip(f *testing.F) {
	seeds := []struct {
		kind  byte
		value string
	}{
		{0, "0"}, {0, "-1"}, {0, "9223372036854775807"}, {0, "9223372036854775808"}, {0, "+1"}, {0, "01"},
		{1, "0"}, {1, "-123.45"}, {1, "999999999999999999"}, {1, "0.000000000000000001"}, {1, "1e3"}, {1, "-0"},
		{2, "018f0000-0000-7000-8000-000000000001"}, {2, "018F0000-0000-7000-8000-000000000001"}, {2, "not-a-uuid"},
		{3, "0001-01-01"}, {3, "2024-02-29"}, {3, "9999-12-31"}, {3, "2023-02-29"},
		{4, "00:00:00"}, {4, "23:59:59.999999"}, {4, "01:02:03.1"}, {4, "24:00:00"}, {4, "01:02:03.000000"},
		{5, "0001-01-01T00:00:00Z"}, {5, "2024-02-29T23:59:59.123456+01:30"}, {5, "9999-12-31T23:59:59.999999Z"}, {5, "2024-01-01T00:00:00.000000001Z"},
		{6, ""}, {6, "YQ=="}, {6, "AAEC/w=="}, {6, "YQ"}, {6, "===="},
	}
	for _, seed := range seeds {
		f.Add(seed.kind, seed.value)
	}

	f.Fuzz(func(t *testing.T, kind byte, input string) {
		if len(input) > 512 {
			t.Skip()
		}
		switch kind % 7 {
		case 0:
			value, err := BigInt(input)
			if err != nil {
				return
			}
			encoded := SerializeBigInt(value)
			if encoded != input {
				t.Fatalf("accepted noncanonical BigInt %q as %q", input, encoded)
			}
			roundTrip, err := BigInt(encoded)
			if err != nil || roundTrip != value {
				t.Fatalf("BigInt round trip %q: value=%d round=%d err=%v", input, value, roundTrip, err)
			}
		case 1:
			value, err := Decimal(input)
			if err != nil {
				return
			}
			encoded := SerializeDecimal(value)
			if encoded != input {
				t.Fatalf("accepted noncanonical Decimal %q as %q", input, encoded)
			}
			roundTrip, err := Decimal(encoded)
			if err != nil || roundTrip != value {
				t.Fatalf("Decimal round trip %q: value=%v round=%v err=%v", input, value, roundTrip, err)
			}
		case 2:
			value, err := UUID(input)
			if err != nil {
				return
			}
			encoded := SerializeUUID(value)
			if encoded != input {
				t.Fatalf("accepted noncanonical UUID %q as %q", input, encoded)
			}
			roundTrip, err := UUID(encoded)
			if err != nil || roundTrip != value {
				t.Fatalf("UUID round trip %q: value=%v round=%v err=%v", input, value, roundTrip, err)
			}
		case 3:
			value, err := Date(input)
			if err != nil {
				return
			}
			encoded := SerializeDate(value)
			if encoded != input {
				t.Fatalf("accepted noncanonical Date %q as %q", input, encoded)
			}
			roundTrip, err := Date(encoded)
			if err != nil || roundTrip != value {
				t.Fatalf("Date round trip %q: value=%v round=%v err=%v", input, value, roundTrip, err)
			}
		case 4:
			value, err := Time(input)
			if err != nil {
				return
			}
			encoded := SerializeTime(value)
			if encoded != input {
				t.Fatalf("accepted noncanonical Time %q as %q", input, encoded)
			}
			roundTrip, err := Time(encoded)
			if err != nil || roundTrip != value {
				t.Fatalf("Time round trip %q: value=%v round=%v err=%v", input, value, roundTrip, err)
			}
		case 5:
			value, err := DateTime(input)
			if err != nil {
				return
			}
			encoded := SerializeDateTime(value)
			roundTrip, err := DateTime(encoded)
			if err != nil || !roundTrip.Equal(value) || roundTrip.Nanosecond() != value.Nanosecond() {
				t.Fatalf("DateTime round trip %q through %q: value=%v round=%v err=%v", input, encoded, value, roundTrip, err)
			}
		case 6:
			value, err := Bytes(input)
			if err != nil {
				return
			}
			encoded := SerializeBytes(value)
			if encoded != input {
				t.Fatalf("accepted noncanonical Bytes %q as %q", input, encoded)
			}
			roundTrip, err := Bytes(encoded)
			if err != nil || !bytes.Equal(roundTrip, value) {
				t.Fatalf("Bytes round trip %q: value=%x round=%x err=%v", input, value, roundTrip, err)
			}
		}
	})
}

func FuzzGraphQLBytesBinaryRoundTrip(f *testing.F) {
	for _, seed := range [][]byte{nil, {}, {0}, {0, 1, 2, 0xff}, bytes.Repeat([]byte{0xa5}, 257)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 4096 {
			t.Skip()
		}
		encoded := base64.StdEncoding.EncodeToString(input)
		decoded, err := Bytes(encoded)
		if err != nil || !bytes.Equal(decoded, input) || SerializeBytes(decoded) != encoded {
			t.Fatalf("binary Bytes round trip: encoded=%q decoded=%x err=%v", encoded, decoded, err)
		}
	})
}

func FuzzGraphQLJSONExactNumbersAndBounds(f *testing.F) {
	seeds := [][]byte{
		[]byte(`null`),
		[]byte(`{"integer":9007199254740993,"decimal":1.0000000000000001,"negative":-9223372036854775809}`),
		[]byte(`{"nested":[{"value":1e-18}]}`),
		[]byte(`{} {}`),
		[]byte(`[[[[1]]]]`),
		append([]byte{'"'}, []byte{0xff, '"'}...),
		bytes.Repeat([]byte{' '}, 2049),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 4096 {
			t.Skip()
		}
		limits := JSONLimits{MaxBytes: 2048, MaxDepth: 16, MaxNodes: 512}
		value, err := JSON(encoded, limits)
		if err != nil {
			return
		}
		if len(encoded) > limits.MaxBytes {
			t.Fatalf("JSON accepted %d bytes beyond limit %d", len(encoded), limits.MaxBytes)
		}
		assertJSONHasNoFloat64(t, value)
		canonical, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("accepted JSON cannot be encoded: %v", err)
		}
		roundTrip, err := JSON(canonical, limits)
		if err != nil {
			t.Fatalf("accepted JSON cannot round trip through %q: %v", canonical, err)
		}
		assertJSONHasNoFloat64(t, roundTrip)
		if !reflect.DeepEqual(value, roundTrip) {
			t.Fatalf("JSON round trip changed exact value: before=%#v after=%#v", value, roundTrip)
		}
	})
}

func FuzzGraphQLFloatFiniteCoercion(f *testing.F) {
	for _, seed := range []struct {
		text string
		bits byte
	}{
		{"0", 0}, {"-0", 1}, {"1.5", 0}, {"3.4028235e38", 0}, {"3.5e38", 0}, {"1.7976931348623157e308", 1}, {"1e-300", 1}, {"1e309", 1}, {"NaN", 1}, {"+1", 1},
	} {
		f.Add(seed.text, seed.bits)
	}

	f.Fuzz(func(t *testing.T, input string, width byte) {
		if len(input) > 128 {
			t.Skip()
		}
		bits := 64
		if width%2 == 0 {
			bits = 32
		}
		value, err := Float(json.Number(input), bits)
		if err != nil {
			return
		}
		if !json.Valid([]byte(input)) {
			t.Fatalf("Float accepted a token outside JSON number grammar: %q", input)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("Float accepted a non-finite value from %q", input)
		}
		encoded := strconv.FormatFloat(value, 'g', -1, 64)
		roundTrip, err := Float(json.Number(encoded), bits)
		if err != nil || math.Float64bits(roundTrip) != math.Float64bits(value) {
			t.Fatalf("Float%d round trip %q through %q: value=%v round=%v err=%v", bits, input, encoded, value, roundTrip, err)
		}
	})
}

func assertJSONHasNoFloat64(t testing.TB, value any) {
	t.Helper()
	switch value := value.(type) {
	case float64:
		t.Fatalf("exact JSON number passed through float64: %v", value)
	case json.Number:
		if value.String() == "" {
			t.Fatal("exact JSON number has empty source token")
		}
	case []any:
		for _, child := range value {
			assertJSONHasNoFloat64(t, child)
		}
	case map[string]any:
		for _, child := range value {
			assertJSONHasNoFloat64(t, child)
		}
	}
}
