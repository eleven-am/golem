package custom

import "testing"

func TestEmptyBytesArgumentsAndResultsStayEmptyNotNull(t *testing.T) {
	request := Request{arguments: []Argument{{name: "payload", value: []byte{}}, {name: "nested", value: []any{[]byte{}}}, {name: "object", value: map[string]any{"payload": []byte{}}}}}
	arguments := request.Arguments()
	assertEmptyBytes(t, "argument", arguments[0].Value())
	assertEmptyBytes(t, "list argument", arguments[1].Value().([]any)[0])
	assertEmptyBytes(t, "object argument", arguments[2].Value().(map[string]any)["payload"])
	assertEmptyBytes(t, "result", Result{value: []byte{}}.Value())
}

func assertEmptyBytes(t *testing.T, path string, value any) {
	t.Helper()
	data, ok := value.([]byte)
	if !ok || data == nil || len(data) != 0 {
		t.Fatalf("%s bytes=%#v; want a non-nil empty slice", path, value)
	}
}
