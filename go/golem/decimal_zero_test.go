package golem

import "testing"

func TestDecimalZeroHasOneCanonicalForm(t *testing.T) {
	for _, scale := range []uint8{0, 1, 2, 18} {
		value, err := NewDecimal(0, scale)
		if err != nil {
			t.Fatal(err)
		}
		if value != (Decimal{}) || value.String() != "0" {
			t.Fatalf("NewDecimal(0,%d)=%#v %q; want the zero Decimal", scale, value, value.String())
		}
	}
	parsed, err := ParseDecimal("-0.00")
	if err != nil || parsed != (Decimal{}) {
		t.Fatalf("ParseDecimal(-0.00)=%#v err=%v", parsed, err)
	}
}
