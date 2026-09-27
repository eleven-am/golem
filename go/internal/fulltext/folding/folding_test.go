package folding

import "testing"

func TestDiacriticsFoldsUnicodeMarks(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{name: "latin composed", input: "Renée", want: "Renee"},
		{name: "latin decomposed", input: "Rene\u0301e", want: "Renee"},
		{name: "greek", input: "Καφές", want: "Καφες"},
		{name: "vietnamese", input: "Tiếng Việt", want: "Tieng Viet"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Diacritics(test.input); got != test.want {
				t.Fatalf("Diacritics(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
