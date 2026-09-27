package folding

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

func Diacritics(value string) string {
	decomposed := norm.NFD.String(value)
	var result strings.Builder
	result.Grow(len(decomposed))
	for _, character := range decomposed {
		if unicode.Is(unicode.M, character) {
			continue
		}
		result.WriteRune(character)
	}
	return norm.NFC.String(result.String())
}
