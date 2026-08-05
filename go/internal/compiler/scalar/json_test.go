package scalar

import "testing"

func TestCanonicalJSON(t *testing.T) {
	canonical, err := CanonicalJSON([]byte(` { "z": [1.2300e2, true], "a": {"b":null} } `))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), `{"a":{"b":null},"z":[123,true]}`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalJSONRejectsAmbiguity(t *testing.T) {
	inputs := [][]byte{
		[]byte(`{"a":1,"a":2}`),
		[]byte(`[] true`),
		[]byte(`"\ud800"`),
		[]byte(`"\udc00"`),
		{0xff},
	}
	for _, input := range inputs {
		if _, err := CanonicalJSON(input); err == nil {
			t.Fatalf("expected %q to fail", input)
		}
	}
}

func TestCanonicalJSONWritesValidJSONStringEscapes(t *testing.T) {
	canonical, err := CanonicalJSON([]byte(`{"control":"\u0001","astral":"\ud83d\ude00"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalJSON(canonical); err != nil {
		t.Fatalf("canonical output is not valid JSON: %q: %v", canonical, err)
	}
}

func TestCanonicalJSONPreservesExactLargeNumbers(t *testing.T) {
	canonical, err := CanonicalJSON([]byte(`90071992547409931234567890.000`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), "9007199254740993123456789e1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalJSONSupportsArbitraryPrecisionExponent(t *testing.T) {
	canonical, err := CanonicalJSON([]byte(`1e2147483648`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), "1e2147483648"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
