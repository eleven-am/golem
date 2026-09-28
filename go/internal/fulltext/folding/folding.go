package folding

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var markCharacters = func() string {
	var result strings.Builder
	for _, span := range unicode.M.R16 {
		for character := uint32(span.Lo); character <= uint32(span.Hi); character += uint32(span.Stride) {
			result.WriteRune(rune(character))
		}
	}
	for _, span := range unicode.M.R32 {
		for character := span.Lo; character <= span.Hi; character += span.Stride {
			result.WriteRune(rune(character))
		}
	}
	return result.String()
}()

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

func MarkCharacters() string {
	return markCharacters
}
