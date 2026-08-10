package sqlite

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	compilerscalar "github.com/eleven-am/golem/go/internal/compiler/scalar"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func encodePolicyJSON(value ir.JSONValue) (string, error) {
	var output bytes.Buffer
	if err := writePolicyJSON(&output, value); err != nil {
		return "", err
	}
	canonical, err := compilerscalar.CanonicalJSON(output.Bytes())
	if err != nil {
		return "", fmt.Errorf("sqlite policy codec: JSON: %w", err)
	}
	return string(canonical), nil
}

func writePolicyJSON(output *bytes.Buffer, value ir.JSONValue) error {
	switch value.Kind() {
	case ir.JSONNull:
		output.WriteString("null")
	case ir.JSONBool:
		v, _ := value.Bool()
		output.WriteString(strconv.FormatBool(v))
	case ir.JSONNumber:
		number, _ := value.Number()
		if number.Negative() {
			output.WriteByte('-')
		}
		output.Write(number.Coefficient())
		if number.Exponent() != 0 {
			output.WriteByte('e')
			output.WriteString(strconv.FormatInt(int64(number.Exponent()), 10))
		}
	case ir.JSONString:
		v, _ := value.Text()
		encoded, _ := json.Marshal(v)
		output.Write(encoded)
	case ir.JSONArray:
		values, _ := value.Array()
		output.WriteByte('[')
		for index, child := range values {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writePolicyJSON(output, child); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case ir.JSONObject:
		members, _ := value.Object()
		output.WriteByte('{')
		for index, member := range members {
			if index > 0 {
				output.WriteByte(',')
			}
			key, _ := json.Marshal(member.Key())
			output.Write(key)
			output.WriteByte(':')
			if err := writePolicyJSON(output, member.Value()); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("sqlite policy codec: unknown JSON kind %d", value.Kind())
	}
	return nil
}

func encodePolicyList(values []ir.Value, element ir.TypeRef, enumWires []string) (string, error) {
	var output strings.Builder
	output.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			output.WriteByte(',')
		}
		wires := enumWires
		if len(enumWires) > 0 {
			wires = enumWires[index : index+1]
		}
		encoded, err := encodePolicyListElement(value, element, wires)
		if err != nil {
			return "", err
		}
		output.WriteString(encoded)
	}
	output.WriteByte(']')
	canonical, err := compilerscalar.CanonicalJSON([]byte(output.String()))
	if err != nil {
		return "", fmt.Errorf("sqlite policy codec: scalar list: %w", err)
	}
	return string(canonical), nil
}

func encodePolicyListElement(value ir.Value, typ ir.TypeRef, enumWires []string) (string, error) {
	if value.Kind() != typ.Kind() {
		return "", fmt.Errorf("sqlite policy codec: list element kind mismatch")
	}
	switch typ.Kind() {
	case ir.ValueBool:
		v, _ := value.Bool()
		return strconv.FormatBool(v), nil
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64:
		v, _ := value.Signed()
		return strconv.FormatInt(v, 10), nil
	case ir.ValueFloat32:
		v, _ := value.Float32Bits()
		return strconv.FormatFloat(float64(math.Float32frombits(v)), 'g', -1, 32), nil
	case ir.ValueFloat64:
		v, _ := value.Float64Bits()
		return strconv.FormatFloat(math.Float64frombits(v), 'g', -1, 64), nil
	case ir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		return decimalJSON(coefficient, scale), nil
	case ir.ValueString:
		v, _ := value.Text()
		encoded, _ := json.Marshal(v)
		return string(encoded), nil
	case ir.ValueUUID:
		v, _ := value.UUID()
		encoded := hex.EncodeToString(v[:])
		wire := encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
		result, _ := json.Marshal(wire)
		return string(result), nil
	case ir.ValueDate:
		year, month, day, _ := value.Date()
		result, _ := json.Marshal(fmt.Sprintf("%04d-%02d-%02d", year, month, day))
		return string(result), nil
	case ir.ValueTime:
		microseconds, _ := value.Time()
		result, _ := json.Marshal(formatTime(microseconds, typ.Precision()))
		return string(result), nil
	case ir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		layout := "2006-01-02T15:04:05"
		if typ.Precision() > 0 {
			layout += "." + strings.Repeat("0", int(typ.Precision()))
		}
		layout += "Z"
		encoded, _ := json.Marshal(time.Unix(seconds, int64(nanos)).UTC().Format(layout))
		return string(encoded), nil
	case ir.ValueEnum:
		if len(enumWires) != 1 {
			return "", fmt.Errorf("sqlite policy codec: enum list wire label is missing")
		}
		encoded, _ := json.Marshal(enumWires[0])
		return string(encoded), nil
	default:
		return "", fmt.Errorf("sqlite policy codec: unsupported list element kind %d", typ.Kind())
	}
}

func decimalJSON(coefficient int64, scale uint8) string {
	if scale == 0 {
		return strconv.FormatInt(coefficient, 10)
	}
	negative := coefficient < 0
	digits := strconv.FormatInt(coefficient, 10)
	if negative {
		digits = digits[1:]
	}
	for len(digits) <= int(scale) {
		digits = "0" + digits
	}
	result := digits[:len(digits)-int(scale)] + "." + digits[len(digits)-int(scale):]
	if negative {
		result = "-" + result
	}
	return result
}
