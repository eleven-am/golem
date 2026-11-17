package graphql

import (
	"encoding/json"
	"github.com/eleven-am/golem/go/golem"
	"testing"
)

func TestGeneratedInputValuePreservesOmittedNullAndZero(t *testing.T) {
	type Nested struct{ Label golem.InputValue[string] }
	type Args struct {
		Input Nested
		Count golem.InputValue[int16]
	}
	for _, test := range []struct {
		name      string
		input     map[string]any
		set, null bool
		text      string
	}{
		{"omitted", map[string]any{}, false, false, ""},
		{"null", map[string]any{"label": nil}, true, true, ""},
		{"empty", map[string]any{"label": ""}, true, false, ""},
		{"value", map[string]any{"label": "hello"}, true, false, "hello"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, err := generatedArguments[Args]([]ComputedArgument{{Name: "input", Value: test.input}, {Name: "count", Value: golem.RuntimeOmittedInputValue()}})
			if err != nil {
				t.Fatal(err)
			}
			text, present := args.Input.Label.Get()
			if args.Input.Label.IsSet() != test.set || args.Input.Label.IsNull() != test.null || present != (test.set && !test.null) || text != test.text || args.Count.IsSet() {
				t.Fatalf("presence=%#v", args)
			}
		})
	}
	args, err := generatedArguments[Args]([]ComputedArgument{{Name: "input", Value: map[string]any{}}, {Name: "count", Value: int32(0)}})
	if err != nil {
		t.Fatal(err)
	}
	if count, present := args.Count.Get(); !present || count != 0 {
		t.Fatalf("explicit zero=%#v", args.Count)
	}
}

func TestGeneratedPresenceAwareJSONUsesTheNativeDocumentCodec(t *testing.T) {
	input := map[string]any{"exact": json.Number("9007199254740993"), "label": "value"}
	args, err := generatedArguments[struct {
		Payload golem.InputValue[golem.JSON[any]]
	}]([]ComputedArgument{{Name: "payload", Value: input}})
	if err != nil {
		t.Fatal(err)
	}
	document, present := args.Payload.Get()
	if !present || string(document.Bytes()) != `{"exact":9007199254740993,"label":"value"}` {
		t.Fatalf("native JSON input lost exactness: %s", document.Bytes())
	}
	plain, err := generatedArguments[struct{ Payload golem.JSON[any] }]([]ComputedArgument{{Name: "payload", Value: input}})
	if err != nil || string(plain.Payload.Bytes()) != string(document.Bytes()) {
		t.Fatalf("plain native JSON input=%s: %v", plain.Payload.Bytes(), err)
	}
}
