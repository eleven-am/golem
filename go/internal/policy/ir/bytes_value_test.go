package ir

import "testing"

func TestEmptyBytesValueStaysNonNilThroughEveryCopy(t *testing.T) {
	for name, input := range map[string][]byte{"empty": {}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			value := BytesValue(input)
			if err := value.Validate(); err != nil {
				t.Fatal(err)
			}
			got, ok := value.Bytes()
			if !ok || got == nil || len(got) != 0 {
				t.Fatalf("Bytes()=%#v ok=%t; want a non-nil empty slice", got, ok)
			}
			cloned, ok := value.clone().Bytes()
			if !ok || cloned == nil {
				t.Fatalf("clone Bytes()=%#v ok=%t; want a non-nil empty slice", cloned, ok)
			}
		})
	}
	if got, ok := BoolValue(true).Bytes(); ok || got != nil {
		t.Fatalf("non-bytes Bytes()=%#v ok=%t", got, ok)
	}
	if err := BoolValue(true).clone().Validate(); err != nil {
		t.Fatalf("clone populated an inactive bytes member: %v", err)
	}
}
