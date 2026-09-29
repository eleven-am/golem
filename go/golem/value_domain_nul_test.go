package golem

import (
	"errors"
	"testing"
)

func TestFreezeRejectsNULInStringAndJSONStringOperands(t *testing.T) {
	text := GeneratedTextField[freezeModel, string](freezeSecondFieldID)
	document := GeneratedJSONField[freezeModel](freezeFieldID).Root()
	tests := map[string]Predicate[freezeModel]{
		"text equality":          text.Eq("nul\x00byte"),
		"text greater than":      text.GT("nul\x00"),
		"text starts with":       text.StartsWith("nul\x00b"),
		"text ends with":         text.EndsWith("\x00byte"),
		"text contains":          text.Contains("\x00"),
		"text list membership":   text.In("plain", "nul\x00byte"),
		"JSON string equality":   document.Eq(JSONString("nul\x00byte")),
		"JSON string contains":   document.StringContains("\x00"),
		"JSON string starts":     document.StringStartsWith("nul\x00"),
		"JSON array containment": document.ArrayContains(JSONArray(JSONString("nul\x00byte"))),
	}
	for name, predicate := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := predicate.Freeze(freezeDescriptor)
			var failure *FreezeError
			if !errors.As(err, &failure) || failure.Code != FreezeInvalidValue {
				t.Fatalf("NUL operand error=%#v; want %s", err, FreezeInvalidValue)
			}
		})
	}
	if _, err := text.Eq("nul byte").Freeze(freezeDescriptor); err != nil {
		t.Fatalf("NUL-free operand was refused: %v", err)
	}
}
