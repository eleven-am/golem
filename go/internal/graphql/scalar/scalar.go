// Package scalar owns exact, provider-neutral GraphQL scalar coercion. Driver
// values and gqlgen defaults never choose logical types or precision.
package scalar

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
)

func invalid(detail compileerr.Detail, name string) error {
	return compileerr.New(compileerr.KindInvalidValue, detail, compileerr.Type(name))
}

func BigInt(input any) (int64, error) {
	var text string
	switch value := input.(type) {
	case string:
		text = value
	case json.Number:
		text = string(value)
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	default:
		return 0, invalid(compileerr.ValueWrongKind, "BigInt")
	}
	if text == "" || strings.HasPrefix(text, "+") || (len(text) > 1 && text[0] == '0') || strings.HasPrefix(text, "-0") {
		return 0, invalid(compileerr.ValueNotCanonical, "BigInt")
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, invalid(compileerr.ValueOutOfRange, "BigInt")
	}
	return value, nil
}

func Decimal(input any) (golem.Decimal, error) {
	text, ok := input.(string)
	if !ok {
		return golem.Decimal{}, invalid(compileerr.ValueWrongKind, "Decimal")
	}
	value, err := golem.ParseDecimal(text)
	if err != nil {
		return golem.Decimal{}, invalid(compileerr.ValueMalformed, "Decimal")
	}
	if value.String() != text {
		return golem.Decimal{}, invalid(compileerr.ValueNotCanonical, "Decimal")
	}
	return value, nil
}

func UUID(input any) (golem.UUID, error) {
	text, ok := input.(string)
	if !ok {
		return golem.UUID{}, invalid(compileerr.ValueWrongKind, "UUID")
	}
	value, err := golem.ParseUUID(text)
	if err != nil {
		return golem.UUID{}, invalid(compileerr.ValueMalformed, "UUID")
	}
	if value.String() != text {
		return golem.UUID{}, invalid(compileerr.ValueNotCanonical, "UUID")
	}
	return value, nil
}

func Date(input any) (golem.Date, error) {
	text, ok := input.(string)
	if !ok {
		return golem.Date{}, invalid(compileerr.ValueWrongKind, "Date")
	}
	value, err := golem.ParseDate(text)
	if err != nil {
		return golem.Date{}, invalid(compileerr.ValueMalformed, "Date")
	}
	if value.String() != text {
		return golem.Date{}, invalid(compileerr.ValueNotCanonical, "Date")
	}
	return value, nil
}

func Time(input any) (golem.Time, error) {
	text, ok := input.(string)
	if !ok {
		return golem.Time{}, invalid(compileerr.ValueWrongKind, "Time")
	}
	value, err := golem.ParseTime(text)
	if err != nil {
		return golem.Time{}, invalid(compileerr.ValueMalformed, "Time")
	}
	if value.String() != text {
		return golem.Time{}, invalid(compileerr.ValueNotCanonical, "Time")
	}
	return value, nil
}

func DateTime(input any) (time.Time, error) {
	text, ok := input.(string)
	if !ok {
		return time.Time{}, invalid(compileerr.ValueWrongKind, "DateTime")
	}
	value, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, invalid(compileerr.ValueMalformed, "DateTime")
	}
	if value.Year() < 1 || value.Year() > 9999 || value.Nanosecond()%1_000 != 0 {
		return time.Time{}, invalid(compileerr.ValueOutOfRange, "DateTime")
	}
	return value.UTC(), nil
}

func Bytes(input any) ([]byte, error) {
	text, ok := input.(string)
	if !ok {
		return nil, invalid(compileerr.ValueWrongKind, "Bytes")
	}
	value, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || base64.StdEncoding.EncodeToString(value) != text {
		return nil, invalid(compileerr.ValueNotCanonical, "Bytes")
	}
	return value, nil
}

func Float(input any, bits int) (float64, error) {
	var value float64
	switch raw := input.(type) {
	case float64:
		value = raw
	case json.Number:
		if !json.Valid([]byte(raw)) {
			return 0, invalid(compileerr.ValueMalformed, "Float")
		}
		parsed, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return 0, invalid(compileerr.ValueMalformed, "Float")
		}
		value = parsed
	case int:
		value = float64(raw)
	case int64:
		value = float64(raw)
	default:
		return 0, invalid(compileerr.ValueWrongKind, "Float")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, invalid(compileerr.ValueNotFinite, "Float")
	}
	if bits == 32 {
		narrowed := float32(value)
		if math.IsInf(float64(narrowed), 0) {
			return 0, invalid(compileerr.ValueOutOfRange, "Float")
		}
		value = float64(narrowed)
	}
	return value, nil
}

type JSONLimits struct{ MaxBytes, MaxDepth, MaxNodes int }

func JSON(encoded []byte, limits JSONLimits) (any, error) {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 512 << 10
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = 32
	}
	if limits.MaxNodes <= 0 {
		limits.MaxNodes = 32_768
	}
	if len(encoded) > limits.MaxBytes {
		return nil, compileerr.New(compileerr.KindInputLimit, compileerr.LimitJSONBytes, compileerr.Max(limits.MaxBytes))
	}
	if !utf8.Valid(encoded) {
		return nil, invalid(compileerr.ValueMalformed, "JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid(compileerr.ValueMalformed, "JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalid(compileerr.ValueMalformed, "JSON")
	}
	nodes := 0
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		if depth > limits.MaxDepth {
			return compileerr.New(compileerr.KindInputLimit, compileerr.LimitJSONDepth, compileerr.Max(limits.MaxDepth))
		}
		nodes++
		if nodes > limits.MaxNodes {
			return compileerr.New(compileerr.KindInputLimit, compileerr.LimitJSONNodes, compileerr.Max(limits.MaxNodes))
		}
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			for _, child := range item {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(value, 1); err != nil {
		return nil, err
	}
	return value, nil
}

func SerializeBigInt(value int64) string          { return strconv.FormatInt(value, 10) }
func SerializeDecimal(value golem.Decimal) string { return value.String() }
func SerializeUUID(value golem.UUID) string       { return value.String() }
func SerializeDate(value golem.Date) string       { return value.String() }
func SerializeTime(value golem.Time) string       { return value.String() }
func SerializeDateTime(value time.Time) string    { return value.UTC().Format(time.RFC3339Nano) }
func SerializeBytes(value []byte) string          { return base64.StdEncoding.EncodeToString(value) }
