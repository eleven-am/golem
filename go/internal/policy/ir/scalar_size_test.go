package ir

import "testing"

func TestScalarCanonicalSizeMatchesEncodingWithoutPayloadCopies(t *testing.T) {
	for _, value := range []Value{
		BoolValue(true),
		{kind: ValueInt16, signed: -1}, {kind: ValueInt32, signed: 42}, {kind: ValueInt64, signed: 1 << 60},
		{kind: ValueFloat32, float32Bits: 1}, {kind: ValueFloat64, float64Bits: 1},
		{kind: ValueDecimal, decimal: DecimalValue{coefficient: 123, scale: 2}},
		{kind: ValueString, text: "\x00λ"}, {kind: ValueBytes, bytes: []byte{0, 1, 255}},
		{kind: ValueUUID, uuid: [16]byte{1}},
		{kind: ValueDate, date: DateValue{year: 2026, month: 11, day: 13}},
		{kind: ValueTime, time: TimeValue{microseconds: 123456}},
		{kind: ValueDateTime, instant: DateTimeValue{unixSeconds: -1, nanosecond: 999999000}},
		{kind: ValueEnum, enum: EnumValue{enum: EnumID{1}, value: EnumValueID{2}}},
	} {
		encoder := canonicalEncoder{}
		encoder.value(value)
		size, err := value.ScalarCanonicalSize()
		if err != nil || encoder.err != nil || size != len(encoder.buffer) {
			t.Fatalf("kind=%v measured=%d encoded=%d err=%v/%v", value.Kind(), size, len(encoder.buffer), err, encoder.err)
		}
	}
	for _, value := range []Value{{}, {kind: ValueJSON}, {kind: ValueScalarList}} {
		if _, err := value.ScalarCanonicalSize(); err == nil {
			t.Fatalf("non-scalar kind %v received a capture size", value.Kind())
		}
	}
}
