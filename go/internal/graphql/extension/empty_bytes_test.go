package extension

import "testing"

func TestComputedSelectionKeepsEmptyBytesArgumentsEmptyNotNull(t *testing.T) {
	cloned := CloneComputedSelection(&ComputedSelection{Arguments: []BoundArgument{{Name: "payload", Value: []byte{}}, {Name: "nested", Value: []any{[]byte{}}}}})
	for index, value := range []any{cloned.Arguments[0].Value, cloned.Arguments[1].Value.([]any)[0]} {
		if data, ok := value.([]byte); !ok || data == nil || len(data) != 0 {
			t.Fatalf("argument %d bytes=%#v; want a non-nil empty slice", index, value)
		}
	}
}
