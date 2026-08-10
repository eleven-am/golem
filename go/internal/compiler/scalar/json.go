package scalar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type jsonKind uint8

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonString
	jsonNumber
	jsonArray
	jsonObject
)

type jsonMember struct {
	key   string
	value jsonValue
}

type jsonValue struct {
	kind    jsonKind
	boolean bool
	text    string
	array   []jsonValue
	object  []jsonMember
}

// CanonicalJSON rejects duplicate keys, invalid UTF-8, trailing data, and
// non-JSON numbers. Object keys are sorted by Unicode code-point order and
// numbers are normalized without conversion through float64.
func CanonicalJSON(input []byte) ([]byte, error) {
	value, err := parseJSON(input)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	writeJSONValue(&output, value)
	return output.Bytes(), nil
}

func parseJSON(input []byte) (jsonValue, error) {
	if !utf8.Valid(input) {
		return jsonValue{}, fmt.Errorf("JSON contains invalid UTF-8")
	}
	if err := validateJSONUnicodeEscapes(input); err != nil {
		return jsonValue{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return jsonValue{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return jsonValue{}, fmt.Errorf("JSON contains trailing data")
		}
		return jsonValue{}, fmt.Errorf("JSON trailing data: %w", err)
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (jsonValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return jsonValue{}, fmt.Errorf("invalid JSON: %w", err)
	}
	switch value := token.(type) {
	case nil:
		return jsonValue{kind: jsonNull}, nil
	case bool:
		return jsonValue{kind: jsonBool, boolean: value}, nil
	case string:
		return jsonValue{kind: jsonString, text: value}, nil
	case json.Number:
		normalized, err := normalizeJSONNumber(string(value))
		if err != nil {
			return jsonValue{}, err
		}
		return jsonValue{kind: jsonNumber, text: normalized}, nil
	case json.Delim:
		switch value {
		case '[':
			result := jsonValue{kind: jsonArray, array: []jsonValue{}}
			for decoder.More() {
				child, err := decodeJSONValue(decoder)
				if err != nil {
					return jsonValue{}, err
				}
				result.array = append(result.array, child)
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
				return jsonValue{}, fmt.Errorf("invalid JSON array closing delimiter")
			}
			return result, nil
		case '{':
			result := jsonValue{kind: jsonObject, object: []jsonMember{}}
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return jsonValue{}, fmt.Errorf("invalid JSON object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return jsonValue{}, fmt.Errorf("invalid JSON object key")
				}
				if _, duplicate := seen[key]; duplicate {
					return jsonValue{}, fmt.Errorf("JSON object repeats key %q", key)
				}
				seen[key] = struct{}{}
				child, err := decodeJSONValue(decoder)
				if err != nil {
					return jsonValue{}, err
				}
				result.object = append(result.object, jsonMember{key: key, value: child})
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
				return jsonValue{}, fmt.Errorf("invalid JSON object closing delimiter")
			}
			sort.Slice(result.object, func(i, j int) bool {
				return result.object[i].key < result.object[j].key
			})
			return result, nil
		default:
			return jsonValue{}, fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	default:
		return jsonValue{}, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func writeJSONValue(output *bytes.Buffer, value jsonValue) {
	switch value.kind {
	case jsonNull:
		output.WriteString("null")
	case jsonBool:
		output.WriteString(strconv.FormatBool(value.boolean))
	case jsonString:
		writeJSONString(output, value.text)
	case jsonNumber:
		output.WriteString(value.text)
	case jsonArray:
		output.WriteByte('[')
		for index, child := range value.array {
			if index > 0 {
				output.WriteByte(',')
			}
			writeJSONValue(output, child)
		}
		output.WriteByte(']')
	case jsonObject:
		output.WriteByte('{')
		for index, member := range value.object {
			if index > 0 {
				output.WriteByte(',')
			}
			writeJSONString(output, member.key)
			output.WriteByte(':')
			writeJSONValue(output, member.value)
		}
		output.WriteByte('}')
	}
}

func writeJSONString(output *bytes.Buffer, value string) {
	encoded, _ := json.Marshal(value)
	output.Write(encoded)
}

func validateJSONUnicodeEscapes(input []byte) error {
	inString := false
	for index := 0; index < len(input); index++ {
		switch input[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(input) {
				continue
			}
			index++
			if input[index] != 'u' {
				continue
			}
			code, ok := decodeHexQuad(input, index+1)
			if !ok {
				continue // The JSON decoder reports the lexical error.
			}
			index += 4
			switch {
			case code >= 0xd800 && code <= 0xdbff:
				if index+6 >= len(input) || input[index+1] != '\\' || input[index+2] != 'u' {
					return fmt.Errorf("JSON string contains an unpaired high surrogate")
				}
				low, valid := decodeHexQuad(input, index+3)
				if !valid || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("JSON string contains an unpaired high surrogate")
				}
				index += 6
			case code >= 0xdc00 && code <= 0xdfff:
				return fmt.Errorf("JSON string contains an unpaired low surrogate")
			}
		}
	}
	return nil
}

func decodeHexQuad(input []byte, start int) (uint16, bool) {
	if start+4 > len(input) {
		return 0, false
	}
	var result uint16
	for _, value := range input[start : start+4] {
		result <<= 4
		switch {
		case value >= '0' && value <= '9':
			result |= uint16(value - '0')
		case value >= 'a' && value <= 'f':
			result |= uint16(value-'a') + 10
		case value >= 'A' && value <= 'F':
			result |= uint16(value-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

func normalizeJSONNumber(input string) (string, error) {
	negative := false
	if strings.HasPrefix(input, "-") {
		negative = true
		input = input[1:]
	}
	parts := strings.FieldsFunc(input, func(r rune) bool { return r == 'e' || r == 'E' })
	if len(parts) > 2 || len(parts) == 0 {
		return "", fmt.Errorf("invalid JSON number")
	}
	mantissa := parts[0]
	exponent := new(big.Int)
	if len(parts) == 2 {
		if _, ok := exponent.SetString(parts[1], 10); !ok {
			return "", fmt.Errorf("invalid JSON number exponent")
		}
	}
	dot := strings.IndexByte(mantissa, '.')
	fractionDigits := 0
	if dot >= 0 {
		fractionDigits = len(mantissa) - dot - 1
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "0", nil
	}
	exponent.Sub(exponent, big.NewInt(int64(fractionDigits)))
	for strings.HasSuffix(mantissa, "0") {
		mantissa = strings.TrimSuffix(mantissa, "0")
		exponent.Add(exponent, big.NewInt(1))
	}
	prefix := ""
	if negative {
		prefix = "-"
	}
	if exponent.Sign() == 0 {
		return prefix + mantissa, nil
	}
	return prefix + mantissa + "e" + exponent.String(), nil
}
