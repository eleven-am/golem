package observation

import "testing"

func TestEmitterRegistrationIsFirstWriterAndEmitIsNilSafe(t *testing.T) {
	called := 0
	RegisterEmitter(func(any, Value) { called++ })
	RegisterEmitter(func(any, Value) { called += 100 })
	Emit(nil, Value{KindValue: "runtime"})
	if called != 1 {
		t.Fatalf("emitter calls=%d", called)
	}
}
