package folding

import (
	"testing"
	"unicode"
)

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

func TestMarkCharactersContainsEveryUnicodeMark(t *testing.T) {
	seen := make(map[rune]bool)
	for _, character := range MarkCharacters() {
		if !unicode.Is(unicode.M, character) {
			t.Fatalf("non-mark character %U returned", character)
		}
		seen[character] = true
	}
	for character := rune(0); character <= unicode.MaxRune; character++ {
		if unicode.Is(unicode.M, character) && !seen[character] {
			t.Fatalf("mark character %U omitted", character)
		}
	}
}
