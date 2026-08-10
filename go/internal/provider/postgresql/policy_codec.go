package postgresql

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func encodePolicyValue(bound policysql.BoundValue) (any, error) {
	value := bound.Value
	if value.Kind() != bound.Type.Kind() {
		return nil, fmt.Errorf("postgresql policy codec: value/type kind mismatch")
	}
	switch value.Kind() {
	case policyir.ValueBool:
		result, _ := value.Bool()
		return result, nil
	case policyir.ValueInt16:
		result, _ := value.Signed()
		return int16(result), nil
	case policyir.ValueInt32:
		result, _ := value.Signed()
		return int32(result), nil
	case policyir.ValueInt64:
		result, _ := value.Signed()
		return result, nil
	case policyir.ValueFloat32:
		bits, _ := value.Float32Bits()
		return math.Float32frombits(bits), nil
	case policyir.ValueFloat64:
		bits, _ := value.Float64Bits()
		return math.Float64frombits(bits), nil
	case policyir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		return formatDecimal(coefficient, scale, bound.Type.Scale()), nil
	case policyir.ValueString:
		result, _ := value.Text()
		return result, nil
	case policyir.ValueBytes:
		result, _ := value.Bytes()
		return result, nil
	case policyir.ValueUUID:
		result, _ := value.UUID()
		return formatUUID(result), nil
	case policyir.ValueDate:
		year, month, day, _ := value.Date()
		return fmt.Sprintf("%04d-%02d-%02d", year, month, day), nil
	case policyir.ValueTime:
		microseconds, _ := value.Time()
		return formatClock(microseconds, bound.Type.Precision()), nil
	case policyir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		return time.Unix(seconds, int64(nanos)).UTC(), nil
	case policyir.ValueEnum:
		if len(bound.EnumWires) != 1 {
			return nil, fmt.Errorf("postgresql policy codec: enum wire label is required")
		}
		return bound.EnumWires[0], nil
	case policyir.ValueJSON:
		value, _ := value.JSON()
		var output bytes.Buffer
		if err := writePolicyJSON(&output, value); err != nil {
			return nil, err
		}
		return output.String(), nil
	case policyir.ValueScalarList:
		values, _ := value.List()
		element, ok := bound.Type.Element()
		if !ok || element.Kind() == policyir.ValueEnum && len(bound.EnumWires) != len(values) {
			return nil, fmt.Errorf("postgresql policy codec: invalid scalar-list descriptor")
		}
		var output bytes.Buffer
		output.WriteByte('[')
		for index, item := range values {
			if index > 0 {
				output.WriteByte(',')
			}
			wire := ""
			if element.Kind() == policyir.ValueEnum {
				wire = bound.EnumWires[index]
			}
			if err := writeListElement(&output, item, element, wire); err != nil {
				return nil, err
			}
		}
		output.WriteByte(']')
		return output.String(), nil
	default:
		return nil, fmt.Errorf("postgresql policy codec: unsupported value kind %d", value.Kind())
	}
}

func writeListElement(output *bytes.Buffer, value policyir.Value, typ policyir.TypeRef, enumWire string) error {
	if value.Kind() != typ.Kind() {
		return fmt.Errorf("postgresql policy codec: scalar-list element/type mismatch")
	}
	var token string
	switch typ.Kind() {
	case policyir.ValueBool:
		item, _ := value.Bool()
		token = strconv.FormatBool(item)
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		item, _ := value.Signed()
		token = strconv.FormatInt(item, 10)
	case policyir.ValueFloat32:
		bits, _ := value.Float32Bits()
		token = strconv.FormatFloat(float64(math.Float32frombits(bits)), 'g', -1, 32)
	case policyir.ValueFloat64:
		bits, _ := value.Float64Bits()
		token = strconv.FormatFloat(math.Float64frombits(bits), 'g', -1, 64)
	case policyir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		token = formatDecimal(coefficient, scale, typ.Scale())
	case policyir.ValueString:
		item, _ := value.Text()
		return writeJSONString(output, item)
	case policyir.ValueUUID:
		item, _ := value.UUID()
		return writeJSONString(output, formatUUID(item))
	case policyir.ValueDate:
		year, month, day, _ := value.Date()
		return writeJSONString(output, fmt.Sprintf("%04d-%02d-%02d", year, month, day))
	case policyir.ValueTime:
		item, _ := value.Time()
		return writeJSONString(output, formatClock(item, typ.Precision()))
	case policyir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		return writeJSONString(output, formatInstant(time.Unix(seconds, int64(nanos)).UTC(), typ.Precision()))
	case policyir.ValueEnum:
		if enumWire == "" {
			return fmt.Errorf("postgresql policy codec: enum wire label is required")
		}
		return writeJSONString(output, enumWire)
	default:
		return fmt.Errorf("postgresql policy codec: unsupported list element kind %d", typ.Kind())
	}
	output.WriteString(token)
	return nil
}

func writePolicyJSON(output *bytes.Buffer, value policyir.JSONValue) error {
	switch value.Kind() {
	case policyir.JSONNull:
		output.WriteString("null")
	case policyir.JSONBool:
		item, _ := value.Bool()
		output.WriteString(strconv.FormatBool(item))
	case policyir.JSONNumber:
		item, _ := value.Number()
		// jsonb represents numbers with PostgreSQL numeric. Reject outside that
		// physical type's documented range instead of allowing a driver/server
		// coercion to define policy semantics.
		magnitude := int64(len(item.Coefficient())) + int64(item.Exponent())
		if magnitude > 131072 || item.Exponent() < -16383 {
			return fmt.Errorf("postgresql policy codec: exact JSON number is outside jsonb numeric range")
		}
		if item.Negative() {
			output.WriteByte('-')
		}
		output.Write(item.Coefficient())
		if item.Exponent() != 0 {
			output.WriteByte('e')
			output.WriteString(strconv.FormatInt(int64(item.Exponent()), 10))
		}
	case policyir.JSONString:
		item, _ := value.Text()
		return writeJSONString(output, item)
	case policyir.JSONArray:
		items, _ := value.Array()
		output.WriteByte('[')
		for index, item := range items {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writePolicyJSON(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case policyir.JSONObject:
		members, _ := value.Object()
		output.WriteByte('{')
		for index, member := range members {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeJSONString(output, member.Key()); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := writePolicyJSON(output, member.Value()); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("postgresql policy codec: unsupported JSON kind %d", value.Kind())
	}
	return nil
}

func writeJSONString(output *bytes.Buffer, value string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	output.Write(encoded)
	return nil
}

func formatDecimal(coefficient int64, sourceScale uint8, targetScale uint16) string {
	negative := coefficient < 0
	digits := strconv.FormatUint(decimalMagnitude(coefficient), 10)
	scale := int(sourceScale)
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if sourceScale == 0 && targetScale > 0 {
		digits += "." + strings.Repeat("0", int(targetScale))
	} else if targetScale > uint16(sourceScale) {
		digits += strings.Repeat("0", int(targetScale)-int(sourceScale))
	}
	if negative && coefficient != 0 {
		digits = "-" + digits
	}
	return digits
}

func decimalMagnitude(value int64) uint64 {
	if value >= 0 {
		return uint64(value)
	}
	return uint64(-(value + 1)) + 1
}

func formatUUID(value [16]byte) string {
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func formatClock(microseconds int64, precision uint16) string {
	hours := microseconds / 3_600_000_000
	microseconds %= 3_600_000_000
	minutes := microseconds / 60_000_000
	microseconds %= 60_000_000
	seconds := microseconds / 1_000_000
	fraction := microseconds % 1_000_000
	base := fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	if precision == 0 {
		return base
	}
	return base + "." + fmt.Sprintf("%06d", fraction)[:precision]
}

func formatInstant(value time.Time, precision uint16) string {
	base := value.UTC().Format("2006-01-02T15:04:05")
	if precision == 0 {
		return base + "Z"
	}
	return base + "." + fmt.Sprintf("%09d", value.Nanosecond())[:precision] + "Z"
}
