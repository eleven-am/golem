package dataincarnation

import "testing"

func TestValueRequiresCanonicalNonzeroIdentity(t *testing.T) {
	for _, text := range []string{"", "00000000000000000000000000000000", "ABCDEF0123456789abcdef0123456789", "abcdef", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if _, err := Parse(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	one, err := New()
	if err != nil {
		t.Fatal(err)
	}
	two, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !one.Valid() || !two.Valid() || one == two {
		t.Fatal("random identities must be distinct and valid")
	}
	parsed, err := Parse(one.String())
	if err != nil || parsed != one {
		t.Fatalf("round trip: %v", err)
	}
	if (Value{}).Valid() || (Value{}).String() != "" {
		t.Fatal("zero identity must stay absent")
	}
}
