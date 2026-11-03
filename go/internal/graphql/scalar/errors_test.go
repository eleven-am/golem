package scalar

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
)

func TestScalarRefusalsAreTypedWithoutEchoingTheValue(t *testing.T) {
	const marker = "private-driver-token"
	tooDeep := strings.Repeat("[", 40) + strings.Repeat("]", 40)
	tests := []struct {
		name    string
		call    func() error
		kind    compileerr.Kind
		message string
	}{
		{"bigint-wrong-kind", func() error { _, err := BigInt(1.5); return err }, compileerr.KindInvalidValue, "Value must be of type BigInt"},
		{"bigint-not-canonical", func() error { _, err := BigInt("01"); return err }, compileerr.KindInvalidValue, "Value is not a canonical BigInt"},
		{"bigint-out-of-range", func() error { _, err := BigInt("9223372036854775808"); return err }, compileerr.KindInvalidValue, "Value is outside the BigInt range"},
		{"decimal-wrong-kind", func() error { _, err := Decimal(1); return err }, compileerr.KindInvalidValue, "Value must be of type Decimal"},
		{"decimal-malformed", func() error { _, err := Decimal(marker); return err }, compileerr.KindInvalidValue, "Value is not a valid Decimal"},
		{"decimal-not-canonical", func() error { _, err := Decimal("1.50"); return err }, compileerr.KindInvalidValue, "Value is not a canonical Decimal"},
		{"uuid-wrong-kind", func() error { _, err := UUID(1); return err }, compileerr.KindInvalidValue, "Value must be of type UUID"},
		{"uuid-malformed", func() error { _, err := UUID(marker); return err }, compileerr.KindInvalidValue, "Value is not a valid UUID"},
		{"uuid-not-canonical", func() error { _, err := UUID("018F0000-0000-7000-8000-000000000001"); return err }, compileerr.KindInvalidValue, "Value is not a canonical UUID"},
		{"date-wrong-kind", func() error { _, err := Date(1); return err }, compileerr.KindInvalidValue, "Value must be of type Date"},
		{"date-malformed", func() error { _, err := Date(marker); return err }, compileerr.KindInvalidValue, "Value is not a valid Date"},
		{"time-wrong-kind", func() error { _, err := Time(1); return err }, compileerr.KindInvalidValue, "Value must be of type Time"},
		{"time-malformed", func() error { _, err := Time(marker); return err }, compileerr.KindInvalidValue, "Value is not a valid Time"},
		{"datetime-wrong-kind", func() error { _, err := DateTime(1); return err }, compileerr.KindInvalidValue, "Value must be of type DateTime"},
		{"datetime-malformed", func() error { _, err := DateTime(marker); return err }, compileerr.KindInvalidValue, "Value is not a valid DateTime"},
		{"datetime-sub-microsecond", func() error { _, err := DateTime("2024-01-01T00:00:00.0000001Z"); return err }, compileerr.KindInvalidValue, "Value is outside the DateTime range"},
		{"bytes-wrong-kind", func() error { _, err := Bytes(1); return err }, compileerr.KindInvalidValue, "Value must be of type Bytes"},
		{"bytes-not-canonical", func() error { _, err := Bytes(marker); return err }, compileerr.KindInvalidValue, "Value is not a canonical Bytes"},
		{"float-wrong-kind", func() error { _, err := Float("x", 64); return err }, compileerr.KindInvalidValue, "Value must be of type Float"},
		{"float-not-finite", func() error { _, err := Float(math.Inf(-1), 64); return err }, compileerr.KindInvalidValue, "Value must be a finite Float"},
		{"float32-out-of-range", func() error { _, err := Float(1e300, 32); return err }, compileerr.KindInvalidValue, "Value is outside the Float range"},
		{"json-too-large", func() error { _, err := JSON([]byte(`"`+marker+`"`), JSONLimits{MaxBytes: 4}); return err }, compileerr.KindInputLimit, "Value exceeds the JSON size limit (4)"},
		{"json-not-utf8", func() error { _, err := JSON([]byte{'"', 0xff, '"'}, JSONLimits{}); return err }, compileerr.KindInvalidValue, "Value is not a valid JSON"},
		{"json-malformed", func() error { _, err := JSON([]byte(`{`+marker), JSONLimits{}); return err }, compileerr.KindInvalidValue, "Value is not a valid JSON"},
		{"json-trailing", func() error { _, err := JSON([]byte(`1 `+marker), JSONLimits{}); return err }, compileerr.KindInvalidValue, "Value is not a valid JSON"},
		{"json-too-deep", func() error { _, err := JSON([]byte(tooDeep), JSONLimits{}); return err }, compileerr.KindInputLimit, "Value exceeds the JSON depth limit (32)"},
		{"json-too-many-nodes", func() error { _, err := JSON([]byte(`[1,2,3]`), JSONLimits{MaxNodes: 3}); return err }, compileerr.KindInputLimit, "Value exceeds the JSON node limit (3)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Internal() || typed.Kind() != test.kind {
				t.Fatalf("error=%v want a typed kind %d refusal", err, test.kind)
			}
			if typed.Message() != test.message {
				t.Fatalf("message=%q want %q", typed.Message(), test.message)
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("message echoes the caller value: %q", err.Error())
			}
		})
	}
}
