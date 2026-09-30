package sql

import "testing"

func TestFragmentArgsKeepEmptyBytesDistinctFromNULL(t *testing.T) {
	fragment := Fragment{args: []any{[]byte{}, []byte(nil), []byte{7}}}
	args := fragment.Args()
	if empty, ok := args[0].([]byte); !ok || empty == nil || len(empty) != 0 {
		t.Fatalf("empty bytes argument=%#v; want a non-nil empty slice that binds as a zero-length value", args[0])
	}
	if absent, ok := args[1].([]byte); !ok || absent != nil {
		t.Fatalf("nil bytes argument=%#v; want nil", args[1])
	}
	if value, ok := args[2].([]byte); !ok || len(value) != 1 || value[0] != 7 {
		t.Fatalf("bytes argument=%#v", args[2])
	}
}
